package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockResponse(r *http.Request, contentType, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestNormalizeHost(t *testing.T) {
	for input, want := range map[string]string{"github.com": "github.com", " ACME.GHE.COM ": "acme.ghe.com", "team.eu.ghe.com": "team.eu.ghe.com", "a-1.ghe.com": "a-1.ghe.com"} {
		got, err := NormalizeHost(input)
		if err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "ghe.com", "github.example.com", "https://acme.ghe.com", "acme.ghe.com/", "acme.ghe.com:443", "user@acme.ghe.com", "acme.ghe.com.", ".ghe.com", "a..ghe.com", "-acme.ghe.com", "acme-.ghe.com", "ac_me.ghe.com", "你.ghe.com", "acme.ghe.com.evil.test", "a\nghe.com", strings.Repeat("a", 64) + ".ghe.com"} {
		if _, err := NormalizeHost(input); err == nil {
			t.Errorf("invalid host accepted: %q", input)
		}
		if client, err := NewClientForHost("secret", input); err == nil || client != nil {
			t.Errorf("constructor accepted invalid host %q", input)
		}
	}
}

func TestDeploymentCredentialsAndCachesAreIndependent(t *testing.T) {
	public := NewClient("public-token")
	enterprise, err := NewClientForHost("enterprise-token", "acme.ghe.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []*Client{public, enterprise} {
		host := client.Host()
		token, _ := client.credentials()
		login, modelID, apiHost := "public-user", "public-model", "api.githubcopilot.com"
		if host != "github.com" {
			login, modelID, apiHost = "enterprise-user", "enterprise-model", "copilot-api.acme.ghe.com"
		}
		var seen []string
		client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			seen = append(seen, r.URL.Host+r.URL.Path)
			if r.URL.Scheme != "https" || r.Header.Get("Authorization") != "Bearer "+token {
				t.Errorf("wrong scheme or deployment credential on %s", r.URL)
			}
			switch r.URL.Host + r.URL.Path {
			case "api." + host + "/user":
				if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
					t.Errorf("account lookup must use the supported GitHub REST version, got %q", r.Header.Get("X-GitHub-Api-Version"))
				}
				return mockResponse(r, "application/json", fmt.Sprintf(`{"login":%q}`, login)), nil
			case "api." + host + "/copilot_internal/user":
				if r.Header.Get("X-GitHub-Api-Version") != "2025-04-01" {
					t.Errorf("entitlement lookup must retain the Copilot version, got %q", r.Header.Get("X-GitHub-Api-Version"))
				}
				// An endpoint belonging to a different deployment cannot redirect this token.
				return mockResponse(r, "application/json", `{"chat_enabled":true,"endpoints":{"api":"https://copilot-api.other.ghe.com"}}`), nil
			case apiHost + "/models":
				return mockResponse(r, "application/json", fmt.Sprintf(`{"data":[{"id":%q,"supported_endpoints":["/chat/completions"]}]}`, modelID)), nil
			case apiHost + "/chat/completions":
				return mockResponse(r, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
			default:
				return nil, fmt.Errorf("unexpected deployment URL: %s", r.URL)
			}
		})
		account, err := client.Account(context.Background())
		if err != nil || account != (AccountInfo{Login: login, Host: host}) {
			t.Fatalf("incorrect account: %#v, %v", account, err)
		}
		models, err := client.Models(context.Background())
		if err != nil || len(models) != 1 || models[0].ID != modelID {
			t.Fatalf("incorrect deployment catalog: %#v, %v", models, err)
		}
		result, err := client.StreamWithTools(context.Background(), Request{Model: modelID, Messages: []Message{{Role: "user", Content: "hello"}}}, nil)
		if err != nil || result.Content != "ok" || len(seen) != 4 {
			t.Fatalf("incorrect deployment inference: %#v, %v, %v", result, err, seen)
		}
	}
	if _, present := public.models["enterprise-model"]; present {
		t.Fatal("enterprise model leaked into public cache")
	}
	if _, present := enterprise.models["public-model"]; present {
		t.Fatal("public model leaked into enterprise cache")
	}
}

