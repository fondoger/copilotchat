// Package entra validates account identity for the Microsoft 365 Copilot gateway.
// Authentication and renewal belong to Windows WAM or the selected Copilot CLI.
// Some organizations restrict device-code grants or browser redirect flows, so
// this provider deliberately uses the broker or an existing CLI sign-in instead
// of implementing those alternative login flows.
package entra

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Resource is the supported Loki gateway's token audience, matching Copilot
// CLI's built-in default. It identifies the service, not the account's tenant.
const Resource = "42c8f7bd-c7a8-4ba9-bc1c-91edfb716247"

var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Account struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Username string `json:"username"`
	Name     string `json:"name,omitempty"`
}

// ValidateAccessToken checks routing claims from the user's local credential
// store and binds the token to the expected account. It does not replace the
// gateway's cryptographic token verification or establish authorization itself.
func ValidateAccessToken(token string, expected Account) (Account, time.Time, error) {
	return validateAccessToken(token, expected, time.Now())
}

func validateAccessToken(token string, expected Account, now time.Time) (Account, time.Time, error) {
	if len(token) > 128*1024 {
		return Account{}, time.Time{}, errors.New("Microsoft returned an invalid access token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return Account{}, time.Time{}, errors.New("Microsoft returned an invalid access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Account{}, time.Time{}, errors.New("Microsoft returned an invalid access token")
	}
	var claims struct {
		Audience   string `json:"aud"`
		Issuer     string `json:"iss"`
		Tenant     string `json:"tid"`
		ObjectID   string `json:"oid"`
		Username   string `json:"preferred_username"`
		UPN        string `json:"upn"`
		UniqueName string `json:"unique_name"`
		Email      string `json:"email"`
		Name       string `json:"name"`
		Expires    int64  `json:"exp"`
		NotBefore  int64  `json:"nbf"`
	}
	if json.Unmarshal(payload, &claims) != nil || !guidPattern.MatchString(claims.ObjectID) || !guidPattern.MatchString(claims.Tenant) || claims.Expires <= 0 {
		return Account{}, time.Time{}, errors.New("Microsoft returned an incomplete access token")
	}
	if claims.Audience != Resource {
		return Account{}, time.Time{}, errors.New("this Microsoft account token is not for the configured Microsoft 365 model gateway")
	}
	// WAM and Copilot CLI use the organizations authority. The selected
	// account supplies its tenant; the gateway still decides service access.
	claims.Tenant = strings.ToLower(claims.Tenant)
	claims.ObjectID = strings.ToLower(claims.ObjectID)
	if !strings.EqualFold(claims.Issuer, "https://sts.windows.net/"+claims.Tenant+"/") && !strings.EqualFold(claims.Issuer, "https://login.microsoftonline.com/"+claims.Tenant+"/v2.0") {
		return Account{}, time.Time{}, errors.New("Microsoft returned an unexpected token issuer")
	}
	expiry := time.Unix(claims.Expires, 0)
	if !expiry.After(now) {
		return Account{}, time.Time{}, errors.New("Microsoft sign-in expired; sign in again")
	}
	if claims.NotBefore > now.Add(5*time.Minute).Unix() {
		return Account{}, time.Time{}, errors.New("Microsoft access token is not valid yet; check the system clock")
	}
	account := Account{ID: claims.ObjectID, TenantID: claims.Tenant, Username: claims.Username, Name: claims.Name}
	if account.Username == "" {
		account.Username = claims.UPN
	}
	if account.Username == "" {
		account.Username = claims.UniqueName
	}
	if account.Username == "" {
		account.Username = claims.Email
	}
	if account.Username == "" {
		account.Username = expected.Username
	}
	if account.Username == "" {
		account.Username = account.ID
	}
	if account.Name == "" {
		account.Name = expected.Name
	}
	if (expected.ID != "" && !strings.EqualFold(expected.ID, account.ID)) || (expected.TenantID != "" && !strings.EqualFold(expected.TenantID, account.TenantID)) {
		return Account{}, time.Time{}, errors.New("Microsoft sign-in returned a different account; reconnect this provider")
	}
	return account, expiry, nil
}
