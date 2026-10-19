// Package github implements GitHub Copilot authentication and service routing.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

const (
	clientID       = "Ov23li8tweQw6odWQebz"
	apiVersion     = "2026-08-01"
	userAPIVersion = "2025-04-01"
	restAPIVersion = "2022-11-28"
)

type DeviceCode struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	issuedAt        time.Time
}

type Client struct {
	*provider.Engine
	http          *http.Client
	githubURL     string
	githubAPIURL  string
	host          string
	mu            sync.Mutex
	token         string
	baseURL       string
	accountLoaded bool
	models        map[string]provider.Model
	sleep         func(context.Context, time.Duration) error
}

var _ provider.Client = (*Client)(nil)

func NewClient(token string) *Client {
	client, _ := NewClientForHost(token, "github.com")
	return client
}

// NormalizeHost accepts a deployment hostname, never a URL or arbitrary server.
func NormalizeHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "github.com" {
		return host, nil
	}
	if len(host) > 253 || !strings.HasSuffix(host, ".ghe.com") {
		return "", errors.New("enter github.com or a GitHub Enterprise domain ending in .ghe.com")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid GitHub deployment hostname")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("use a hostname without a scheme, path, port, or credentials")
			}
		}
	}
	return host, nil
}

func fallbackBaseURL(host string) string {
	if host == "github.com" {
		return "https://api.githubcopilot.com"
	}
	return "https://copilot-api." + host
}

func NewClientForHost(token, host string) (*Client, error) {
	host, err := NormalizeHost(host)
	if err != nil {
		return nil, err
	}
	apiURL := "https://api." + host
	client := &Client{
		http:      provider.NewHTTPClient(),
		githubURL: "https://" + host, githubAPIURL: apiURL, host: host,
		token: token, baseURL: fallbackBaseURL(host), models: make(map[string]provider.Model), sleep: sleepContext,
	}
	client.Engine = provider.NewEngine(client)
	return client, nil
}

func (c *Client) Host() string { return c.host }

// Account identifies this client's signed-in user; no other deployment is queried.
func (c *Client) Account(ctx context.Context) (provider.AccountInfo, error) {
	token, _ := c.credentials()
	if token == "" {
		return provider.AccountInfo{}, errors.New("connect GitHub Copilot first")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, c.githubAPIURL+"/user", nil, token, restAPIVersion)
	if err != nil {
		return provider.AccountInfo{}, err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := provider.ReadJSON(resp, &user); err != nil {
		return provider.AccountInfo{}, err
	}
	if user.Login == "" {
		return provider.AccountInfo{}, errors.New("GitHub returned an account without a login name")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != token {
		return provider.AccountInfo{}, errors.New("the active GitHub account changed; retry the request")
	}
	return provider.AccountInfo{Login: user.Login, Host: c.host}, nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) credentials() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, c.baseURL
}

// APIBaseURL returns the account route resolved when loading its model catalog.
// It contains no credentials and does not initiate a network request.
func (c *Client) APIBaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.baseURL
}

func (c *Client) do(ctx context.Context, method, endpoint string, body any, token, version string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", provider.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if version != "" {
		req.Header.Set("X-GitHub-Api-Version", version)
	}
	return c.http.Do(req)
}

// loadAccount uses the account's regional Copilot endpoint when GitHub reports
// one. A temporary entitlement lookup failure does not turn into a login denial.
func (c *Client) loadAccount(ctx context.Context) error {
	c.mu.Lock()
	loaded, token := c.accountLoaded, c.token
	c.mu.Unlock()
	if token == "" {
		return errors.New("connect GitHub Copilot in Providers settings first")
	}
	if loaded {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.do(lookupCtx, http.MethodGet, c.githubAPIURL+"/copilot_internal/user", nil, token, userAPIVersion)
	if ctx.Err() != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return ctx.Err()
	}
	var user struct {
		ChatEnabled *bool `json:"chat_enabled"`
		CanSignup   bool  `json:"can_signup_for_limited"`
		Endpoints   struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err == nil {
		err = provider.ReadJSON(resp, &user)
	}
	if err == nil && user.ChatEnabled != nil && !*user.ChatEnabled {
		if user.CanSignup {
			return errors.New("this GitHub account needs to sign up for Copilot Free at https://github.com/features/copilot/plans")
		}
		return errors.New("this GitHub account needs an active Copilot subscription or an organization-assigned Copilot seat")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != token {
		return errors.New("the active GitHub account changed; retry the request")
	}
	if err == nil && user.Endpoints.API != "" {
		endpoint, parseErr := url.Parse(user.Endpoints.API)
		if parseErr == nil && c.allowedAPIEndpoint(endpoint) {
			c.baseURL = strings.TrimRight(user.Endpoints.API, "/")
		}
	}
	c.accountLoaded = true
	return nil
}

func (c *Client) allowedAPIEndpoint(endpoint *url.URL) bool {
	if endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Port() != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return false
	}
	host := strings.ToLower(endpoint.Hostname())
	if c.host == "github.com" {
		return host == "githubcopilot.com" || strings.HasSuffix(host, ".githubcopilot.com")
	}
	return strings.HasSuffix(host, "."+c.host)
}
