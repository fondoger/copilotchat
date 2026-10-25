package agentconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
)

func TestLocalAgentConfiguration(t *testing.T) {
	o := Options{Agent: ClaudeCode, BaseURL: localproxy.BaseURL(localproxy.DefaultPort, "my-claude-code-profile"), Key: localproxy.DefaultKey, CatalogPath: `C:\User Data\agent-catalog.json`}
	text, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal([]byte(text), &config) != nil || config.Env["ANTHROPIC_AUTH_TOKEN"] != o.Key || config.Env["ANTHROPIC_BASE_URL"] != o.BaseURL {
		t.Fatal(text)
	}
	if _, ok := config.Env["ANTHROPIC_MODEL"]; ok {
		t.Fatal("optional model forced")
	}
	for _, disallowed := range []string{"apiKeyHelper", "CUSTOM_HEADERS", "powershell", "TTL_MS", "data-dir"} {
		if strings.Contains(text, disallowed) {
			t.Fatal(disallowed)
		}
	}
	o.Agent = Codex
	o.Model = provider.Model{ID: "gpt-fixture (2)", Endpoint: "/responses"}
	text, err = Generate(o)
	if err != nil || !strings.Contains(text, `experimental_bearer_token = "copilotchat"`) || !strings.Contains(text, `wire_api = "responses"`) || !strings.Contains(text, `model = "gpt-fixture (2)"`) || !strings.Contains(text, `model_catalog_json = "C:\\User Data\\agent-catalog.json"`) || strings.Contains(text, "http_headers") || strings.Contains(text, ".auth]") {
		t.Fatal(text, err)
	}
	o.Model.Endpoint = "/v1/messages"
	if config, err := Generate(o); err != nil || !strings.Contains(config, `wire_api = "responses"`) {
		t.Fatal("Messages model was not exposed through the Responses bridge", config, err)
	}
	o.Model.Endpoint = "/chat/completions"
	if _, err = Generate(o); err == nil {
		t.Fatal("incompatible model accepted")
	}
	o.Agent = ClaudeCode
	o.Model = provider.Model{}
	for _, base := range []string{"https://example.com", "http://0.0.0.0:39321/providers/github.com", o.BaseURL + "?destination=other", o.BaseURL + "/../other"} {
		o.BaseURL = base
		if _, err = Generate(o); err == nil {
			t.Fatal("invalid endpoint accepted", base)
		}
	}
}

func TestClaudeDefaultsAreOptionalAndProtocolChecked(t *testing.T) {
	o := Options{Agent: ClaudeCode, BaseURL: localproxy.BaseURL(39321, "claude"), Key: "copilotchat", ClaudeDefaults: map[string]provider.Model{}}
	for _, key := range ClaudeModelVariables {
		o.ClaudeDefaults[key] = provider.Model{ID: "claude-work (2)", Endpoints: []string{"/responses", "/v1/messages"}}
	}
	text, err := Generate(o)
	var config struct{ Env map[string]string }
	if err != nil || json.Unmarshal([]byte(text), &config) != nil {
		t.Fatal(text, err)
	}
	for _, key := range ClaudeModelVariables {
		if config.Env[key] != "claude-work (2)" {
			t.Fatal("missing model mapping", key)
		}
	}
	delete(o.ClaudeDefaults, ClaudeModelVariables[0])
	text, err = Generate(o)
	if err != nil || strings.Contains(text, ClaudeModelVariables[0]) || strings.Contains(text, "ANTHROPIC_MODEL") {
		t.Fatal("unset variable was not omitted", text, err)
	}
	o.ClaudeDefaults[ClaudeModelVariables[1]] = provider.Model{ID: "chat-only", Endpoint: "/chat/completions"}
	if _, err := Generate(o); err == nil {
		t.Fatal("incompatible default allowed")
	}
}

func TestClaudePermissionMode(t *testing.T) {
	for _, mode := range []string{"", "default", "auto", "acceptEdits", "plan", "dontAsk", "bypassPermissions", "invalid", "Auto", " auto "} {
		t.Run("mode="+mode, func(t *testing.T) {
			text, err := Generate(Options{
				Agent: ClaudeCode, BaseURL: localproxy.BaseURL(39321, "claude"), Key: "copilotchat",
				ClaudePermissionMode: mode,
			})
			if mode == "invalid" || mode == "Auto" || mode == " auto " {
				if err == nil {
					t.Fatal("invalid permission mode accepted", text)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Permissions map[string]string `json:"permissions"`
			}
			if err := json.Unmarshal([]byte(text), &config); err != nil {
				t.Fatal(err)
			}
			if mode == "" {
				if config.Permissions != nil {
					t.Fatal("unconfigured permissions must be omitted", text)
				}
			} else if len(config.Permissions) != 1 || config.Permissions["defaultMode"] != mode {
				t.Fatal("permission mode was not generated as permissions.defaultMode", text)
			}
		})
	}
}

func TestClaudeAutoModeModel(t *testing.T) {
	for _, test := range []struct {
		id, upstream, want string
	}{
		{"", "", ""},
		{"work/claude-fixture", "claude-fixture", "work/claude-fixture"},
		{"work/claude-user/gpt-fixture", "gpt-fixture", "work/claude-user/gpt-fixture[1m]"},
		{"work/gpt-fixture[1m]", "gpt-fixture", "work/gpt-fixture[1m]"},
	} {
		t.Run("model="+test.id, func(t *testing.T) {
			model := provider.Model{ID: test.id, Endpoint: "/responses"}
			text, err := Generate(Options{
				Agent: ClaudeCode, BaseURL: localproxy.BaseURL(39321, "claude"), Key: "copilotchat",
				ClaudeDefaults: map[string]provider.Model{"CLAUDE_CODE_AUTO_MODE_MODEL": model},
				Models:         []localproxy.Model{{ID: test.id, Model: provider.Model{ID: test.upstream, Endpoint: "/responses"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var config struct{ Env map[string]string }
			if err := json.Unmarshal([]byte(text), &config); err != nil {
				t.Fatal(err)
			}
			got, present := config.Env["CLAUDE_CODE_AUTO_MODE_MODEL"]
			if got != test.want || present != (test.id != "") {
				t.Fatal("auto mode model must use the default model ID rules", text)
			}
		})
	}
}

func TestCodexRemoteCompactionControlsProviderName(t *testing.T) {
	o := Options{Agent: Codex, BaseURL: localproxy.BaseURL(39321, "codex"), Key: "copilotchat", CatalogPath: "/tmp/catalog.json", Model: provider.Model{ID: "gpt-fixture", Endpoint: "/responses"}}
	for _, enabled := range []bool{false, true} {
		o.RemoteCompaction = enabled
		text, err := Generate(o)
		name := "Copilot Chat Proxy"
		if enabled {
			name = "OpenAI"
		}
		if err != nil || !strings.Contains(text, "name = "+quote(name)+"\n") {
			t.Fatal("wrong compaction provider name", text, err)
		}
	}
}
