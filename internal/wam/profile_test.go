package wam

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRestoreRequiresOriginalHostGrant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	t.Setenv(profilesEnv, path)
	write := func(p Profile) {
		t.Helper()
		data, err := json.Marshal(map[string]Profile{"work": p})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(fixtureProfile())
	profile, err := ReadProfile("work")
	if err != nil || !reflect.DeepEqual(profile, fixtureProfile()) {
		t.Fatal("cannot read host profile:", err)
	}
	ref, err := signIn(context.Background(), 42, profile, func(context.Context, uintptr, registration, string) (string, string, error) {
		return fixtureToken(nil), "opaque-account", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Profile)
	}{
		{"client", func(p *Profile) { p.ClientID = p.Resource }},
		{"audience", func(p *Profile) { p.Resource = p.ClientID }},
		{"origin replaced", func(p *Profile) { p.Origins[0] = "https://another.example.test" }},
		{"origin added", func(p *Profile) { p.Origins = append(p.Origins, "https://extra.example.test") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fixtureProfile()
			tc.change(&p)
			write(p)
			current, err := ReadProfile("work")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewSource(ref, current); err == nil {
				t.Fatal("restored account after host grant changed")
			}
			// A fresh sign-in can grant the new profile, including an API that
			// issues v2 tokens with azp instead of appid.
			fresh, err := signIn(context.Background(), 42, current, func(_ context.Context, _ uintptr, reg registration, _ string) (string, string, error) {
				if reg.clientID != current.ClientID || reg.resource != current.Resource {
					t.Fatal("broker did not receive the current registration")
				}
				return fixtureToken(map[string]any{"appid": "", "azp": current.ClientID, "aud": current.Resource, "iss": "https://login.microsoftonline.com/" + ref.Account.TenantID + "/v2.0"}), ref.ID, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s, err := NewSource(fresh, current)
			if err != nil {
				t.Fatal("cannot restore freshly approved grant:", err)
			}
			s.Revoke()
		})
	}
	for _, change := range []func(*Reference){
		func(r *Reference) { r.ProfileHash = "" },
		func(r *Reference) { r.ID = " " },
		func(r *Reference) { r.Account.ID = "" },
		func(r *Reference) { r.Account.TenantID = zeroID },
	} {
		invalid := ref
		change(&invalid)
		if _, err := NewSource(invalid, profile); err == nil {
			t.Fatal("restored incomplete account reference")
		}
	}
}

const zeroID = "00000000-0000-0000-0000-000000000000"

func TestInvalidGrantsCannotOpenAccountPicker(t *testing.T) {
	check := func(p Profile) {
		t.Helper()
		_, err := signIn(context.Background(), 42, p, func(context.Context, uintptr, registration, string) (string, string, error) {
			t.Fatal("invalid grant reached the broker")
			return "", "", nil
		})
		if err == nil {
			t.Fatalf("accepted invalid grant: %+v", p)
		}
	}
	for _, id := range []string{"", zeroID, "api://not-a-guid", "{11111111-2222-4333-8444-555555555555}"} {
		p := fixtureProfile()
		p.ClientID = id
		check(p)
		p = fixtureProfile()
		p.Resource = id
		check(p)
	}
	p := fixtureProfile()
	p.Origins = nil
	check(p)
	for _, origin := range []string{
		"", " https://host", "http://host", "https://", "https://*.example.test", "https://host*", "https://host:443", "https://host:", "https://[::1]:443",
		"https://user@host", "https://user:pass@host", "https://@host", "https://host/", "https://host/path", "https://host?", "https://host?q=1", "https://host#", "https://host#section",
		"https://host\\@other", "https://host%2Fbad", "https://host\n", "https://host..test", "https://[::1%25zone]",
	} {
		p := fixtureProfile()
		p.Origins = append(p.Origins, origin)
		check(p)
	}
	p = fixtureProfile()
	p.Origins = []string{"https://example.test", "https://192.0.2.1", "https://[2001:db8::1]"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReadProfileFailsClosedWithConfigurationHelp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	check := func(name string) {
		t.Helper()
		p, err := ReadProfile(name)
		if p.ClientID != "" || err == nil || !strings.Contains(err.Error(), profilesEnv) {
			t.Fatalf("missing guidance for unavailable profile: %v", err)
		}
	}
	t.Setenv(profilesEnv, "")
	check("work")
	t.Setenv(profilesEnv, path)
	check("work")
	for _, data := range []string{"not JSON", `{"other":{}}`, `{"work":{"origins":["https://example.test"]}}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		check("work")
	}
	check(path) // An untrusted profile selector is never a file path.
}

func TestSignInDropsCredentialOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ref, err := signIn(ctx, 42, fixtureProfile(), func(context.Context, uintptr, registration, string) (string, string, error) {
		cancel()
		return fixtureToken(nil), "opaque-account", nil
	})
	if ref != (Reference{}) || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled picker created a reference")
	}
}
