// Package agentconfig builds copyable configuration for the local agent API.
package agentconfig

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

const ClaudeCode = "Claude Code"
const Codex = "Codex / ChatGPT Desktop"

type Options struct {
	Provider            store.Provider
	Model               provider.Model
	Agent, BaseURL, Key string
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
	if o.Provider.ID == "" || o.Provider.Disabled {
		return "", errors.New("select an enabled provider")
	}
	if o.Agent != ClaudeCode && o.Agent != Codex {
		return "", errors.New("select a supported agent")
	}
	if (o.Agent != ClaudeCode || o.Model.ID != "") && !Compatible(o.Model, o.Agent) {
		return "", errors.New("model does not support this agent's protocol")
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/providers/"+o.Provider.ID {
		return "", errors.New("invalid local provider endpoint")
	}
	if o.Key == "" {
		return "", errors.New("enable local forwarding first")
	}
	if o.Agent == ClaudeCode {
		env := map[string]string{"ANTHROPIC_BASE_URL": o.BaseURL, "ANTHROPIC_AUTH_TOKEN": o.Key, "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"}
		if o.Model.ID != "" {
			env["ANTHROPIC_MODEL"] = o.Model.ID
		}
		data, err := json.MarshalIndent(map[string]any{"env": env}, "", "  ")
		return string(data) + "\n", err
	}
	id := fmt.Sprintf("copilot_chat_%x", sha256.Sum256([]byte(o.Provider.ID)))[:29]
	var out strings.Builder
	fmt.Fprintf(&out, "model_provider = %s\nmodel = %s\n\n[model_providers.%s]\nname = %s\nbase_url = %s\nwire_api = \"responses\"\nexperimental_bearer_token = %s\n", quote(id), quote(o.Model.ID), id, quote(o.Provider.Name), quote(o.BaseURL), quote(o.Key))
	return out.String(), nil
}
func quote(value string) string { data, _ := json.Marshal(value); return string(data) }
