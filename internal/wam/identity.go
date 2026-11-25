package wam

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Account struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Username string `json:"username"`
	Name     string `json:"name,omitempty"`
}

// Tokens come only from WAM. These checks pin the routing claims locally;
// the resource server is responsible for signature and authorization checks.
func validateAccessToken(token string, expected Account, reg registration, now time.Time) (Account, time.Time, error) {
	if len(token) > 128*1024 {
		return Account{}, time.Time{}, errors.New("WAM returned an invalid access token")
	}
	parts := strings.SplitN(token, ".", 4)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return Account{}, time.Time{}, errors.New("WAM returned an invalid access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Account{}, time.Time{}, errors.New("WAM returned an invalid access token")
	}
	var claims struct {
		Audience   string `json:"aud"`
		AppID      string `json:"appid"`
		AZP        string `json:"azp"`
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
	if json.Unmarshal(payload, &claims) != nil || !ValidID(claims.ObjectID) || !ValidID(claims.Tenant) || claims.Expires <= 0 {
		return Account{}, time.Time{}, errors.New("WAM returned an incomplete access token")
	}
	if !strings.EqualFold(claims.Audience, reg.resource) {
		return Account{}, time.Time{}, errors.New("WAM returned a token for a different resource")
	}
	if (claims.AppID == "" && claims.AZP == "") ||
		(claims.AppID != "" && !strings.EqualFold(claims.AppID, reg.clientID)) ||
		(claims.AZP != "" && !strings.EqualFold(claims.AZP, reg.clientID)) {
		return Account{}, time.Time{}, errors.New("WAM sign-in returned a token for a different application; reconnect this provider")
	}
	// The organizations authority uses the selected account's tenant.
	claims.Tenant = strings.ToLower(claims.Tenant)
	claims.ObjectID = strings.ToLower(claims.ObjectID)
	if !strings.EqualFold(claims.Issuer, "https://sts.windows.net/"+claims.Tenant+"/") && !strings.EqualFold(claims.Issuer, "https://login.microsoftonline.com/"+claims.Tenant+"/v2.0") {
		return Account{}, time.Time{}, errors.New("WAM returned an unexpected token issuer")
	}
	expiry := time.Unix(claims.Expires, 0)
	if !expiry.After(now) {
		return Account{}, time.Time{}, errors.New("WAM sign-in expired; sign in again")
	}
	if claims.NotBefore > now.Add(5*time.Minute).Unix() {
		return Account{}, time.Time{}, errors.New("WAM access token is not valid yet; check the system clock")
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
		return Account{}, time.Time{}, errors.New("WAM sign-in returned a different account; reconnect this provider")
	}
	return account, expiry, nil
}
