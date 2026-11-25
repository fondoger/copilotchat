package plugin

import (
	"context"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

// The same wire-contract check can exercise a separately maintained plugin
// that advertises a regional catalog and the Responses protocol. All HTTP is fake.
var contractPlugin = flag.String("contract-plugin", "../../examples/catalog-provider", "plugin directory for the regional catalog fixture")

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fixtureCredential struct{}

func (fixtureCredential) Token(context.Context) (string, error) { return "fixture", nil }
func (fixtureCredential) Revoke()                               {}

func TestCatalogToNativeAndGateway(t *testing.T) {
	p, err := Load(*contractPlugin)
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(p, provider.AccountInfo{ID: "account"}, fixtureCredential{})
	defer c.Revoke()
	var spec requestSpec
	if err = p.call(t.Context(), "catalog", newParams(), &spec); err != nil {
		t.Fatal(err)
	}
	region := ""
	for _, u := range spec.Redirects {
		if u != spec.URL {
			region = u
			break
		}
	}
	if !strings.HasSuffix(region, "/models") {
		t.Fatalf("fixture needs a regional /models redirect: %+v", spec)
	}
	raw := `{ "model":"sample","large":9007199254740993, "input":[] }`
	var paths []string
	c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.String())
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Fatal("missing upstream authorization")
		}
		status := 200
		headers := make(http.Header)
		body := ""
		switch r.URL.String() {
		case spec.URL:
			status = 307
			headers.Set("Location", region)
		case region:
			body = `{"data":[{"id":"sample","supported_endpoints":["/responses"],"model_picker_enabled":false},{"id":"visible","supported_endpoints":["/responses"]}]}`
		case strings.TrimSuffix(region, "/models") + "/responses":
			if r.Method != "POST" || r.Header.Get("X-ConversationId") != "chat-id" {
				t.Fatalf("invalid native request: %s, %v", r.Method, r.Header)
			}
			headers.Set("Content-Type", "text/event-stream")
			body = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
		case strings.TrimSuffix(region, "/models") + "/responses/compact?feature=on":
			b, err := io.ReadAll(r.Body)
			if err != nil || string(b) != raw || r.Header.Get("Accept") != "application/json" || r.Header.Get("OpenAI-Beta") != "fixture" || r.Header.Get("X-Local") != "" {
				t.Fatal("gateway body or headers changed")
			}
			status = 307
			headers.Set("Location", spec.URL)
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	models, err := c.Models(t.Context())
	if err != nil || len(models) != 1 || models[0].ID != "visible" {
		t.Fatalf("picker: %+v, %v", models, err)
	}
	result, err := c.StreamWithEvents(t.Context(), provider.Request{Model: "sample", ConversationID: "chat-id", Messages: []provider.Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil || result.Content != "ok" {
		t.Fatalf("native: %+v, %v", result, err)
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "local")
	hdr.Set("OpenAI-Beta", "fixture")
	hdr.Set("X-Local", "private")
	resp, err := c.Forward(t.Context(), provider.RawRequest{Endpoint: "/responses/compact", RawQuery: "feature=on", Body: []byte(raw), Header: hdr})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 307 || resp.Request.Header.Get("Authorization") != "" || len(paths) != 4 {
		t.Fatalf("unexpected forwarding: %d, requests=%v", resp.StatusCode, paths)
	}
	c.Revoke()
	if _, err = c.Forward(t.Context(), provider.RawRequest{Endpoint: "/responses"}); err == nil || len(paths) != 4 {
		t.Fatal("revoked account made a request")
	}
}

func TestPluginCanModifyGatewayHeaders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{
		"id":"header-fixture","name":"Header Fixture","version":"1.0.0","main":"index.js",
		"network":["https://fixture.example.org"]
	}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(`
		export function catalog() {}
		export function models() {}
		export function inference({requestHeaders: h}) {
			for (const name of ['Authorization', 'Cookie', 'X-Api-Key', 'Connection', 'Internal-Hop', 'Content-Length']) {
				if (name in h) throw new Error('reserved header reached plugin: ' + name);
			}
			const changes = {
				'anthropic-beta': null,
				'Custom-Feature': h['Custom-Feature'].map(v => 'upstream-' + v),
			};
			if (h['Test-Mode']?.[0] === 'reserved') changes['X-Api-Key'] = ['blocked'];
			if (h['Test-Mode']?.[0] === 'invalid') changes['Custom-Feature'] = ['bad\r\nvalue'];
			return {url: 'https://fixture.example.org/v1/messages', forwardHeaders: changes};
		}
	`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(p, provider.AccountInfo{ID: "account"}, fixtureCredential{})
	c.catalogURL = "https://fixture.example.org/models"
	defer c.Revoke()
	calls := 0
	c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if got := strings.Join(r.Header.Values("Custom-Feature"), ","); got != "upstream-a,upstream-b" {
			t.Errorf("custom values: %q", got)
		}
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Error("missing host authorization or version")
		}
		for _, name := range []string{"Anthropic-Beta", "X-Api-Key", "Cookie", "Internal-Hop", "Unselected-Custom"} {
			if r.Header.Get(name) != "" {
				t.Errorf("unexpected outbound header %q", name)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	raw := provider.RawRequest{Endpoint: "/v1/messages", Header: http.Header{
		"Anthropic-Beta": {"unwanted"}, "Custom-Feature": {"a", "b"}, "Unselected-Custom": {"ignored"},
		"authorization": {"local"}, "Cookie": {"session=local"}, "X-Api-Key": {"local"},
		"Connection": {"keep-alive, Internal-Hop"}, "Internal-Hop": {"local"}, "Content-Length": {"0"},
	}}
	resp, err := c.Forward(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, mode := range []string{"reserved", "invalid"} {
		raw.Header.Set("Test-Mode", mode)
		if _, err := c.Forward(t.Context(), raw); err == nil || !strings.Contains(err.Error(), "plugin forward header") {
			t.Errorf("%s override: %v", mode, err)
		}
	}
	if calls != 1 {
		t.Errorf("made %d upstream calls; want 1", calls)
	}
}

func TestCatalogRedirectMustBeExplicit(t *testing.T) {
	p, err := Load("../../examples/catalog-provider")
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.Client(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Revoke()
	calls := 0
	c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls != 1 {
			t.Fatal("redirect made a second request")
		}
		return &http.Response{Request: r, StatusCode: 302, Header: http.Header{"Location": {p.Manifest.Network[1] + "/other"}}, Body: http.NoBody}, nil
	})
	if _, err = c.Models(t.Context()); err == nil {
		t.Fatal("unexpected redirect accepted")
	}
	wrong, _ := url.Parse("https://other.example.org/models")
	if _, err = c.Do(&http.Request{URL: wrong}); err == nil {
		t.Fatal("undeclared origin accepted")
	}
}
func TestRuntimeHonorsCancellationDuringModuleLoad(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile("../../examples/catalog-provider/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "plugin.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "index.js"), []byte(`while(true){};export function catalog(){} export function models(){} export function inference(){}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.call(ctx, "catalog", nil, &requestSpec{}); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("busy module not interrupted: %v", err)
	}
}
