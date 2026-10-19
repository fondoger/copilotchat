package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

func (c *Client) BeginLogin(ctx context.Context) (DeviceCode, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodPost, c.githubURL+"/login/device/code", map[string]string{
		"client_id": clientID, "scope": "read:user",
	}, "", "")
	if err != nil {
		return DeviceCode{}, err
	}
	var code DeviceCode
	if err := provider.ReadJSON(resp, &code); err != nil {
		return DeviceCode{}, err
	}
	if code.DeviceCode == "" || code.UserCode == "" || code.VerificationURI == "" {
		return DeviceCode{}, errors.New("GitHub returned an incomplete device authorization response")
	}
	verification, err := url.Parse(code.VerificationURI)
	if err != nil || verification.Scheme != "https" || verification.User != nil || verification.Port() != "" || !strings.EqualFold(verification.Hostname(), c.host) {
		return DeviceCode{}, errors.New("GitHub returned a verification URL outside the selected deployment")
	}
	if code.Interval <= 0 {
		code.Interval = 5
	}
	if code.ExpiresIn <= 0 {
		code.ExpiresIn = 900
	}
	code.issuedAt = time.Now()
	return code, nil
}

// PollLogin waits until authorization succeeds, the code expires, or ctx is
// cancelled. It observes GitHub's interval and slow_down instructions.
func (c *Client) PollLogin(ctx context.Context, code DeviceCode) (string, error) {
	if code.DeviceCode == "" {
		return "", errors.New("device code is missing; start GitHub login again")
	}
	issuedAt := code.issuedAt
	if issuedAt.IsZero() {
		issuedAt = time.Now()
	}
	expiresIn := code.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 900
	}
	deadline := issuedAt.Add(time.Duration(expiresIn) * time.Second)
	if !deadline.After(time.Now()) {
		return "", errors.New("GitHub device code expired; start login again")
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	interval := time.Duration(code.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	for {
		requestCtx, cancelRequest := context.WithTimeout(ctx, 30*time.Second)
		resp, err := c.do(requestCtx, http.MethodPost, c.githubURL+"/login/oauth/access_token", map[string]string{
			"client_id": clientID, "device_code": code.DeviceCode,
			"grant_type": "urn:ietf:params:oauth:grant-type:device_code",
		}, "", "")
		var token struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
			Description string `json:"error_description"`
			Interval    int    `json:"interval"`
		}
		if err == nil {
			err = provider.ReadJSON(resp, &token)
		}
		cancelRequest()
		if err != nil {
			return "", err
		}
		if token.AccessToken != "" {
			c.mu.Lock()
			c.token, c.accountLoaded = token.AccessToken, false
			c.baseURL, c.models = fallbackBaseURL(c.host), make(map[string]provider.Model)
			c.mu.Unlock()
			if err := c.loadAccount(ctx); err != nil {
				return "", err
			}
			return token.AccessToken, nil
		}
		switch token.Error {
		case "authorization_pending":
		case "slow_down":
			if token.Interval > 0 {
				interval = time.Duration(token.Interval) * time.Second
			} else {
				interval += 5 * time.Second
			}
		case "expired_token":
			return "", errors.New("GitHub device code expired; start login again")
		case "access_denied":
			return "", errors.New("GitHub authorization was declined")
		default:
			return "", fmt.Errorf("GitHub device authorization failed: %s", token.Error)
		}
		if err := c.sleep(ctx, interval+3*time.Second); err != nil {
			return "", err
		}
	}
}
