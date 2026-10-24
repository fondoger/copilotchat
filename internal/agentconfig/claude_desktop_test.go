package agentconfig

import (
	"encoding/json"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestClaudeDesktopConfiguration(t *testing.T) {
	o := Options{Agent: ClaudeDesktop, BaseURL: "http://127.0.0.1:39321/claude-code", Key: "other-profile-key"}
	config, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(config), &got); err != nil {
		t.Fatal(err)
	}
	if got["inferenceGatewayBaseUrl"] != o.BaseURL || got["inferenceGatewayApiKey"] != "copilotchat-claude-desktop" ||
		got["inferenceProvider"] != "gateway" || got["inferenceCredentialKind"] != "static" {
		t.Fatal(config)
	}
	for _, endpoint := range []string{"/v1/messages", "/responses"} {
		if !Compatible(provider.Model{Endpoint: endpoint}, ClaudeDesktop) {
			t.Fatal("Desktop cannot use Messages bridge", endpoint)
		}
	}
	if Compatible(provider.Model{Endpoint: "/chat/completions"}, ClaudeDesktop) {
		t.Fatal("unimplemented translation exposed")
	}
}
