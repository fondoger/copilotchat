package desktop

import (
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/agentconfig"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestClaudeDefaultsUseUnsharedModelsAndPreserveUnavailableIdentity(t *testing.T) {
	for _, key := range agentconfig.ClaudeModelVariables {
		for _, unavailable := range []string{"", "disabled", "signed out", "missing model", "wrong protocol"} {
			t.Run(key+"/"+unavailable, func(t *testing.T) {
				a := profileTestApp(t)
				a.state.Settings.LocalKey = "fixture-local-key"
				p := a.selectedForwardProfile()
				p.ClaudeDefaults = map[string]store.ForwardModelRef{key: {ProviderID: "work", ModelID: "gpt-5.5"}}
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
				selected := a.claudeDefaults(p)[key]
				if selected.ProviderID != "work" || selected.Model.ID != "gpt-5.5" {
					t.Fatal("default model changed account or identity", selected)
				}
				config, err := a.agentConfiguration(p, agentconfig.ClaudeCode)
				if unavailable != "" {
					if err == nil || agentconfig.Compatible(selected.Model, agentconfig.ClaudeCode) {
						t.Fatal("unavailable default retained capabilities", selected, config, err)
					}
				} else if err != nil || !strings.Contains(config, localproxy.DefaultModelID(selected)+"[1m]") {
					t.Fatal("unshared default was not generated as a private route", config, err)
				}
				if key == "ANTHROPIC_DEFAULT_SONNET_MODEL" && unavailable == "" && !strings.Contains(config, localproxy.ClassifierModelID(selected)+"[1m]") {
					t.Fatal("classifier did not reuse the unshared Sonnet default", config)
				}
				_, err = a.agentConfiguration(p, agentconfig.ClaudeDesktop)
				usesDesktop := key == "ANTHROPIC_DEFAULT_SONNET_MODEL" || key == "ANTHROPIC_DEFAULT_HAIKU_MODEL" || key == "CLAUDE_CODE_SUBAGENT_MODEL"
				if (err != nil) != (usesDesktop && unavailable != "") {
					t.Fatal("Desktop validated the wrong defaults", key, unavailable, err)
				}
				if len(a.forwardingModels(p)) != 0 {
					t.Fatal("default choice enabled ordinary sharing")
				}
			})
		}
	}
}
