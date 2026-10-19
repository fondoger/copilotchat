// Package agentconfig builds token-free configuration for direct agent access.
package agentconfig

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/microsoft"
	"github.com/fondoger/copilotchat/internal/store"
)

const ClaudeCode = "Claude Code"
const Codex = "Codex / ChatGPT Desktop"

type Options struct {
	Provider                            store.Provider
	Model                               provider.Model
	Agent, Helper, DataDir, BaseURL, OS string
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
	if o.Model.ID == "" || !Compatible(o.Model, o.Agent) {
		return "", errors.New("this model does not advertise the protocol required by the selected agent")
	}
	if o.Helper == "" || o.DataDir == "" {
		return "", errors.New("could not locate the bundled helper or settings directory")
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid provider API endpoint")
	}
	headers := map[string]string{"User-Agent": provider.UserAgent}
	switch o.Provider.Kind {
	case store.ProviderKindM365:
		if o.BaseURL != microsoft.BaseURL {
			return "", errors.New("unexpected Microsoft 365 API endpoint")
		}
		headers["X-ClientType"] = "GitHub"
	case store.ProviderKindGitHub:
		host := strings.ToLower(u.Hostname())
		valid := o.Provider.Host == "github.com" && (host == "githubcopilot.com" || strings.HasSuffix(host, ".githubcopilot.com"))
		valid = valid || (strings.HasSuffix(o.Provider.Host, ".ghe.com") && strings.HasSuffix(host, "."+o.Provider.Host))
		if !valid || (u.Path != "" && u.Path != "/") {
			return "", errors.New("unexpected GitHub Copilot API endpoint")
		}
		headers["X-GitHub-Api-Version"] = "2026-08-01"
		headers["Openai-Intent"] = "conversation-edits"
		headers["X-Interaction-Type"] = "conversation-agent"
		headers["x-initiator"] = "user"
		if o.Model.Vision {
			headers["Copilot-Vision-Request"] = "true"
		}
	default:
		return "", errors.New("unsupported provider kind")
	}
	args := []string{"--provider", o.Provider.ID, "--data-dir", o.DataDir}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if o.Agent == ClaudeCode {
		lines := make([]string, 0, len(headers))
		for _, key := range keys {
			lines = append(lines, key+": "+headers[key])
		}
		config := map[string]any{
			"apiKeyHelper": shellCommand(o.OS, o.Helper, args),
			"env": map[string]string{
				"ANTHROPIC_BASE_URL":                strings.TrimRight(o.BaseURL, "/"),
				"ANTHROPIC_MODEL":                   o.Model.ID,
				"ANTHROPIC_DEFAULT_OPUS_MODEL":      o.Model.ID,
				"ANTHROPIC_DEFAULT_SONNET_MODEL":    o.Model.ID,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":     o.Model.ID,
				"ANTHROPIC_SMALL_FAST_MODEL":        o.Model.ID,
				"ANTHROPIC_CUSTOM_HEADERS":          strings.Join(lines, "\n"),
				"CLAUDE_CODE_API_KEY_HELPER_TTL_MS": "300000",
			},
		}
		data, err := json.MarshalIndent(config, "", "  ")
		return string(data) + "\n", err
	}
	id := fmt.Sprintf("copilot_chat_%x", sha256.Sum256([]byte(o.Provider.ID)))[:29]
	var out strings.Builder
	fmt.Fprintf(&out, "model_provider = %s\nmodel = %s\n\n[model_providers.%s]\nname = %s\nbase_url = %s\nwire_api = \"responses\"\nhttp_headers = { ", quote(id), quote(o.Model.ID), id, quote(o.Provider.Name), quote(strings.TrimRight(o.BaseURL, "/")))
	for i, key := range keys {
		if i > 0 {
			out.WriteString(", ")
		}
		fmt.Fprintf(&out, "%s = %s", quote(key), quote(headers[key]))
	}
	out.WriteString(" }\n")
	fmt.Fprintf(&out, "\n[model_providers.%s.auth]\ncommand = %s\nargs = [%s]\ntimeout_ms = 75000\nrefresh_interval_ms = 300000\n", id, quote(o.Helper), quotedArgs(args))
	return out.String(), nil
}

func quote(value string) string { data, _ := json.Marshal(value); return string(data) }
func quotedArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = quote(arg)
	}
	return strings.Join(quoted, ", ")
}

func shellCommand(goos, helper string, args []string) string {
	values := append([]string{helper}, args...)
	if goos == "windows" {
		// Claude runs a shell string. EncodedCommand survives both cmd.exe and
		// Git Bash, including spaces, apostrophes, %, $, and non-ASCII paths.
		for i, value := range values {
			values[i] = "'" + strings.ReplaceAll(value, "'", "''") + "'"
		}
		units := utf16.Encode([]rune("& " + strings.Join(values, " ") + "; exit $LASTEXITCODE"))
		data := make([]byte, 2*len(units))
		for i, u := range units {
			binary.LittleEndian.PutUint16(data[2*i:], u)
		}
		return "powershell.exe -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(data)
	}
	for i, value := range values {
		values[i] = "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
	}
	return strings.Join(values, " ")
}
