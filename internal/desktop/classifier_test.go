package desktop

import (
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/agentconfig"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestClassifierIgnoresSharingButRequiresAvailableProvider(t *testing.T) {
	for _, unavailable := range []string{"", "disabled", "signed out", "missing model", "wrong protocol"} {
		t.Run(unavailable, func(t *testing.T) {
			a := profileTestApp(t)
			a.state.Settings.LocalKey = "fixture-local-key"
			p := a.selectedForwardProfile()
			p.ClaudeClassifierModel = store.ForwardModelRef{ProviderID: "work", ModelID: "gpt-5.5"}
			a.setForwardEnabled(p, -1, false)
			switch unavailable {
			case "disabled":
				a.provider("work").Disabled = true
			case "signed out":
				a.providers["work"].client = nil
			case "missing model":
				a.providers["work"].models = nil
			case "wrong protocol":
				a.providers["work"].models = []provider.Model{{ID: "gpt-5.5", Endpoint: "/chat/completions"}}
			}
			if len(a.forwardingModels(p)) != 0 {
				t.Fatal("classifier widened shared models")
			}
			selected := a.classifierModel(p)
			for _, agent := range []string{agentconfig.ClaudeCode, agentconfig.ClaudeDesktop} {
				config, err := a.agentConfiguration(p, agent)
				if unavailable != "" {
					if err == nil {
						t.Fatal("unavailable classifier generated configuration", agent, config)
					}
				} else if err != nil {
					t.Fatal("disabled checkbox blocked classifier", agent, err)
				}
			}
			if selected.ProviderID != "work" || selected.Model.ID != "gpt-5.5" {
				t.Fatal("classifier identity lost", selected)
			}
			if unavailable != "" && agentconfig.Compatible(selected.Model, agentconfig.ClaudeCode) {
				t.Fatal("unavailable classifier retained capabilities", selected)
			}
			if unavailable == "" {
				p.ClaudeDefaults = map[string]store.ForwardModelRef{"ANTHROPIC_DEFAULT_SONNET_MODEL": p.ClaudeClassifierModel}
				if _, err := a.agentConfiguration(p, agentconfig.ClaudeCode); err == nil {
					t.Fatal("ordinary default bypassed sharing filter")
				}
				delete(p.ClaudeDefaults, "ANTHROPIC_DEFAULT_SONNET_MODEL")
				config, err := a.agentConfiguration(p, agentconfig.ClaudeCode)
				if err != nil || !strings.Contains(config, "copilotchat-classifier-") {
					t.Fatal(config, err)
				}
			}
		})
	}
}