func TestEnterpriseDeviceLoginUsesConfiguredDomain(t *testing.T) {
	client, err := NewClientForHost("old-token", "acme.ghe.com")
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = "https://old-region.acme.ghe.com"
	client.models["old-model"] = Model{ID: "old-model"}
	var paths []string
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Host+r.URL.Path)
		if r.URL.Path == "/copilot_internal/user" {
			if r.URL.Host != "api.acme.ghe.com" || r.Header.Get("Authorization") != "Bearer new-token" {
				t.Error("entitlement request crossed deployment or account")
			}
			return mockResponse(r, "application/json", `{"chat_enabled":true}`), nil
		}
		if r.URL.Host != "acme.ghe.com" || r.Header.Get("Authorization") != "" {
			t.Error("device OAuth used wrong deployment or sent an existing token")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["client_id"] != clientID {
			t.Errorf("invalid OAuth body: %#v, %v", body, err)
		}
		switch r.URL.Path {
		case "/login/device/code":
			return mockResponse(r, "application/json", `{"device_code":"device","user_code":"CODE","verification_uri":"https://acme.ghe.com/login/device","expires_in":900,"interval":5}`), nil
		case "/login/oauth/access_token":
			if body["device_code"] != "device" {
				t.Error("device code was lost")
			}
			return mockResponse(r, "application/json", `{"access_token":"new-token"}`), nil
		default:
			return nil, fmt.Errorf("unexpected OAuth URL %s", r.URL)
		}
	})
	code, err := client.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.PollLogin(context.Background(), code)
	if err != nil || token != "new-token" || client.baseURL != "https://copilot-api.acme.ghe.com" || len(client.models) != 0 {
		t.Fatalf("login did not reset account state within deployment: %v, %q, %q, %v", err, token, client.baseURL, client.models)
	}
	want := []string{"acme.ghe.com/login/device/code", "acme.ghe.com/login/oauth/access_token", "api.acme.ghe.com/copilot_internal/user"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("wrong device flow: %v", paths)
	}
}

func TestUntrustedDeploymentURLsAreRejected(t *testing.T) {
	for _, host := range []string{"github.com", "acme.ghe.com"} {
		client, _ := NewClientForHost("token", host)
		allowed := "https://region.githubcopilot.com"
		if host != "github.com" {
			allowed = "https://region.acme.ghe.com"
		}
		for _, raw := range []string{allowed, allowed + "/"} {
			endpoint, _ := url.Parse(raw)
			if !client.allowedAPIEndpoint(endpoint) {
				t.Errorf("own regional endpoint rejected: %s", raw)
			}
		}
		for _, raw := range []string{"https://copilot-api.other.ghe.com", "https://evil.test", allowed + ":443", allowed + "/path", allowed + "?query=1", allowed + "#fragment", strings.Replace(allowed, "https://", "https://user@", 1), strings.Replace(allowed, "https:", "http:", 1)} {
			endpoint, _ := url.Parse(raw)
			if client.allowedAPIEndpoint(endpoint) {
				t.Errorf("unsafe endpoint accepted: %s for %s", raw, host)
			}
		}
		client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return mockResponse(r, "application/json", `{"device_code":"device","user_code":"CODE","verification_uri":"https://other.ghe.com/login/device"}`), nil
		})
		if _, err := client.BeginLogin(context.Background()); err == nil {
			t.Fatal("cross-deployment verification URL accepted")
		}
	}
}

func TestAccountChangeDiscardsStaleCatalog(t *testing.T) {
	client := NewClient("old-token")
	client.accountLoaded = true
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		client.mu.Lock()
		client.token = "new-token"
		client.mu.Unlock()
		return mockResponse(r, "application/json", `{"data":[{"id":"old-account-model"}]}`), nil
	})
	models, err := client.Models(context.Background())
	if err == nil || models != nil || len(client.models) != 0 {
		t.Fatalf("stale catalog survived account change: %#v, %v", models, err)
	}
}
