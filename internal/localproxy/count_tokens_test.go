package localproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestCountTokensForwarding(t *testing.T) {
	for _, test := range []struct {
		name       string
		endpoints  []string
		statuses   []int
		replies    []string
		paths      []string
		wantStatus int
		wantSource string
		wantTokens int
	}{
		{"native", []string{"/v1/messages"}, []int{200}, []string{`{"input_tokens":123}`}, []string{"/v1/messages/count_tokens"}, 200, "anthropic", 123},
		{"responses", []string{"/responses"}, []int{200}, []string{`{"object":"response.input_tokens","input_tokens":456}`}, []string{"/responses/input_tokens"}, 200, "responses", 456},
		{"dual uses native", []string{"/v1/messages", "/responses"}, []int{200}, []string{`{"input_tokens":321}`}, []string{"/v1/messages/count_tokens"}, 200, "anthropic", 321},
		{"native unsupported", []string{"/v1/messages"}, []int{501}, []string{`not implemented`}, []string{"/v1/messages/count_tokens"}, 501, "", 0},
		{"responses unsupported", []string{"/responses"}, []int{405}, []string{`method not allowed`}, []string{"/responses/input_tokens"}, 405, "", 0},
		{"dual unsupported", []string{"/v1/messages", "/responses"}, []int{404}, []string{`not found`}, []string{"/v1/messages/count_tokens"}, 404, "", 0},
		{"explicit unsupported", []string{"/responses"}, []int{400}, []string{`{"error":{"message":"This endpoint is not supported"}}`}, []string{"/responses/input_tokens"}, 400, "", 0},
		{"authentication", []string{"/v1/messages", "/responses"}, []int{401}, []string{`{"error":{"message":"bad key"}}`}, []string{"/v1/messages/count_tokens"}, 401, "", 0},
		{"rate limit", []string{"/responses"}, []int{429}, []string{`{"error":{"message":"slow down"}}`}, []string{"/responses/input_tokens"}, 429, "", 0},
		{"invalid tool", []string{"/v1/messages", "/responses"}, []int{400}, []string{`{"error":{"message":"count_tokens unsupported image format"}}`}, []string{"/v1/messages/count_tokens"}, 400, "", 0},
		{"invalid model", []string{"/responses"}, []int{400}, []string{`{"error":{"message":"model is not supported"}}`}, []string{"/responses/input_tokens"}, 400, "", 0},
		{"unavailable", []string{"/responses"}, []int{503}, []string{`{"error":{"message":"service unavailable"}}`}, []string{"/responses/input_tokens"}, 503, "", 0},
		{"invalid reply", []string{"/responses"}, []int{200}, []string{`{"object":"response.input_tokens"}`}, []string{"/responses/input_tokens"}, 502, "", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []provider.RawRequest
			client := &rawClient{fn: func(_ context.Context, raw provider.RawRequest) (*http.Response, error) {
				i := len(calls)
				calls = append(calls, raw)
				if i >= len(test.statuses) {
					t.Fatal("unexpected additional upstream request", raw.Endpoint)
				}
				return &http.Response{StatusCode: test.statuses[i], Header: http.Header{"Retry-After": {"7"}, "Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader(test.replies[i]))}, nil
			}}
			s := New("key")
			t.Cleanup(s.Close)
			model := Model{ProviderID: "account", ID: "provider/original-model", Model: provider.Model{ID: "original-model", Endpoints: test.endpoints}}
			s.Update(map[string]Target{"account": {Client: client}}, map[string]Profile{"test": {ID: "test-profile", Models: []Model{model}}})
			body := `{"model":"` + claudeDesktopModelID(model) + `","system":"system instructions","messages":[{"role":"user","content":"foo 中文"}],"tools":[{"name":"Skill","description":"Invoke a skill","input_schema":{"type":"object","properties":{"skill":{"type":"string"}},"const":9007199254740993}}]}`
			r := httptest.NewRequest("POST", "/test/v1/messages/count_tokens?beta=true", strings.NewReader(body))
			r.Host = "127.0.0.1:39321"
			r.Header.Set("X-Api-Key", ClaudeDesktopKey)
			r.Header.Set("X-ConversationId", "session-1")
			r.Header.Set("Anthropic-Beta", "example-beta")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != test.wantStatus || w.Header().Get("X-CopilotChat-Token-Count-Source") != test.wantSource || len(calls) != len(test.paths) {
				t.Fatal(w.Code, w.Body.String(), w.Header(), len(calls))
			}
			if test.wantStatus == 200 {
				var result struct {
					InputTokens int `json:"input_tokens"`
				}
				if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.InputTokens <= 0 || (test.wantTokens != 0 && result.InputTokens != test.wantTokens) || strings.Contains(w.Body.String(), `"object"`) {
					t.Fatal("invalid Anthropic count response", w.Body.String())
				}
			} else if test.wantStatus != 502 && w.Header().Get("Retry-After") != "7" {
				t.Fatal("upstream retry hint lost")
			}
			if test.wantStatus != 200 && test.wantStatus != 502 && (w.Body.String() != test.replies[len(test.replies)-1] || w.Header().Get("Content-Type") != "text/plain") {
				t.Fatal("upstream error response changed", w.Body.String(), w.Header())
			}
			for i, call := range calls {
				if call.Endpoint != test.paths[i] || call.ConversationID == "" || call.ConversationID != calls[0].ConversationID {
					t.Fatal("count routed to wrong endpoint/session", call)
				}
				fields, err := decodeObject(call.Body)
				if err != nil || text(fields["model"]) != "original-model" || !strings.Contains(string(call.Body), "9007199254740993") {
					t.Fatal("model or tool schema not preserved", string(call.Body))
				}
				if call.Endpoint == "/responses/input_tokens" {
					for _, field := range []string{"messages", "stream", "store", "include", "max_output_tokens", "temperature"} {
						if _, exists := fields[field]; exists {
							t.Fatal("generation-only field in count request", field)
						}
					}
					if text(fields["instructions"]) != "system instructions" || len(array(fields["input"])) != 1 || text(obj(array(fields["tools"])[0])["type"]) != "function" || call.Header.Get("Anthropic-Beta") != "" || call.RawQuery != "" {
						t.Fatal("incorrect Responses count translation", string(call.Body))
					}
				} else if call.RawQuery != "beta=true" || call.Header.Get("Anthropic-Beta") != "example-beta" {
					t.Fatal("native protocol headers lost")
				}
			}
		})
	}
}

func TestCountTokensTransportFailureIsNotEstimated(t *testing.T) {
	client := &rawClient{fn: func(_ context.Context, _ provider.RawRequest) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	writeCountTokens(w, r, t.Context(), client, provider.Model{Endpoint: "/responses"}, provider.RawRequest{Body: []byte(`{"model":"test","messages":[{"role":"user","content":"foo"}]}`)})
	if w.Code != 502 || w.Header().Get("X-CopilotChat-Token-Count-Source") != "" {
		t.Fatal("transport error concealed", w.Code, w.Body.String())
	}
}
