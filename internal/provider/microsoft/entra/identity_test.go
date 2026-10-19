package entra

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	fixtureTenant = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	secondTenant  = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"
	fixtureID     = "12345678-1234-1234-1234-123456789abc"
)

func testToken(now time.Time, changes map[string]any) string {
	claims := map[string]any{"aud": Resource, "iss": "https://sts.windows.net/" + fixtureTenant + "/", "tid": fixtureTenant, "oid": fixtureID, "upn": "person@example.com", "name": "Example Person", "exp": now.Add(time.Hour).Unix()}
	for key, value := range changes {
		claims[key] = value
	}
	data, _ := json.Marshal(claims)
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(data) + ".test-signature"
}

func TestTokenClaimsBindGatewayTenantAndAccount(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, test := range []struct {
		name     string
		claims   map[string]any
		expected Account
	}{
		{"wrong resource", map[string]any{"aud": "https://graph.microsoft.com"}, Account{}},
		{"malformed tenant", map[string]any{"tid": "another-tenant"}, Account{}},
		{"issuer from another tenant", map[string]any{"tid": secondTenant}, Account{ID: fixtureID, TenantID: secondTenant}},
		{"wrong issuer", map[string]any{"iss": "https://example.com/"}, Account{}},
		{"wrong user", nil, Account{ID: "87654321-1234-1234-1234-123456789abc"}},
		{"same user in another expected tenant", nil, Account{ID: fixtureID, TenantID: secondTenant, Username: "person@example.com"}},
		{"expired", map[string]any{"exp": now.Add(-time.Hour).Unix()}, Account{}},
		{"expires now", map[string]any{"exp": now.Unix()}, Account{}},
		{"future", map[string]any{"nbf": now.Add(5*time.Minute + time.Second).Unix()}, Account{}},
		{"no oid", map[string]any{"oid": ""}, Account{}},
		{"no expiry", map[string]any{"exp": 0}, Account{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := validateAccessToken(testToken(now, test.claims), test.expected, now); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
	for _, tenantID := range []string{fixtureTenant, secondTenant} {
		for _, issuer := range []string{"https://sts.windows.net/" + tenantID + "/", "https://login.microsoftonline.com/" + tenantID + "/v2.0"} {
			account, expiry, err := ValidateAccessToken(testToken(now, map[string]any{"iss": issuer, "tid": tenantID}), Account{ID: fixtureID, TenantID: tenantID})
			if err != nil {
				t.Fatal(err)
			}
			if account.ID != fixtureID || account.TenantID != tenantID || account.Username != "person@example.com" || account.Name != "Example Person" || !expiry.Equal(now.Add(time.Hour)) {
				t.Error("unexpected identity or expiry")
			}
		}
	}
	if _, _, err := validateAccessToken(testToken(now, map[string]any{"nbf": now.Add(5 * time.Minute).Unix()}), Account{}, now); err != nil {
		t.Error("clock-skew tolerance was not applied")
	}
}

func TestTenantAndObjectIDsRequireGUIDsAndNormalizeCase(t *testing.T) {
	now := time.Now()
	for _, claim := range []string{"tid", "oid"} {
		for _, value := range []string{"", "not-a-guid", strings.Repeat("a", 36), "aaaaaaaa-bbbb-4ccc8-ddd-eeeeeeeeeeee", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeez", " " + fixtureTenant} {
			if _, _, err := ValidateAccessToken(testToken(now, map[string]any{claim: value}), Account{}); err == nil {
				t.Errorf("malformed %s accepted: %q", claim, value)
			}
		}
	}
	for _, expected := range []Account{
		{ID: fixtureID, TenantID: secondTenant},
		{ID: strings.ToUpper(fixtureID), TenantID: strings.ToUpper(secondTenant)},
	} {
		account, _, err := ValidateAccessToken(testToken(now, map[string]any{"tid": strings.ToUpper(secondTenant), "oid": strings.ToUpper(fixtureID), "iss": "https://sts.windows.net/" + strings.ToUpper(secondTenant) + "/"}), expected)
		if err != nil || account.ID != fixtureID || account.TenantID != secondTenant {
			t.Fatalf("GUID case was not normalized: %v", err)
		}
	}
}

func TestTokenIdentityUsesAvailableUsernameClaims(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name     string
		claims   map[string]any
		expected Account
		username string
	}{
		{"preferred username", map[string]any{"preferred_username": "preferred@example.com"}, Account{}, "preferred@example.com"},
		{"upn", nil, Account{}, "person@example.com"},
		{"unique name", map[string]any{"upn": "", "unique_name": "unique@example.com"}, Account{}, "unique@example.com"},
		{"email", map[string]any{"upn": "", "email": "email@example.com"}, Account{}, "email@example.com"},
		{"saved username", map[string]any{"upn": ""}, Account{Username: "saved@example.com"}, "saved@example.com"},
		{"object id", map[string]any{"upn": ""}, Account{}, fixtureID},
	} {
		t.Run(test.name, func(t *testing.T) {
			account, _, err := ValidateAccessToken(testToken(now, test.claims), test.expected)
			if err != nil {
				t.Fatal(err)
			}
			if account.Username != test.username {
				t.Errorf("username = %q, want %q", account.Username, test.username)
			}
		})
	}
	account, _, err := ValidateAccessToken(testToken(now, map[string]any{"name": ""}), Account{Name: "Saved Name"})
	if err != nil || account.Name != "Saved Name" {
		t.Error("saved display name was not preserved")
	}
}

func TestMalformedTokensDoNotExposeTheirContents(t *testing.T) {
	for _, token := range []string{
		"SECRET-TOKEN",
		"header.invalid_base64!.SECRET-SIGNATURE",
		"header." + base64.RawURLEncoding.EncodeToString([]byte(`{"aud":"SECRET-TOKEN"`)) + ".signature",
		".payload.SECRET-SIGNATURE",
		"header.payload.",
		strings.Repeat("SECRET-TOKEN", 128*1024),
	} {
		if _, _, err := ValidateAccessToken(token, Account{}); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Error("malformed token was accepted or exposed in the error")
		}
	}
}
