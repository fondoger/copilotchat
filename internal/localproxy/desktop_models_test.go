package localproxy

import (
	"encoding/json"
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
		{provider.Model{ID: "claude-opus-4.6"}, "claude-opus-4.6-", "opus"},
		{provider.Model{ID: "claude-sonnet-4.7"}, "claude-sonnet-4.7-", "sonnet"},
		{provider.Model{ID: "gpt-5.5", ThinkingEfforts: []string{"low", "medium", "high", "xhigh"}, ContextWindow: 400000}, "opus-", "opus"},
		{provider.Model{ID: "grok-4.7", ThinkingEfforts: []string{"low", "high"}, ContextWindow: 256000}, "opus-4-5-", "opus"},
		{provider.Model{ID: "deepseek-reasoner", ThinkingEfforts: []string{"high"}, ContextWindow: 1000000}, "opus-4-5-", "opus"},
		{provider.Model{ID: "gemini-thinking", ThinkingEfforts: []string{"xhigh", "max"}, ContextWindow: 1000000}, "fable-", "fable"},
		{provider.Model{ID: "qwen-fast"}, "sonnet-4-5-", "sonnet"},
		{provider.Model{ID: "glm-fast", ThinkingEfforts: []string{"none"}}, "sonnet-4-5-", "sonnet"},
		{provider.Model{ID: "grok-4.7", ThinkingEfforts: []string{"low", "medium", "high", "xhigh"}, ContextWindow: 500000}, "opus-", "opus"},
		{provider.Model{ID: "long-context", ContextWindow: 1000000}, "opus-4-5-", "opus"},
		{provider.Model{ID: "short-context", ContextWindow: 999999}, "sonnet-4-5-", "sonnet"},
		{provider.Model{ID: "budget-reasoner", MaxThinkingBudget: 4096, ContextWindow: 128000}, "opus-4-5-", "opus"},
		{provider.Model{ID: "max-small-window", ThinkingEfforts: []string{"max"}, ContextWindow: 128000}, "fable-", "fable"},
	} {
		t.Run(test.model.ID, func(t *testing.T) {
			model := Model{ProviderID: "account-a", ID: "github-copilot/" + test.model.ID, Model: test.model}
			id := claudeDesktopModelID(model)
			if !strings.HasPrefix(id, test.prefix) || !regexp.MustCompile(`^[0-9]+$`).MatchString(strings.TrimPrefix(id, test.prefix)) || claudeDesktopModelLine(test.model) != test.line {
				t.Fatal("invalid Desktop model name", id)
			}
			// Public names and account counts may change; the provider binding must not.
			model.ID = "github-copilot/user/" + test.model.ID
			model.ProviderName = "Renamed provider"
			if claudeDesktopModelID(model) != id {
				t.Fatal("public route changed Desktop ID")
			}
			model.ProviderID = "account-b"
			if claudeDesktopModelID(model) == id {
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
	if row["id"] != claudeDesktopModelID(models[0]) || row["line"] != "opus" || row["display_name"] != "GPT Fixture (Work)" || row["max_input_tokens"] != float64(400000) {
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
