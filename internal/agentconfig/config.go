// Package agentconfig builds copyable configuration for the local agent API.
package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
)

const ClaudeCode = "Claude Code"
const ClaudeDesktop = "Claude Desktop"
const Codex = "Codex / ChatGPT Desktop"

var ClaudeModelVariables = []string{
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

type Options struct {
	Model               provider.Model
	Models              []localproxy.Model
	Agent, BaseURL, Key string
	CatalogPath         string
	ClaudeDefaults      map[string]provider.Model
	RemoteCompaction    bool
}

func Compatible(model provider.Model, agent string) bool {
	switch agent {
	case ClaudeCode, ClaudeDesktop, Codex:
		return hasEndpoint(model, "/v1/messages") || hasEndpoint(model, "/responses")
	default:
		return false
	}
}

func hasEndpoint(model provider.Model, endpoint string) bool {
	if len(model.Endpoints) > 0 {
		return slices.Contains(model.Endpoints, endpoint)
	}
	return model.Endpoint == endpoint
}

func Generate(o Options) (string, error) {
	if o.Agent != ClaudeCode && o.Agent != ClaudeDesktop && o.Agent != Codex {
		return "", errors.New("select a supported agent")
	}
	if o.Agent == Codex && (o.Model.ID == "" || !Compatible(o.Model, o.Agent)) {
		return "", errors.New("model does not support this agent's protocol")
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !localproxy.ValidProfileName(strings.TrimPrefix(u.Path, "/")) {
		return "", errors.New("invalid local endpoint")
	}
	if o.Agent == ClaudeDesktop {
		data, err := json.MarshalIndent(map[string]any{
			"modelPrefer1mContext":            true,
			"inferenceGatewayBaseUrl":         o.BaseURL,
			"inferenceGatewayApiKey":          localproxy.ClaudeDesktopKey,
			"inferenceProvider":               "gateway",
			"inferenceCredentialKind":         "static",
			"inferenceModelPricingEnabled":    true,
			"inferenceModelPricingMultiplier": 1,
			"disableEssentialTelemetry":       true,
			"disableNonessentialTelemetry":    true,
			"disableNonessentialServices":     true,
			"skipWebFetchPreflight":           false,
			"disableDeploymentModeChooser":    true,
			"sshHostAllowlist":                []string{"*"},
			"builtinBrowserEnabled":           true,
		}, "", "  ")
		return string(data) + "\n", err
	}
	if o.Key == "" {
		return "", errors.New("enable local forwarding first")
	}
	if o.Agent == ClaudeCode {
		env := map[string]string{
			"ANTHROPIC_BASE_URL":                         o.BaseURL,
			"ANTHROPIC_AUTH_TOKEN":                       o.Key,
			"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
			"DISABLE_LOGIN_COMMAND":                      "1",
			"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS":     "1",
		}
		for _, key := range ClaudeModelVariables {
			model := o.ClaudeDefaults[key]
			if model.ID == "" {
				continue
			}
			if !Compatible(model, ClaudeCode) {
				return "", fmt.Errorf("%s does not support the Messages protocol", key)
			}
			upstreamID := model.ID[strings.LastIndex(model.ID, "/")+1:]
			for _, m := range o.Models {
				if m.ID == model.ID {
					upstreamID = m.Model.ID
					break
				}
			}
			env[key] = claudeConfigModelID(model.ID, upstreamID)
		}
		options := []map[string]string{}
		settings := map[string]any{}
		for _, m := range o.Models {
			if !Compatible(m.Model, ClaudeCode) {
				continue
			}
			label := m.Model.Name
			if label == "" {
				label = m.Model.ID
			}
			if m.ProviderName != "" {
				label += " (" + m.ProviderName + ")"
			}
			row := map[string]string{"model": claudeConfigModelID(m.ID, m.Model.ID), "label": label}
			if strings.Contains(strings.ToLower(m.Model.ID), "claude") {
				// Claude Code's settings schema spells this field "behavesAs".
				row["behavesAs"] = strings.ReplaceAll(m.Model.ID, ".", "-")
				settings[m.ID] = map[string]any{"#realContextWindow": m.Model.ContextWindow}
			} else {
				settings[m.ID] = claudeModelSettings(m.Model)
			}
			options = append(options, row)
		}
		data, err := json.MarshalIndent(map[string]any{"env": env,
			"modelPicker":   map[string]any{"options": options, "replaceBuiltInOptions": true},
			"modelSettings": settings}, "", "  ")
		return string(data) + "\n", err
	}
	if o.CatalogPath == "" {
		return "", errors.New("local Codex model catalog is unavailable")
	}
	var out strings.Builder
	name := "Copilot Chat Proxy"
	if o.RemoteCompaction {
		name = "OpenAI"
	}
	fmt.Fprintf(&out, "model_provider = \"copilot_chat\"\nmodel = %s\nmodel_catalog_json = %s\n\n[model_providers.copilot_chat]\nname = %s\nbase_url = %s\nwire_api = \"responses\"\nexperimental_bearer_token = %s\n", quote(o.Model.ID), quote(o.CatalogPath), quote(name), quote(o.BaseURL), quote(o.Key))
	return out.String(), nil
}

func claudeConfigModelID(id, upstreamID string) string {
	if strings.Contains(strings.ToLower(upstreamID), "claude") {
		return id
	}
	return strings.TrimSuffix(id, "[1m]") + "[1m]"
}

func claudeModelSettings(m provider.Model) map[string]any {
	// Use advertised levels, not model-name guesses. "low" is Claude Code's
	// lowest selectable cap; the adapter omits reasoning for non-reasoning models.
	effort := "low"
	for _, candidate := range []string{"low", "medium", "high", "xhigh", "max"} {
		if slices.Contains(m.ThinkingEfforts, candidate) {
			effort = candidate
		}
	}
	var window any = "auto"
	// Leave room for the reply and cap automatic compaction at 400K tokens.
	// Unknown catalogs keep automatic sizing rather than inventing a limit.
	if m.ContextWindow > 0 {
		window = min(m.ContextWindow*85/100, 400000)
	}
	return map[string]any{"maxEffortLevel": effort, "autoCompactWindow": window, "#realContextWindow": m.ContextWindow}
}
func quote(value string) string { data, _ := json.Marshal(value); return string(data) }
