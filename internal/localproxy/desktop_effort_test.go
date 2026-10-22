package localproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestDesktopEffortFallbackForwarding(t *testing.T) {
	for _, endpoint := range []string{"/v1/messages", "/responses"} {
		for _, test := range []struct {
			name, requested, want string
			supported             []string
		}{
			{"max to xhigh", "max", "xhigh", []string{"low", "high", "xhigh"}},
			{"max to high", "max", "high", []string{"high", "low"}},
			{"xhigh skips max", "xhigh", "high", []string{"low", "high", "max"}},
			{"xhigh to medium", "xhigh", "medium", []string{"medium", "max"}},
			{"supported max", "max", "max", []string{"high", "max"}},
			{"supported xhigh", "xhigh", "xhigh", []string{"high", "xhigh"}},
			{"no effort support", "max", "", nil},
		} {
			t.Run(endpoint+"/"+test.name, func(t *testing.T) {
				model := Model{ID: "github-copilot/test", ProviderID: "account", Model: provider.Model{
					ID: "test", Endpoint: endpoint, ThinkingEfforts: test.supported,
				}}
				called := false
				s := New("key")
				t.Cleanup(s.Close)
				s.Update(map[string]Target{"account": {Client: &rawClient{fn: func(_ context.Context, raw provider.RawRequest) (*http.Response, error) {
					called = true
					var body object
					if json.Unmarshal(raw.Body, &body) != nil {
						t.Fatal("invalid upstream body")
					}
					key := "output_config"
					if endpoint == "/responses" {
						key = "reasoning"
					}
					if got := text(obj(body[key])["effort"]); got != test.want {
						t.Fatalf("effort = %q, want %q", got, test.want)
					}
					if endpoint == "/v1/messages" && !strings.Contains(string(raw.Body), "9007199254740993") {
						t.Fatal("lost numeric precision")
					}
					res := response(`{"id":"test","status":"completed","content":[],"output":[]}`)
					res.Header.Set("Content-Type", "application/json")
					return res, nil
				}}}}, map[string]Profile{"test": {ID: "test", Models: []Model{model}}})
				body := `{"model":"` + claudeDesktopModelID(model) + `","max_tokens":100,"output_config":{"effort":"` + test.requested + `"},"messages":[{"role":"user","content":"hello"}],"metadata":{"number":9007199254740993}}`
				r := httptest.NewRequest("POST", "/test/v1/messages", strings.NewReader(body))
				r.Host = "127.0.0.1:39321"
				r.Header.Set("X-Api-Key", ClaudeDesktopKey)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if !called || w.Code != 200 {
					t.Fatal("request not forwarded", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestDesktopEffortPreservesUnrelatedFields(t *testing.T) {
	body := []byte(`{"output_config":{"effort":"xhigh","format":{"type":"json_schema"}},"reasoning":{"effort":"max","summary":"auto"},"reasoning_effort":"max","messages":[{"content":[{"signature":"signed","number":9007199254740993}]}]}`)
	encoded := normalizeDesktopEffort(body, provider.Model{ThinkingEfforts: []string{"high", "max"}})
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &fields)
	if string(fields["messages"]) != `[{"content":[{"signature":"signed","number":9007199254740993}]}]` || string(fields["reasoning_effort"]) != `"max"` {
		t.Fatal("unrelated fields changed", string(encoded))
	}
	var config object
	_ = json.Unmarshal(fields["output_config"], &config)
	if config["effort"] != "high" || obj(config["format"])["type"] != "json_schema" {
		t.Fatal("output configuration lost", config)
	}
}
