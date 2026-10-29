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

func TestClassifierPrivateRouteAndCatalogIsolation(t *testing.T) {
	for _, endpoint := range []string{"/v1/messages", "/responses"} {
		t.Run(endpoint, func(t *testing.T) {
			classifier := Model{ID: "provider/hidden", ProviderID: "selected", Model: provider.Model{ID: "hidden", Endpoint: endpoint, Endpoints: []string{endpoint}}}
			other := classifier
			other.ProviderID = "other"
			if ClassifierModelID(other) == ClassifierModelID(classifier) {
				t.Fatal("classifier accounts share an alias")
			}
			called := 0
			targets := map[string]Target{
				"selected": {Client: &rawClient{fn: func(_ context.Context, raw provider.RawRequest) (*http.Response, error) {
					called++
					got := parsed(t, string(raw.Body))
					if got["model"] != "hidden" || raw.Endpoint != endpoint {
						t.Fatal("incorrect classifier target", raw.Endpoint, string(raw.Body))
					}
					reply := `{"type":"message","role":"assistant","content":[{"type":"text","text":"safe"}],"stop_reason":"end_turn","usage":{}}`
					if endpoint == "/responses" {
						reply = `{"id":"check","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"safe"}]}],"usage":{}}`
					}
					response := response(reply)
					response.Header.Set("Content-Type", "application/json")
					return response, nil
				}}},
				"other": {Client: &rawClient{fn: func(context.Context, provider.RawRequest) (*http.Response, error) {
					t.Fatal("classifier crossed accounts")
					return nil, nil
				}}},
			}
			s := New("key")
			t.Cleanup(s.Close)
			profiles := map[string]Profile{
				"test":  {ID: "test", ClaudeDefaults: map[string]Model{claudeSonnetDefault: classifier}},
				"other": {ID: "other", ClaudeDefaults: map[string]Model{claudeSonnetDefault: other}},
			}
			s.Update(targets, profiles)
			// Update owns snapshots, including a classifier absent from ordinary Models.
			classifier.Model.Endpoints[0] = "/unavailable"
			request := func(key, path, model string) *httptest.ResponseRecorder {
				t.Helper()
				body, _ := json.Marshal(object{"model": model, "max_tokens": 100, "messages": []any{object{"role": "user", "content": "check safety"}}})
				r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
				r.Host = "127.0.0.1:39321"
				r.Header.Set("X-Api-Key", key)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				return w
			}
			alias := ClassifierModelID(classifier)
			if w := request("key", "/test/v1/messages", alias+"[1m]"); w.Code != 200 || called != 1 {
				t.Fatal("private classifier route failed", w.Code, w.Body.String())
			}
			for _, test := range []struct {
				key, path, model string
				status           int
			}{
				{"key", "/test/v1/messages", "provider/hidden", 400},
				{"key", "/test/v1/messages", "hidden", 400},
				{"key", "/other/v1/messages", alias, 400},
				{"key", "/test/v1/messages/count_tokens", alias, 400},
				{"key", "/test/responses", alias, 400},
				{"wrong-key", "/test/v1/messages", alias, 401},
			} {
				if w := request(test.key, test.path, test.model); w.Code != test.status {
					t.Fatal("classifier route leaked", test, w.Code, w.Body.String())
				}
			}
			for _, key := range []string{"key", ClaudeDesktopKey} {
				for _, path := range []string{"/test/models", "/test/v1/models", "/test/codex/models"} {
					r := httptest.NewRequest("GET", path, nil)
					r.Host = "127.0.0.1:39321"
					r.Header.Set("X-Api-Key", key)
					w := httptest.NewRecorder()
					s.ServeHTTP(w, r)
					if w.Code != 200 || strings.Contains(w.Body.String(), "hidden") || strings.Contains(w.Body.String(), "classifier-") {
						t.Fatal("classifier exposed in catalog", path, w.Code, w.Body.String())
					}
				}
			}
			// A stale alias must fail after the account is removed, never fall back.
			delete(targets, "selected")
			s.Update(targets, profiles)
			if w := request("key", "/test/v1/messages", alias); w.Code != 400 {
				t.Fatal("removed classifier account still reachable", w.Code, w.Body.String())
			}
			if called != 1 {
				t.Fatal("ordinary requests reached classifier", called)
			}
		})
	}
}
