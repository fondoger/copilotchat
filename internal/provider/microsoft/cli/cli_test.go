package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
)

const (
	fixtureID     = "12345678-1234-1234-1234-123456789abc"
	fixtureTenant = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	secondTenant  = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"
)

func fixtureReference() Reference {
	return Reference{Account: entra.Account{ID: fixtureID, TenantID: fixtureTenant, Username: "work@example.test"}}
}
func fixtureConfig() cliConfig {
	return cliConfig{Selected: cliUser{Login: "work@example.test", Host: gateway}, Users: []cliUser{{Login: "work@example.test", Host: gateway, Kind: "loki", DerivedFrom: "entra:" + fixtureID + "." + fixtureTenant}}}
}
func writeFixtureConfig(t *testing.T, config cliConfig) string {
	t.Helper()
	dir := t.TempDir()
	data, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), append([]byte("\xef\xbb\xbf// CLI configuration\n"), data...), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func fixtureToken(account entra.Account, expiry time.Time) string {
	claims, _ := json.Marshal(map[string]any{"aud": entra.Resource, "iss": "https://sts.windows.net/" + account.TenantID + "/", "tid": account.TenantID, "oid": account.ID, "upn": account.Username, "exp": expiry.Unix()})
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"
}

func TestSelectedWorkAccountIsUnambiguousAndPinned(t *testing.T) {
	config := fixtureConfig()
	t.Setenv("COPILOT_HOME", writeFixtureConfig(t, config))
	ref, err := Selected()
	if err != nil || ref.Account != fixtureReference().Account {
		t.Fatalf("wrong reference: %v", err)
	}
	for _, change := range []func(*cliConfig){
		func(c *cliConfig) { c.Selected.Host = "https://github.com" },
		func(c *cliConfig) { c.Users[0].DerivedFrom = "entra:" + fixtureID + ".other-tenant" },
		func(c *cliConfig) {
			c.Users = append(c.Users, cliUser{Login: c.Selected.Login, Host: gateway, Kind: "loki", DerivedFrom: "entra:87654321-1234-1234-1234-123456789abc." + fixtureTenant})
		},
		func(c *cliConfig) {
			c.Users = append(c.Users, cliUser{Login: c.Selected.Login, Host: gateway, Kind: "loki", DerivedFrom: "entra:" + fixtureID + "." + secondTenant})
		},
	} {
		config = fixtureConfig()
		change(&config)
		if _, err := selectedReference(config); err == nil {
			t.Fatal("ambiguous or wrong-service identity accepted")
		}
	}
}

