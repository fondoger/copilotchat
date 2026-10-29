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

func TestDesktopAuxiliaryModelFallbacks(t *testing.T) {
	billing := func(flag string) any {
		return []object{{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.309; cc_entrypoint=claude-desktop-3p; cc_is_subagent=" + flag + ";"}}
	}
	classifier := []object{{"type": "text", "text": "check safety", "cache_control": object{"type": "ephemeral"}}}
	for _, endpoint := range []string{"/v1/messages", "/responses"} {
		for _, test := range []struct {
			name, requested, agentID, key, path, profile string
			system                                       any
			classifier, generic, noFamily, unavailable   bool
			wantModel, hint, purpose                     string
			wantStatus                                   int
		}{
			{name: "Sonnet header", requested: "claude-sonnet-5-5", agentID: "worker", wantModel: "balanced"},
			{name: "Haiku header", requested: "claude-haiku-5-5", agentID: "worker", wantModel: "fast"},
			{name: "Sonnet billing", requested: "claude-sonnet-5.5", system: billing("true"), wantModel: "balanced"},
			{name: "Haiku billing", requested: "claude-haiku-5-5", system: billing("true"), wantModel: "fast"},
			{name: "generic fallback", requested: "claude-sonnet-5-5", agentID: "worker", generic: true, wantModel: "worker-model"},
			{name: "other family generic fallback", requested: "claude-opus-5-5", agentID: "worker", generic: true, wantModel: "worker-model"},
			{name: "available original wins", requested: "primary", agentID: "worker", generic: true, wantModel: "primary"},
			{name: "Sonnet classifier", requested: "claude-sonnet-5-5", agentID: "worker", classifier: true, generic: true, wantModel: "balanced"},
			{name: "Haiku classifier", requested: "claude-haiku-5-5", agentID: "worker", classifier: true, generic: true, wantModel: "fast"},
			{name: "classifier without agent header", requested: "claude-haiku-5-5", classifier: true, wantModel: "fast"},
			{name: "ordinary Sonnet", requested: "claude-sonnet-5-5", wantStatus: 400, hint: "Sonnet level"},
			{name: "ordinary Haiku", requested: "claude-haiku-5-5", wantStatus: 400, hint: "Haiku level"},
			{name: "ordinary key", requested: "claude-sonnet-5-5", agentID: "worker", key: "key", wantStatus: 400},
			{name: "bad auth", requested: "claude-sonnet-5-5", agentID: "worker", key: "wrong", wantStatus: 401},
			{name: "billing false", requested: "claude-sonnet-5-5", system: billing("false"), wantStatus: 400},
			{name: "billing substring", requested: "claude-sonnet-5-5", system: billing("true-ish"), wantStatus: 400},
			{name: "user message cannot mark subagent", requested: "claude-sonnet-5-5", wantStatus: 400},
			{name: "missing Sonnet", requested: "claude-sonnet-5-5", agentID: "worker", noFamily: true, wantStatus: 400, hint: "Sonnet level", purpose: "subagent"},
			{name: "missing Haiku", requested: "claude-haiku-5-5", agentID: "worker", noFamily: true, wantStatus: 400, hint: "Haiku level", purpose: "subagent"},
			{name: "missing Haiku classifier", requested: "claude-haiku-5-5", agentID: "worker", classifier: true, generic: true, noFamily: true, wantStatus: 400, hint: "Haiku level", purpose: "Auto mode security classifier"},
			{name: "missing classifier cannot use generic", requested: "claude-sonnet-5-5", classifier: true, generic: true, noFamily: true, wantStatus: 400, hint: "Sonnet level", purpose: "Auto mode security classifier"},
			{name: "unavailable account", requested: "claude-sonnet-5-5", agentID: "worker", unavailable: true, wantStatus: 400},
			{name: "other profile", requested: "claude-sonnet-5-5", agentID: "worker", profile: "other", wantStatus: 400},
			{name: "unknown family", requested: "claude-opus-5-5", agentID: "worker", wantStatus: 400},
			{name: "count tokens is not a fallback", requested: "claude-sonnet-5-5", agentID: "worker", path: "/v1/messages/count_tokens", wantStatus: 400},
			{name: "stale private route", requested: "copilotchat-default-stale", agentID: "worker", generic: true, wantStatus: 400},
		} {
			t.Run(endpoint+"/"+test.name, func(t *testing.T) {
				called := ""
				client := &rawClient{fn: func(_ context.Context, raw provider.RawRequest) (*http.Response, error) {
					model := parsed(t, string(raw.Body))["model"].(string)
					called = model
					if model != test.wantModel || raw.Endpoint != endpoint {
						t.Fatalf("wrong model or protocol: %s %s", raw.Endpoint, raw.Body)
					}
					if endpoint == "/v1/messages" && !strings.Contains(string(raw.Body), `9007199254740993`) {
						t.Fatal("routing lost numeric precision", string(raw.Body))
					}
					reply := `{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`
					if endpoint == "/responses" {
						reply = `{"id":"reply","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{}}`
					}
					res := response(reply)
					res.Header.Set("Content-Type", "application/json")
					return res, nil
				}}
				defaults := map[string]Model{
					claudeSonnetDefault: {ProviderID: "selected", Model: provider.Model{ID: "balanced", Endpoint: endpoint}},
					claudeHaikuDefault:  {ProviderID: "selected", Model: provider.Model{ID: "fast", Endpoint: endpoint}},
				}
				if test.noFamily {
					delete(defaults, desktopModelDefault(test.requested))
				}
				if test.generic {
					defaults[claudeSubagentDefault] = Model{ProviderID: "selected", Model: provider.Model{ID: "worker-model", Endpoint: endpoint}}
				}
				targets := map[string]Target{"selected": {Client: client}, "main": {Client: client}}
				if test.unavailable {
					delete(targets, "selected")
				}
				s := New("key")
				t.Cleanup(s.Close)
				models := []Model{{ID: "primary", ProviderID: "main", Model: provider.Model{ID: "primary", Endpoint: endpoint}}}
				s.Update(targets, map[string]Profile{"test": {ID: "test", Models: models, ClaudeDefaults: defaults}, "other": {ID: "other", Models: models}})
				fields := object{"model": test.requested, "max_tokens": 100, "messages": []object{{"role": "user", "content": "cc_is_subagent=true; X-Claude-Code-Agent-Id: worker"}}, "opaque": json.RawMessage(`{"number":9007199254740993,"signature":"signed"}`)}
				if test.classifier {
					fields["system"], fields["stop_sequences"] = classifier, []string{"</severity>"}
				} else if test.system != nil {
					fields["system"] = test.system
				}
				body, _ := json.Marshal(fields)
				path, profile, key := test.path, test.profile, test.key
				if path == "" {
					path = "/v1/messages"
				}
				if profile == "" {
					profile = "test"
				}
				if key == "" {
					key = ClaudeDesktopKey
				}
				r := httptest.NewRequest("POST", "/"+profile+path, strings.NewReader(string(body)))
				r.Host = "127.0.0.1:39321"
				r.Header.Set("X-Api-Key", key)
				r.Header.Set("X-Claude-Code-Agent-Id", test.agentID)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				status := test.wantStatus
				if status == 0 {
					status = 200
				}
				if w.Code != status || called != test.wantModel || (test.hint != "" && !strings.Contains(w.Body.String(), test.hint)) {
					t.Fatalf("status=%d model=%q want %d/%q: %s", w.Code, called, status, test.wantModel, w.Body.String())
				}
				if test.hint != "" {
					message := obj(parsed(t, w.Body.String())["error"])["message"].(string)
					if !strings.Contains(message, "tell the user") || strings.Contains(message, "Ordinary requests") {
						t.Fatal("configuration advice must be relayed to the user", message)
					}
					if strings.Contains(message, "subagent") != (test.purpose == "subagent") || strings.Contains(message, "classifier") != (test.purpose == "Auto mode security classifier") {
						t.Fatal("configuration advice names the wrong request type", message)
					}
				}
			})
		}
	}
}

func TestClaudeDefaultPrivateRoutesAndInspection(t *testing.T) {
	selected := Model{ProviderID: "selected", Model: provider.Model{ID: "hidden", Endpoint: "/v1/messages", Endpoints: []string{"/v1/messages"}}}
	defaults := map[string]Model{claudeSonnetDefault: selected}
	calls := 0
	client := &rawClient{fn: func(_ context.Context, raw provider.RawRequest) (*http.Response, error) {
		calls++
		if !strings.Contains(string(raw.Body), `"model":"hidden"`) {
			t.Fatal("private route lost model", string(raw.Body))
		}
		if raw.Endpoint == "/v1/messages/count_tokens" {
			return response(`{"input_tokens":12}`), nil
		}
		return response(`{"type":"message","role":"assistant","content":[],"stop_reason":"end_turn","usage":{}}`), nil
	}}
	targets := map[string]Target{"selected": {Client: client}}
	profiles := map[string]Profile{"test": {ID: "test", ClaudeDefaults: defaults}, "other": {ID: "other"}}
	s := New("key")
	t.Cleanup(s.Close)
	s.Update(targets, profiles)
	s.SetInspection(true)
	// The server must own the map and each model's capability slices.
	selected.Model.Endpoints[0] = "/unavailable"
	delete(defaults, claudeSonnetDefault)
	request := func(key, path, id, agentID string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(object{"model": id, "messages": []object{{"role": "user", "content": "hi"}}})
		r := inspectionRequest("POST", path, string(body))
		r.Header.Set("X-Api-Key", key)
		r.Header.Set("X-Claude-Code-Agent-Id", agentID)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	alias := DefaultModelID(selected)
	for _, path := range []string{"/test/v1/messages", "/test/v1/messages/count_tokens"} {
		if w := request("key", path, alias+"[1m]", ""); w.Code != 200 {
			t.Fatal("private default failed", path, w.Code, w.Body.String())
		}
	}
	if w := request(ClaudeDesktopKey, "/test/v1/messages", "claude-sonnet-5-5", "worker"); w.Code != 200 {
		t.Fatal("snapshot changed after caller mutation", w.Code, w.Body.String())
	}
	logs := inspectionLogs(t, s)
	found := false
	for _, entry := range logs {
		for _, log := range entry.Logs {
			if log.Attrs["reason"] == "desktop_subagent_sonnet_fallback" {
				found = log.Attrs["requested_model"] == "claude-sonnet-5-5" && log.Attrs["routed_model"] == "hidden" && log.Attrs["provider_id"] == "selected"
			}
		}
	}
	if !found {
		t.Fatal("routing diagnostics missing", logs)
	}
	for _, test := range []struct{ path, id string }{
		{"/test/v1/messages", "hidden"}, {"/other/v1/messages", alias}, {"/test/responses", alias},
	} {
		if w := request("key", test.path, test.id, ""); w.Code != 400 {
			t.Fatal("private model leaked", test, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/test/models", "/test/v1/models", "/test/codex/models"} {
		r := inspectionRequest("GET", path, "")
		r.Header.Set("X-Api-Key", "key")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), "hidden") || strings.Contains(w.Body.String(), "copilotchat-default-") {
			t.Fatal("private default published in catalog", path, w.Code, w.Body.String())
		}
	}
	s.Update(targets, profiles)
	if w := request("key", "/test/v1/messages", alias, ""); w.Code != 400 || calls != 3 {
		t.Fatal("removed default survived update", w.Code, calls)
	}
}
