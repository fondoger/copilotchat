package agentconfig

import (
	"encoding/json"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
	"strings"
	"testing"
)

func TestLocalAgentConfiguration(t *testing.T) {
	p := store.DefaultProvider()
	o := Options{Provider: p, Agent: ClaudeCode, BaseURL: localproxy.BaseURL(localproxy.DefaultPort, p.ID), Key: "fixture-local-key"}
	text, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal([]byte(text), &config) != nil || config.Env["ANTHROPIC_AUTH_TOKEN"] != o.Key || config.Env["ANTHROPIC_BASE_URL"] != o.BaseURL || config.Env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "1" {
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
	o.Model = provider.Model{ID: "gpt-fixture", Endpoint: "/responses"}
	text, err = Generate(o)
	if err != nil || !strings.Contains(text, `experimental_bearer_token = "fixture-local-key"`) || !strings.Contains(text, `wire_api = "responses"`) || strings.Contains(text, "http_headers") || strings.Contains(text, ".auth]") {
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