func TestSecondTenantCanSelectReuseAndRenewWithNormalizedIDs(t *testing.T) {
	config := fixtureConfig()
	config.Selected.Login = strings.ToUpper(config.Selected.Login)
	config.Users[0].DerivedFrom = "entra:" + strings.ToUpper(fixtureID) + "." + strings.ToUpper(secondTenant)
	// Duplicate CLI records differing only in GUID case identify one account.
	duplicate := config.Users[0]
	duplicate.DerivedFrom = strings.ToLower(duplicate.DerivedFrom)
	config.Users = append(config.Users, duplicate)
	root := writeFixtureConfig(t, config)
	ref, err := SelectedAt(root)
	if err != nil || ref.Account.ID != fixtureID || ref.Account.TenantID != secondTenant || ref.Account.Username != "work@example.test" {
		t.Fatalf("second tenant was not selected and normalized: %v", err)
	}
	valid := fixtureToken(ref.Account, time.Now().Add(time.Hour))
	current := valid
	ref.Account.ID = strings.ToUpper(ref.Account.ID)
	ref.Account.TenantID = strings.ToUpper(ref.Account.TenantID)
	ref.Account.Username = strings.ToUpper(ref.Account.Username)
	refreshes := 0
	source, err := newSource(ref, root, func(context.Context, string) error {
		refreshes++
		current = valid
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Revoke()
	source.read = func(_ context.Context, key string) (string, error) {
		identityKey := "loki:entra:" + fixtureID + "." + secondTenant
		if key != identityKey+":loki" && key != identityKey {
			t.Error("credential key was not normalized to the selected tenant")
		}
		return current, nil
	}
	if token, err := source.Token(context.Background()); err != nil || token != valid || refreshes != 0 {
		t.Fatalf("second tenant credential was not reused: %v", err)
	}
	current = fixtureToken(source.ref.Account, time.Now().Add(-time.Minute))
	if token, err := source.Token(context.Background()); err != nil || token != valid || refreshes != 1 {
		t.Fatalf("second tenant credential was not renewed: %v", err)
	}
}

func TestUnsupportedCLIIdentityFormatIsRejectedClearly(t *testing.T) {
	for _, identity := range []string{
		"",
		"entra:" + fixtureID + ".organizations",
		"entra:" + strings.Repeat("a", 36) + "." + fixtureTenant,
		"entra:" + fixtureID + ".aaaaaaaa-bbbb-4ccc8-ddd-eeeeeeeeeeee",
		"entra:" + fixtureID + ".aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeez",
		"entra:" + fixtureID + "." + fixtureTenant + ".extra",
	} {
		config := fixtureConfig()
		config.Users[0].DerivedFrom = identity
		if _, err := selectedReference(config); err == nil || !strings.Contains(err.Error(), "unsupported Microsoft work-account identity format") {
			t.Fatalf("unsupported identity did not report the CLI format error: %v", err)
		}
	}
	for _, change := range []func(*Reference){
		func(ref *Reference) { ref.Account.ID = strings.Repeat("a", 36) },
		func(ref *Reference) { ref.Account.TenantID = "organizations" },
		func(ref *Reference) { ref.Account.TenantID = "aaaaaaaa-bbbb-4ccc8-ddd-eeeeeeeeeeee" },
	} {
		ref := fixtureReference()
		change(&ref)
		if source, err := newSource(ref, t.TempDir(), nil); err == nil {
			source.Revoke()
			t.Fatal("malformed saved account identity accepted")
		}
	}
}

func TestSameObjectAndUsernameInAnotherTenantCannotBeReusedOrRefreshed(t *testing.T) {
	ref := fixtureReference()
	otherAccount := ref.Account
	otherAccount.TenantID = secondTenant
	otherToken := fixtureToken(otherAccount, time.Now().Add(time.Hour))
	current := otherToken
	refreshes := 0
	source := &Source{ref: ref, read: func(context.Context, string) (string, error) { return current, nil }, refresh: func(context.Context, string) error {
		refreshes++
		current = otherToken
		return nil
	}}
	if token, err := source.saved(context.Background()); token != "" || err == nil {
		t.Fatal("cached token from another tenant was accepted")
	}
	config := fixtureConfig()
	config.Users[0].DerivedFrom = "entra:" + fixtureID + "." + secondTenant
	source.root = writeFixtureConfig(t, config)
	current = fixtureToken(ref.Account, time.Now().Add(-time.Minute))
	if token, err := source.Token(context.Background()); token != "" || err == nil || refreshes != 0 {
		t.Fatal("renewal started for another tenant sharing the same object ID and username")
	}
	source.root = writeFixtureConfig(t, fixtureConfig())
	if token, err := source.Token(context.Background()); token != "" || err == nil || refreshes != 1 {
		t.Fatal("renewal returned a token from another tenant")
	}
}

func TestTokenReusesExactAccountAndRenewsOnlySelectedAccount(t *testing.T) {
	ref := fixtureReference()
	config := fixtureConfig()
	root := writeFixtureConfig(t, config)
	valid := fixtureToken(ref.Account, time.Now().Add(time.Hour))
	current := valid
	reads, refreshes := 0, 0
	source := &Source{ref: ref, root: root, read: func(_ context.Context, account string) (string, error) {
		reads++
		if !strings.HasPrefix(account, "loki:entra:"+fixtureID+"."+fixtureTenant) {
			t.Fatal("read unrelated credential")
		}
		return current, nil
	}, refresh: func(context.Context, string) error { refreshes++; current = valid; return nil }}
	if token, err := source.Token(context.Background()); err != nil || token != valid || refreshes != 0 {
		t.Fatal("valid token not reused")
	}
	current = fixtureToken(ref.Account, time.Now().Add(-time.Minute))
	if _, err := source.Token(context.Background()); err != nil || refreshes != 1 {
		t.Fatal("expired token not renewed", err)
	}
	current = fixtureToken(entra.Account{ID: "87654321-1234-1234-1234-123456789abc", TenantID: fixtureTenant, Username: "other@example.test"}, time.Now().Add(time.Hour))
	source.lastRefresh = time.Now().Add(-time.Hour)
	config.Selected.Login = "other@example.test"
	source.root = writeFixtureConfig(t, config)
	if _, err := source.Token(context.Background()); err == nil || refreshes != 1 {
		t.Fatal("switched CLI account was used or refreshed")
	}
	if reads == 0 {
		t.Fatal("credential reader unused")
	}
}

func TestCLIRefreshFailureIsThrottledAndEnvironmentCannotOverrideProvider(t *testing.T) {
	ref := fixtureReference()
	count := 0
	source := &Source{ref: ref, root: writeFixtureConfig(t, fixtureConfig()), read: func(context.Context, string) (string, error) { return "", errors.New("unavailable") }, refresh: func(context.Context, string) error { count++; return errors.New("renewal failed") }}
	for i := 0; i < 2; i++ {
		if _, err := source.Token(context.Background()); err == nil {
			t.Fatal("missing credential accepted")
		}
	}
	if count != 1 {
		t.Fatal("renewal retried without backoff")
	}
	env := cleanEnvironment([]string{"PATH=fixture", "gh_token=secret-fixture", "COPILOT_HOME=elsewhere", "COPILOT_LOKI_TOKEN=secret-fixture", "COPILOT_PROVIDER_BASE_URL=https://untrusted.test"}, "pinned-root")
	if len(env) != 2 || env[0] != "PATH=fixture" || env[1] != "COPILOT_HOME=pinned-root" {
		t.Fatal("CLI environment can select a different account or endpoint")
	}
}

func TestCanceledCLIRefreshCanRetryImmediately(t *testing.T) {
	ref := fixtureReference()
	valid := fixtureToken(ref.Account, time.Now().Add(time.Hour))
	current := ""
	attempts := 0
	source := &Source{ref: ref, root: writeFixtureConfig(t, fixtureConfig()), read: func(context.Context, string) (string, error) { return current, nil }, refresh: func(context.Context, string) error {
		attempts++
		if attempts == 1 {
			return context.Canceled
		}
		current = valid
		return nil
	}}
	if _, err := source.Token(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not preserved")
	}
	if token, err := source.Token(context.Background()); err != nil || token != valid || attempts != 2 {
		t.Fatal("canceled renewal prevented retry", err)
	}
}

func TestBrokerProfileAndDisconnectKeepGlobalAccountUntouched(t *testing.T) {
	global := fixtureConfig()
	global.Selected.Login = "different@example.test"
	globalRoot := writeFixtureConfig(t, global)
	t.Setenv("COPILOT_HOME", globalRoot)
	root := writeFixtureConfig(t, fixtureConfig())
	ref, err := SelectedAt(root)
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewBrokerSource(ref, root)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	source.read = func(context.Context, string) (string, error) { return "", errors.New("needs renewal") }
	source.refresh = func(ctx context.Context, profile string) error {
		if profile != root {
			t.Error("broker renewed the global CLI profile")
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := source.Token(context.Background()); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("renewal did not start")
	}
	source.Revoke()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("disconnect did not cancel renewal", err)
	}
	if _, err := source.Token(context.Background()); err == nil {
		t.Fatal("disconnected source remains usable")
	}
	after, err := readConfig(globalRoot)
	if err != nil || after.Selected.Login != global.Selected.Login {
		t.Fatal("broker modified global CLI account selection")
	}
}

func TestCredentialReadStopsOnCancellationAndRevoke(t *testing.T) {
	for _, action := range []string{"cancel request", "revoke source"} {
		t.Run(action, func(t *testing.T) {
			source, err := NewBrokerSource(fixtureReference(), writeFixtureConfig(t, fixtureConfig()))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			abort := make(chan struct{})
			defer func() { close(abort); cancel(); source.Revoke() }()
			started := make(chan struct{})
			var start sync.Once
			var refreshes atomic.Int32
			source.read = func(ctx context.Context, _ string) (string, error) {
				start.Do(func() { close(started) })
				// Model a Keychain process waiting for user interaction. It must
				// receive the request/source cancellation rather than an unrelated
				// background context with its own ten-second timeout.
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-abort:
					return "", errors.New("test ended")
				}
			}
			source.refresh = func(context.Context, string) error {
				refreshes.Add(1)
				return errors.New("unexpected renewal")
			}
			type result struct {
				token string
				err   error
			}
			done := make(chan result, 1)
			go func() { token, err := source.Token(ctx); done <- result{token, err} }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("credential read did not start")
			}
			if action == "cancel request" {
				cancel()
			} else {
				revoked := make(chan struct{})
				go func() { source.Revoke(); close(revoked) }()
				select {
				case <-revoked:
				case <-time.After(3 * time.Second):
					t.Fatal("revocation remained blocked on the credential read")
				}
			}
			select {
			case got := <-done:
				if got.token != "" || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("canceled read returned a token or wrong error: %v", got.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation did not stop the credential read")
			}
			if refreshes.Load() != 0 {
				t.Error("canceled credential read started account renewal")
			}
		})
	}
}

func TestCancellationDoesNotReturnCachedOrRenewedToken(t *testing.T) {
	for _, stage := range []string{"cached", "renewed"} {
		for _, action := range []string{"cancel request", "revoke lifetime"} {
			t.Run(stage+"/"+action, func(t *testing.T) {
				ref := fixtureReference()
				source, err := NewBrokerSource(ref, writeFixtureConfig(t, fixtureConfig()))
				if err != nil {
					t.Fatal(err)
				}
				defer source.Revoke()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				valid := fixtureToken(ref.Account, time.Now().Add(time.Hour))
				renewed := stage == "cached"
				source.refresh = func(context.Context, string) error { renewed = true; return nil }
				source.read = func(context.Context, string) (string, error) {
					if !renewed {
						return "", errors.New("credential requires renewal")
					}
					if action == "cancel request" {
						cancel()
					} else {
						// Check the lifetime directly too: AfterFunc may not yet
						// have propagated source cancellation to the child context.
						source.cancel()
					}
					return valid, nil
				}
				if token, err := source.Token(ctx); token != "" || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation exposed a %s token or wrong error: %v", stage, err)
				}
			})
		}
	}
}

func TestPlatformCredentialReadHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if token, err := readCredential(ctx, "fixture-unused"); token != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("credential reader ignored cancellation: %v", err)
	}
}
