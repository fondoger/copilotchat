package localproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestAnthropicModelsMetadata(t *testing.T) {
	m := Model{ID: "m365-copilot/claude-sonnet-fixture", ProviderName: "Work", Model: provider.Model{
		ID: "claude-sonnet-fixture", Name: "Claude Fixture", Endpoint: "/v1/messages",
		ContextWindow: 1000000, MaxOutputTokens: 64000, CreatedAt: "2026-10-01T02:00:00+02:00",
		Lifecycle: "deprecated", DeprecatedAt: "2026-10-20T00:00:00Z", RetiresAt: "2027-01-01T00:00:00Z", Line: "sonnet",
		Capabilities: json.RawMessage(`{"batch":{"supported":true},"image_input":{"supported":true},"effort":{"supported":true,"xhigh":null},"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}},"server_tools":{"supported":true,"web_search":{"supported":true}}}`),
	}}
	row := anthropicModel(m)
	if row["id"] != m.ID || row["type"] != "model" || row["display_name"] != "Claude Fixture (Work)" || row["created_at"] != "2026-10-01T00:00:00Z" || row["max_input_tokens"] != 1000000 || row["max_tokens"] != 64000 || row["line"] != "sonnet" || row["lifecycle"] != "deprecated" || row["deprecated_at"] != m.Model.DeprecatedAt || row["retires_at"] != m.Model.RetiresAt {
		t.Fatal("native metadata changed", row)
	}
	caps := obj(row["capabilities"])
	if len(caps) != 10 || obj(caps["batch"])["supported"] != false || obj(caps["image_input"])["supported"] != true || obj(caps["effort"])["xhigh"] != nil || obj(obj(caps["server_tools"])["web_search"])["supported"] != true || obj(obj(obj(caps["thinking"])["types"])["adaptive"])["supported"] != true {
		t.Fatal("native capabilities lost or unsupported Batch API advertised", caps)
	}
	if _, ok := row["object"]; ok {
		t.Fatal("OpenAI fields leaked into Anthropic model", row)
	}
	unknown := anthropicModel(Model{ID: "github-copilot/unknown", Model: provider.Model{ID: "unknown"}})
	for _, field := range []string{"max_input_tokens", "max_tokens", "line", "deprecated_at", "retires_at"} {
		if value, present := unknown[field]; !present || value != nil {
			t.Fatal("unknown metadata must be explicitly null", field, value)
		}
	}
	if unknown["created_at"] != "1970-01-01T00:00:00Z" || unknown["lifecycle"] != "active" {
		t.Fatal("invalid fallback metadata", unknown)
	}
	// Responses conversion exposes only the Anthropic features it implements.
	m.Model.Endpoint = "/responses"
	m.Model.Vision, m.Model.PDF, m.Model.StructuredOutputs, m.Model.Reasoning = true, true, true, true
	m.Model.ThinkingEfforts = []string{"low", "high"}
	caps = anthropicCapabilities(m)
	for _, key := range []string{"batch", "citations", "code_execution", "context_management", "server_tools"} {
		if obj(caps[key])["supported"] != false {
			t.Fatal("unimplemented API advertised", key, caps[key])
		}
	}
	if obj(caps["pdf_input"])["supported"] != true || obj(caps["structured_outputs"])["supported"] != true || obj(obj(caps["effort"])["high"])["supported"] != true {
		t.Fatal("converted capabilities lost", caps)
	}
}

func TestAnthropicModelLine(t *testing.T) {
	for id, want := range map[string]any{
		"claude-haiku-4.5": "haiku", "claude-sonnet-4.6": "sonnet", "claude-opus-4.8": "opus",
		"claude-fable-5": "fable", "CLAUDE-MYTHOS-5": "mythos", "gpt-5.5": nil, "opus-4.8": nil,
		"claude-sonnet": nil, "claude-sonnetish-5": nil,
	} {
		model := Model{ID: "github-copilot/" + id, Model: provider.Model{ID: id, Line: "upstream-other-line"}}
		if got := anthropicModel(model)["line"]; got != want {
			t.Errorf("%s: line = %v, want %v", id, got, want)
		}
	}
}

