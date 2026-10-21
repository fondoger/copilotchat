package agentconfig

import (
	"encoding/json"
	"testing"

	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
)

func TestClaudeModelPickerAndCapabilities(t *testing.T) {
	o := Options{Agent: ClaudeCode, BaseURL: localproxy.BaseURL(39321, "work"), Key: "copilotchat", Models: []localproxy.Model{
		{ID: "m365-copilot/claude-sonnet-4.6", ProviderName: "Microsoft 365 Copilot", Model: provider.Model{ID: "claude-sonnet-4.6", Name: "Claude Sonnet 4.6", Endpoint: "/v1/messages", ContextWindow: 200000}},
		{ID: "github-copilot/gpt-6.1-sol", ProviderName: "GitHub Copilot", Model: provider.Model{ID: "gpt-6.1-sol", Name: "GPT 6.1 Sol", Endpoint: "/responses", ThinkingEfforts: []string{"high", "none", "xhigh", "low"}, ContextWindow: 400000}},
		{ID: "github-copilot/plain", Model: provider.Model{ID: "plain", Endpoint: "/responses"}},
		{ID: "github-copilot/chat-only", Model: provider.Model{ID: "chat-only", Endpoint: "/chat/completions"}},
	}}
	config, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ModelPicker struct {
			Options               []struct{ Model, Label, BehavesAs string }
			ReplaceBuiltInOptions bool
		}
		ModelSettings map[string]struct {
			MaxEffortLevel    string
			AutoCompactWindow any
			RealWindowSize    int `json:"#realContextWindow"`
		}
	}
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	rows := parsed.ModelPicker.Options
	if len(rows) != 3 || !parsed.ModelPicker.ReplaceBuiltInOptions {
		t.Fatal(config)
	}
	if rows[0].Model != o.Models[0].ID || rows[0].BehavesAs != "claude-sonnet-4-6" || rows[0].Label != "Claude Sonnet 4.6 (Microsoft 365 Copilot)" || rows[1].BehavesAs != "" {
		t.Fatal(config)
	}
	if rows[1].Model != o.Models[1].ID+"[1m]" || rows[2].Model != o.Models[2].ID+"[1m]" {
		t.Fatal("non-Claude models must request the 1M context tier", config)
	}
	if len(parsed.ModelSettings) != 3 || parsed.ModelSettings[o.Models[1].ID].MaxEffortLevel != "xhigh" || parsed.ModelSettings[o.Models[1].ID].AutoCompactWindow != float64(340000) || parsed.ModelSettings[o.Models[1].ID].RealWindowSize != 400000 {
		t.Fatal(config)
	}
	if claude := parsed.ModelSettings[o.Models[0].ID]; claude.RealWindowSize != 200000 || claude.AutoCompactWindow != nil || claude.MaxEffortLevel != "" {
		t.Fatal("Claude models should retain their built-in window/effort defaults", config)
	}
	if parsed.ModelSettings[o.Models[2].ID].AutoCompactWindow != "auto" {
		t.Fatal("missing catalog limit must not be invented", config)
	}
}

func TestClaudeCompactionWindowReservesHeadroom(t *testing.T) {
	for _, test := range []struct {
		actual int
		want   any
	}{
		{0, "auto"},
		{128000, 108800},
		{200000, 170000},
		{400000, 340000},
		{1000000, 400000},
	} {
		got := claudeModelSettings(provider.Model{ContextWindow: test.actual})
		if got["autoCompactWindow"] != test.want || got["#realContextWindow"] != test.actual {
			t.Fatalf("context %d: got %v", test.actual, got)
		}
	}
}

func TestClaudeDefaultModelUsesContextSuffix(t *testing.T) {
	o := Options{Agent: ClaudeCode, BaseURL: localproxy.BaseURL(39321, "work"), Key: "copilotchat",
		Models:         []localproxy.Model{{ID: "github-copilot/claude-user/gpt-5.5", Model: provider.Model{ID: "gpt-5.5", Endpoint: "/responses"}}},
		ClaudeDefaults: map[string]provider.Model{"ANTHROPIC_DEFAULT_SONNET_MODEL": {ID: "github-copilot/claude-user/gpt-5.5", Endpoint: "/responses"}},
	}
	config, err := Generate(o)
	var parsed struct{ Env map[string]string }
	if err != nil || json.Unmarshal([]byte(config), &parsed) != nil || parsed.Env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "github-copilot/claude-user/gpt-5.5[1m]" {
		t.Fatal(config, err)
	}
}
