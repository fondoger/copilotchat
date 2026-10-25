package localproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestDesktopClassifierRouting(t *testing.T) {
	type classifierRef struct{ ProviderID, ModelID string }
	const system = `[{"type":"text","text":"prefix"},{"type":"text","text":"check safety","cache_control":{"type":"ephemeral"}}]`
	for _, endpoint := range []string{"/v1/messages", "/responses"} {
		for _, test := range []struct {
			name, key, path, profile, requested, system, stops string
			classifier                                         classifierRef
			wantAccount                                        string
			wantStatus                                         int
		}{
			{"unknown original", ClaudeDesktopKey, "/v1/messages", "test", "unknown", system, `["</severity>"]`, classifierRef{"second", "fast"}, "second", 200},
			{"override known original", ClaudeDesktopKey, "/v1/messages", "test", "primary", system, `["</severity>"]`, classifierRef{"second", "fast"}, "second", 200},
			{"ordinary key", "key", "/v1/messages", "test", "primary", system, `["</severity>"]`, classifierRef{"second", "fast"}, "first", 200},
			{"no system breakpoint", ClaudeDesktopKey, "/v1/messages", "test", "primary", `[{"type":"text","text":"check"}]`, `["</severity>"]`, classifierRef{"second", "fast"}, "first", 200},
			{"string system", ClaudeDesktopKey, "/v1/messages", "test", "primary", `"check safety"`, `["</severity>"]`, classifierRef{"second", "fast"}, "first", 200},
			{"null system", ClaudeDesktopKey, "/v1/messages", "test", "primary", `null`, `["</severity>"]`, classifierRef{"second", "fast"}, "first", 200},
			{"wrong cache type", ClaudeDesktopKey, "/v1/messages", "test", "primary", `[{"type":"text","text":"check","cache_control":{"type":"persistent"}}]`, `["</severity>"]`, classifierRef{"second", "fast"}, "first", 200},
			{"different stop", ClaudeDesktopKey, "/v1/messages", "test", "primary", system, `["severity"]`, classifierRef{"second", "fast"}, "first", 200},
			{"extra stop", ClaudeDesktopKey, "/v1/messages", "test", "primary", system, `["</severity>","other"]`, classifierRef{"second", "fast"}, "first", 200},
			{"no stop", ClaudeDesktopKey, "/v1/messages", "test", "primary", system, `null`, classifierRef{"second", "fast"}, "first", 200},
			{"unconfigured", ClaudeDesktopKey, "/v1/messages", "test", "primary", system, `["</severity>"]`, classifierRef{}, "first", 200},
			{"unconfigured unknown", ClaudeDesktopKey, "/v1/messages", "test", "unknown", system, `["</severity>"]`, classifierRef{}, "", 400},
			{"missing model", ClaudeDesktopKey, "/v1/messages", "test", "primary", system, `["</severity>"]`, classifierRef{"second", "removed"}, "", 400},
			{"missing account", ClaudeDesktopKey, "/v1/messages", "test", "primary", system, `["</severity>"]`, classifierRef{"removed", "fast"}, "", 400},
			{"disabled ordinary request", "key", "/v1/messages", "test", "second-fast", system, `["</severity>"]`, classifierRef{"second", "fast"}, "", 400},
			{"other profile", ClaudeDesktopKey, "/v1/messages", "other", "primary", system, `["</severity>"]`, classifierRef{"second", "fast"}, "first", 200},
		} {
			t.Run(endpoint+"/"+test.name, func(t *testing.T) {
				called := ""
				targets := map[string]Target{}
				for _, account := range []string{"first", "second"} {
					targets[account] = Target{Client: &rawClient{fn: func(_ context.Context, raw provider.RawRequest) (*http.Response, error) {
						called = account
						var fields map[string]json.RawMessage
						if err := json.Unmarshal(raw.Body, &fields); err != nil {
							t.Fatal(err)
						}
						wantModel := `"primary"`
						if account == "second" {
							wantModel = `"fast"`
						}
						if string(fields["model"]) != wantModel {
							t.Fatalf("wrong upstream model: %s", raw.Body)
						}
						if endpoint == "/v1/messages" {
							var stops, expectedStops []string
							_ = json.Unmarshal(fields["stop_sequences"], &stops)
							_ = json.Unmarshal([]byte(test.stops), &expectedStops)
							if string(fields["system"]) != test.system || !slices.Equal(stops, expectedStops) || string(fields["unknown"]) != `{"number":9007199254740993,"signature":"signed"}` {
								t.Fatalf("routing changed unrelated fields: %s", raw.Body)
							}
						} else if raw.Endpoint != "/responses" || !strings.Contains(string(raw.Body), `"input"`) {
							t.Fatalf("missing Responses conversion: %s", raw.Body)
						} else if strings.Contains(test.system, `"ephemeral"`) {
							converted := parsed(t, string(raw.Body))
							system := array(obj(array(converted["input"])[0])["content"])
							if len(system) != 2 || obj(obj(system[1])["prompt_cache_breakpoint"])["mode"] != "explicit" || obj(converted["prompt_cache_options"])["mode"] != "explicit" {
								t.Fatalf("classifier cache boundary lost: %s", raw.Body)
							}
						}
						result := response(`{"id":"check","type":"message","role":"assistant","content":[{"type":"text","text":"<severity>0</severity>"}],"stop_reason":"end_turn","usage":{}}`)
						if endpoint == "/responses" {
							result = response(`{"id":"check","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<severity>0</severity>"}]}],"usage":{}}`)
						}
						result.Header.Set("Content-Type", "application/json")
						return result, nil
					}}}
				}
				models := []Model{
					{ID: "primary", ProviderID: "first", Model: provider.Model{ID: "primary", Endpoint: endpoint}},
					{ID: "first-fast", ProviderID: "first", Model: provider.Model{ID: "fast", Endpoint: endpoint}},
				}
				classifier := Model{ProviderID: test.classifier.ProviderID, Model: provider.Model{ID: test.classifier.ModelID}}
				if test.classifier.ModelID == "fast" {
					classifier.Model.Endpoint = endpoint
				}
				s := New("key")
				t.Cleanup(s.Close)
				s.Update(targets, map[string]Profile{
					"test":  {ID: "test", Models: models, ClaudeClassifierModel: classifier},
					"other": {ID: "other", Models: models[:1]},
				})
				body := `{"model":"` + test.requested + `","max_tokens":100,"system":` + test.system + `,"stop_sequences":` + test.stops + `,"messages":[{"role":"user","content":"check"}],"unknown":{"number":9007199254740993,"signature":"signed"}}`
				r := httptest.NewRequest("POST", "/"+test.profile+test.path, strings.NewReader(body))
				r.Host = "127.0.0.1:39321"
				r.Header.Set("X-Api-Key", test.key)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				wantStatus, wantAccount := test.wantStatus, test.wantAccount
				if endpoint == "/responses" && test.name == "wrong cache type" {
					wantStatus, wantAccount = 400, ""
				}
				if w.Code != wantStatus || called != wantAccount {
					t.Fatalf("status=%d account=%q want %d/%q: %s", w.Code, called, wantStatus, wantAccount, w.Body.String())
				}
				if w.Code == 200 && !strings.Contains(w.Body.String(), "severity") {
					t.Fatalf("classifier output lost: %s", w.Body.String())
				}
			})
		}
	}
}

