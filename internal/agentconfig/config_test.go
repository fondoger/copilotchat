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