func TestAnthropicModelsPagination(t *testing.T) {
	models := []Model{}
	for _, item := range []struct{ id, date, lifecycle, endpoint string }{
		{"old", "", "", "/responses"},
		{"new", "2026-10-21T00:00:00Z", "active", "/v1/messages"},
		{"retired", "2026-10-20T00:00:00Z", "retired", "/v1/messages"},
		{"deprecated", "2026-10-19T00:00:00Z", "deprecated", "/responses"},
		{"chat-only", "2026-10-22T00:00:00Z", "active", "/chat/completions"},
	} {
		models = append(models, Model{ID: item.id, Model: provider.Model{ID: item.id, CreatedAt: item.date, Lifecycle: item.lifecycle, Endpoint: item.endpoint}})
	}
	for _, test := range []struct {
		query string
		ids   []string
		more  bool
	}{
		{"", []string{"new", "deprecated", "old"}, false},
		{"?limit=1", []string{"new"}, true},
		{"?limit=1&after_id=new", []string{"deprecated"}, true},
		{"?limit=1&after_id=deprecated", []string{"old"}, false},
		{"?limit=1&before_id=old", []string{"deprecated"}, true},
		{"?limit=1&before_id=deprecated", []string{"new"}, false},
		{"?before_id=new", []string{}, false},
		{"?after_id=old", []string{}, false},
		{"?lifecycle=retired", []string{"retired"}, false},
		{"?lifecycle[]=active&lifecycle[]=retired", []string{"new", "retired", "old"}, false},
	} {
		t.Run(test.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeAnthropicModels(w, httptest.NewRequest("GET", "/v1/models"+test.query, nil), models, false)
			var page struct {
				Data    []struct{ ID string } `json:"data"`
				HasMore bool                  `json:"has_more"`
				FirstID *string               `json:"first_id"`
				LastID  *string               `json:"last_id"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Data == nil || page.HasMore != test.more {
				t.Fatal(w.Code, w.Body.String())
			}
			ids := []string{}
			for _, model := range page.Data {
				ids = append(ids, model.ID)
			}
			if !reflect.DeepEqual(ids, test.ids) {
				t.Fatal(ids, test.ids)
			}
			if len(ids) == 0 {
				if page.FirstID != nil || page.LastID != nil {
					t.Fatal("empty cursors", w.Body.String())
				}
			} else if page.FirstID == nil || page.LastID == nil || *page.FirstID != ids[0] || *page.LastID != ids[len(ids)-1] {
				t.Fatal("invalid cursors", w.Body.String())
			}
		})
	}
	for _, query := range []string{"?limit=0", "?limit=1001", "?limit=no", "?limit=1&limit=2", "?after_id=new&before_id=old", "?after_id=missing", "?lifecycle=invalid", "?lifecycle=active&lifecycle=active&lifecycle=active&lifecycle=active"} {
		w := httptest.NewRecorder()
		writeAnthropicModels(w, httptest.NewRequest("GET", "/v1/models"+query, nil), models, false)
		if w.Code != 400 {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	// Public release-date ordering must not reorder the provider routing snapshot.
	if models[0].ID != "old" {
		t.Fatal("catalog mutated routing order")
	}
	w := httptest.NewRecorder()
	writeAnthropicModels(w, httptest.NewRequest("GET", "/v1/models", nil), nil, false)
	var empty struct {
		Data    []object `json:"data"`
		FirstID any      `json:"first_id"`
		LastID  any      `json:"last_id"`
		HasMore bool     `json:"has_more"`
	}
	if json.Unmarshal(w.Body.Bytes(), &empty) != nil || empty.Data == nil || len(empty.Data) != 0 || empty.HasMore || empty.FirstID != nil || empty.LastID != nil {
		t.Fatal("empty list shape", w.Body.String())
	}
}

func TestClaudeDesktopModelsAndRouting(t *testing.T) {
	calls := make(chan provider.RawRequest, 8)
	client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
		calls <- r
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_fixture","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
	}}
	s := New("fixture-key")
	t.Cleanup(s.Close)
	base := "http://127.0.0.1:39321/fixture"
	// Exercise the complete handler without depending on loopback firewall rules.
	send := func(t *testing.T, method, url, body string, headers map[string]string) *http.Response {
		t.Helper()
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r.URL.Scheme, r.URL.Host = "", "" // Incoming server requests use origin-form URLs.
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Result()
	}
	models := []Model{
		{ProviderID: "p1", ID: "github-copilot/claude-fixture", Model: provider.Model{ID: "claude-fixture", Endpoint: "/v1/messages"}},
		{ProviderID: "p1", ID: "github-copilot/grok-fixture", Routes: []string{"github-copilot/user/grok-fixture"}, Model: provider.Model{ID: "grok-fixture", Endpoint: "/responses", ContextWindow: 256000, Reasoning: true, ThinkingEfforts: []string{"low", "high"}}},
	}
	s.Update(map[string]Target{"p1": {Client: client}}, map[string]Profile{"fixture": {ID: "fixture-id", Models: models}})
	for _, key := range []string{ClaudeDesktopKey, "fixture-key"} {
		header := "X-Api-Key"
		for _, path := range []string{"/v1/models", "/models"} {
			res := send(t, "GET", base+path, "", map[string]string{header: key})
			var catalog struct{ Data []struct{ ID string } }
			if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&catalog) != nil || len(catalog.Data) != 2 {
				t.Fatal(res.Status, catalog)
			}
			wantClaude, want := models[0].ID, models[1].ID
			if key == ClaudeDesktopKey {
				wantClaude, want = claudeDesktopModelID(models[0]), claudeDesktopModelID(models[1])
			}
			if catalog.Data[0].ID != wantClaude || catalog.Data[1].ID != want || strings.Contains(catalog.Data[1].ID, "[1m]") {
				t.Fatal("incorrect client-specific IDs", catalog)
			}
		}
	}
	var conversation string
	for _, test := range []struct{ id, endpoint, upstream string }{
		{claudeDesktopModelID(models[0]), "/v1/messages", "claude-fixture"},
		{claudeDesktopModelID(models[1]), "/v1/messages", "grok-fixture"},
		{claudeDesktopModelID(models[1]), "/v1/responses", "grok-fixture"},
		{"claude-desktop/github-copilot/grok-fixture", "/v1/messages", "grok-fixture"},
		{"claude-desktop/github-copilot/user/grok-fixture", "/v1/messages", "grok-fixture"},
		{"github-copilot/grok-fixture[1m]", "/v1/messages", "grok-fixture"},
		{"github-copilot/claude-fixture", "/v1/messages", "claude-fixture"},
		{"claude-desktop/github-copilot/grok-fixture", "/v1/responses", "grok-fixture"},
	} {
		res := send(t, "POST", base+test.endpoint, `{"model":"`+test.id+`","max_tokens":200,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hello"}]}`, map[string]string{"Authorization": "Bearer " + ClaudeDesktopKey, "X-ConversationId": "desktop-session"})
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatal(test.id, res.Status, string(body))
		}
		got := <-calls
		var fields struct{ Model string }
		if json.Unmarshal(got.Body, &fields) != nil || fields.Model != test.upstream || got.ConversationID == "" {
			t.Fatal("desktop ID forwarded upstream", string(got.Body))
		}
		if conversation != "" && got.ConversationID != conversation {
			t.Fatal("desktop alias changed session affinity")
		}
		conversation = got.ConversationID
		if test.upstream == "grok-fixture" && got.Endpoint != "/responses" {
			t.Fatal("missing protocol conversion", got.Endpoint)
		}
		if test.upstream == "grok-fixture" && test.endpoint == "/v1/messages" {
			fields, err := decodeObject(got.Body)
			if err != nil || text(obj(fields["reasoning"])["effort"]) != "high" {
				t.Fatal("Desktop effort was not forwarded", string(got.Body))
			}
		}
	}
	if models[1].ID != "github-copilot/grok-fixture" {
		t.Fatal("desktop catalog mutated shared IDs")
	}
	if send(t, "GET", base+"/v1/models", "", map[string]string{"X-Api-Key": "invalid"}).StatusCode != 401 {
		t.Fatal("invalid key accepted")
	}
}
