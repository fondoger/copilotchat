package agentconfig

import (
	"encoding/json"
	"reflect"
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
	want := map[string]any{
		"modelPrefer1mContext":    true,
		"inferenceGatewayBaseUrl": o.BaseURL, "inferenceGatewayApiKey": "copilotchat-claude-desktop",
		"inferenceProvider": "gateway", "inferenceCredentialKind": "static",
		"inferenceModelPricingEnabled": true, "inferenceModelPricingMultiplier": float64(1),
		"disableEssentialTelemetry": true, "disableNonessentialTelemetry": true, "disableNonessentialServices": true,
		"skipWebFetchPreflight": false, "disableDeploymentModeChooser": true,
	}
	if !reflect.DeepEqual(got, want) {
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
