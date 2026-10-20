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
	Agent, BaseURL, Key string
	CatalogPath         string
	ClaudeDefaults      map[string]provider.Model
	RemoteCompaction    bool
}

func Compatible(model provider.Model, agent string) bool {
	endpoint := ""
	switch agent {
	case ClaudeCode:
		endpoint = "/v1/messages"
	case Codex:
		endpoint = "/responses"
	default:
		return false
	}
	if len(model.Endpoints) > 0 {
		return slices.Contains(model.Endpoints, endpoint)
	}
	return model.Endpoint == endpoint
}

func Generate(o Options) (string, error) {
	if o.Agent != ClaudeCode && o.Agent != Codex {
		return "", errors.New("select a supported agent")
	}
	if o.Agent == Codex && (o.Model.ID == "" || !Compatible(o.Model, o.Agent)) {
		return "", errors.New("model does not support this agent's protocol")
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !localproxy.ValidProfileName(strings.TrimPrefix(u.Path, "/")) {
		return "", errors.New("invalid local endpoint")
	}
	if o.Key == "" {
		return "", errors.New("enable local forwarding first")
	}
	if o.Agent == ClaudeCode {
		env := map[string]string{"ANTHROPIC_BASE_URL": o.BaseURL, "ANTHROPIC_AUTH_TOKEN": o.Key, "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"}
		for _, key := range ClaudeModelVariables {
			model := o.ClaudeDefaults[key]
			if model.ID == "" {
				continue
			}
			if !Compatible(model, ClaudeCode) {
				return "", fmt.Errorf("%s does not support the Messages protocol", key)
			}
			env[key] = model.ID
		}
		data, err := json.MarshalIndent(map[string]any{"env": env}, "", "  ")
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
func quote(value string) string { data, _ := json.Marshal(value); return string(data) }