func TestDesktopClassifierDoesNotOverrideTokenCounting(t *testing.T) {
	called := false
	s := New("key")
	t.Cleanup(s.Close)
	s.Update(map[string]Target{"account": {Client: &rawClient{fn: func(_ context.Context, raw provider.RawRequest) (*http.Response, error) {
		called = true
		if raw.Endpoint != "/v1/messages/count_tokens" || !strings.Contains(string(raw.Body), `"model":"primary"`) {
			t.Fatalf("counting was rerouted: %s %s", raw.Endpoint, raw.Body)
		}
		return response(`{"input_tokens":1}`), nil
	}}}}, map[string]Profile{"test": {ID: "test", Models: []Model{{ID: "primary", ProviderID: "account", Model: provider.Model{ID: "primary", Endpoint: "/v1/messages"}}}, ClaudeClassifierModel: Model{ProviderID: "account", Model: provider.Model{ID: "missing"}}}})
	r := httptest.NewRequest("POST", "/test/v1/messages/count_tokens", strings.NewReader(`{"model":"primary","system":[{"type":"text","text":"check","cache_control":{"type":"ephemeral"}}],"stop_sequences":["</severity>"],"messages":[{"role":"user","content":"hi"}]}`))
	r.Host = "127.0.0.1:39321"
	r.Header.Set("X-Api-Key", ClaudeDesktopKey)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if !called || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
