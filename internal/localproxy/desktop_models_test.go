package localproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestClaudeDesktopModelNames(t *testing.T) {
	for _, test := range []struct {
		model        provider.Model
		prefix, line string
	}{
		{provider.Model{ID: "claude-opus-4.6"}, "claude-opus-4-6", "opus"},
		{provider.Model{ID: "claude-sonnet-4.7"}, "claude-sonnet-4-7", "sonnet"},
		{provider.Model{ID: "gpt-6-astra", ThinkingEfforts: []string{"xhigh", "max"}, ContextWindow: 1050000}, "mythos-", "mythos"},
		{provider.Model{ID: "grok-4.7", ThinkingEfforts: []string{"xhigh"}, ContextWindow: 500000}, "mythos-", "mythos"},
		{provider.Model{ID: "fast-no-thinking", ContextWindow: 128000}, "mythos-", "mythos"},
	} {
		t.Run(test.model.ID, func(t *testing.T) {
			model := Model{ProviderID: "account-a", ID: "github-copilot/" + test.model.ID, Model: test.model}
			id := claudeDesktopModelID(model)
			opaque := !strings.Contains(test.model.ID, "claude")
			if !strings.HasPrefix(id, test.prefix) || (opaque && !regexp.MustCompile(`^[0-9]+$`).MatchString(strings.TrimPrefix(id, test.prefix))) || (!opaque && id != test.prefix) || claudeDesktopModelLine(test.model) != test.line {
				t.Fatal("invalid Desktop model name", id)
			}
			// Public names and account counts may change; the provider binding must not.
			model.ID = "github-copilot/user/" + test.model.ID
			model.ProviderName = "Renamed provider"
			if claudeDesktopModelID(model) != id {
				t.Fatal("public route changed Desktop ID")
			}
			model.ProviderID = "account-b"
			if opaque && claudeDesktopModelID(model) == id {
				t.Fatal("accounts share a Desktop ID")
			}
		})
	}
}

func TestClaudeDesktopCatalogPreservesCapabilities(t *testing.T) {
	models := []Model{{ProviderID: "account", ID: "github-copilot/gpt-fixture", ProviderName: "Work",
		Model: provider.Model{ID: "gpt-fixture", Name: "GPT Fixture", Endpoint: "/responses", ContextWindow: 400000, ThinkingEfforts: []string{"low", "high", "xhigh"}}}}
	w := httptest.NewRecorder()
	writeAnthropicModels(w, httptest.NewRequest("GET", "/v1/models", nil), models, true)
	var page struct {
		Data []object `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Data) != 1 {
		t.Fatal(w.Body.String(), err)
	}
	row := page.Data[0]
	if row["id"] != claudeDesktopModelID(models[0]) || row["line"] != "mythos" || row["display_name"] != "GPT Fixture (Work)" || row["max_input_tokens"] != float64(400000) {
		t.Fatal("incorrect Desktop metadata", row)
	}
	effort := obj(obj(row["capabilities"])["effort"])
	if obj(effort["xhigh"])["supported"] != true || obj(effort["max"])["supported"] != false {
		t.Fatal("Desktop naming changed actual effort capabilities", effort)
	}
	if clientModelIDs(models, false)[0].ID != models[0].ID || models[0].ID != "github-copilot/gpt-fixture" {
		t.Fatal("Desktop names leaked into other clients")
	}
}

func TestClaudeDesktopDuplicateClaudeIDsAndRouting(t *testing.T) {
	models := []Model{
		{ProviderID: "first", ID: "github-copilot/claude-opus-4.6", Model: provider.Model{ID: "claude-opus-4.6", Endpoint: "/v1/messages", CreatedAt: "2025-01-01T00:00:00Z"}},
		{ProviderID: "second", ID: "m365-copilot/claude-opus-4.6", Model: provider.Model{ID: "claude-opus-4.6", Endpoint: "/v1/messages", CreatedAt: "2026-01-01T00:00:00Z"}},
		{ProviderID: "third", ID: "github-copilot/claude-opus-4-6", Model: provider.Model{ID: "claude-opus-4-6", Endpoint: "/v1/messages"}},
	}
	ids := clientModelIDs(models, true)
	if ids[0].ID != "claude-opus-4-6" || ids[1].ID != claudeDesktopOpaqueID(models[1]) || ids[2].ID != claudeDesktopOpaqueID(models[2]) || ids[1].ID == ids[2].ID {
		t.Fatal("duplicate or normalized Claude IDs are not unique", ids)
	}
	var routed string
	clients := map[string]Target{}
	for _, model := range models {
		account := model.ProviderID
		clients[account] = Target{Client: &rawClient{fn: func(_ context.Context, raw provider.RawRequest) (*http.Response, error) {
			routed = account
			var fields struct{ Model string }
			if json.Unmarshal(raw.Body, &fields) != nil || fields.Model != models[map[string]int{"first": 0, "second": 1, "third": 2}[account]].Model.ID {
				t.Fatal("upstream model ID changed", string(raw.Body))
			}
			return response(`{"input_tokens":1}`), nil
		}}}
	}
	s := New("key")
	t.Cleanup(s.Close)
	s.Update(clients, map[string]Profile{"test": {ID: "test", Models: models}})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "127.0.0.1:39321"
		r.Header.Set("X-Api-Key", ClaudeDesktopKey)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	catalog := call("GET", "/test/v1/models?limit=1000", "")
	var page struct{ Data []object }
	if catalog.Code != 200 || json.Unmarshal(catalog.Body.Bytes(), &page) != nil || len(page.Data) != 3 || page.Data[0]["id"] != ids[1].ID || page.Data[0]["line"] != "mythos" || page.Data[1]["id"] != "claude-opus-4-6" || page.Data[1]["line"] != "opus" {
		t.Fatal("catalog sorting changed first-account naming", catalog.Body.String())
	}
	for i, model := range ids {
		w := call("POST", "/test/v1/messages/count_tokens", `{"model":"`+model.ID+`","messages":[{"role":"user","content":"foo"}]}`)
		if w.Code != 200 || routed != models[i].ProviderID {
			t.Fatal("Desktop ID routed to wrong account", model.ID, routed, w.Code, w.Body.String())
		}
	}
	if models[0].ID != "github-copilot/claude-opus-4.6" || clientModelIDs(models, false)[1].ID != models[1].ID {
		t.Fatal("Desktop naming changed other clients")
	}
}
