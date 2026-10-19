package agentconfig

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/microsoft"
	"github.com/fondoger/copilotchat/internal/store"
)

func fixture() Options {
	return Options{Provider: store.Provider{ID: "work-id", Name: "Work \"one\"", Kind: store.ProviderKindM365}, Model: provider.Model{ID: "claude-fixture", Endpoint: "/v1/messages"}, Agent: ClaudeCode, Helper: `C:\Apps with spaces\helper.exe`, DataDir: `C:\Data\你好`, BaseURL: microsoft.BaseURL, OS: "windows"}
}

func TestClaudeConfigurationHasNoTokenAndPinsEveryModelAlias(t *testing.T) {
	o := fixture()
	config, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Helper string            `json:"apiKeyHelper"`
		Env    map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
		if parsed.Env[key] != o.Model.ID {
			t.Fatal("unconfigured model alias", key)
		}
	}
	if parsed.Env["ANTHROPIC_BASE_URL"] != microsoft.BaseURL || !strings.Contains(parsed.Env["ANTHROPIC_CUSTOM_HEADERS"], "X-ClientType: GitHub") {
		t.Fatal("missing service configuration")
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if _, ok := parsed.Env[key]; ok {
			t.Fatal("static credential field")
		}
	}
	if !strings.HasPrefix(parsed.Helper, "powershell.exe -NoProfile -NonInteractive -EncodedCommand ") {
		t.Fatal("unsafe helper invocation")
	}
}

func TestProtocolSelectionAndAccountRouting(t *testing.T) {
	o := fixture()
	o.Agent = Codex
	if _, err := Generate(o); err == nil {
		t.Fatal("Messages-only model accepted by Codex")
	}
	o.Model.Endpoints = []string{"/v1/messages", "/responses"}
	config, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`wire_api = "responses"`, `timeout_ms = 75000`, `refresh_interval_ms = 300000`, `args = ["--provider", "work-id", "--data-dir"`, `.auth]`} {
		if !strings.Contains(config, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	for _, forbidden := range []string{"env_key", "requires_openai_auth", "experimental_bearer_token"} {
		if strings.Contains(config, forbidden) {
			t.Fatal("conflicting authentication setting")
		}
	}
	o.BaseURL = "https://example.test"
	if _, err := Generate(o); err == nil {
		t.Fatal("arbitrary token destination accepted")
	}
	o.Provider = store.Provider{ID: "enterprise", Kind: store.ProviderKindGitHub, Host: "example.ghe.com"}
	o.BaseURL = "https://copilot-api.example.ghe.com"
	if _, err := Generate(o); err != nil {
		t.Fatal(err)
	}
	o.BaseURL = "https://copilot-api.other.ghe.com"
	if _, err := Generate(o); err == nil {
		t.Fatal("another enterprise route accepted")
	}
	o = fixture()
	o.Provider.Disabled = true
	if _, err := Generate(o); err == nil {
		t.Fatal("disabled provider configured")
	}
}

func TestHelperShellQuotingRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "space '$ % 你好")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--provider", "p'$ & %name%", "--data-dir", dir}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "helper.ps1")
		if err := os.WriteFile(path, []byte("ConvertTo-Json -InputObject @($args) -Compress\nexit 37\n"), 0600); err != nil {
			t.Fatal(err)
		}
		command := shellCommand("windows", path, args)
		parts := strings.Fields(command)
		cmd = exec.Command(parts[0], parts[1:]...)
	} else {
		path := filepath.Join(dir, "helper")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nexit 37\n"), 0700); err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command("/bin/sh", "-c", shellCommand(runtime.GOOS, path, args))
	}
	output, err := cmd.Output()
	if err == nil || cmd.ProcessState.ExitCode() != 37 {
		t.Fatalf("exit status lost: %v", err)
	}
	var got []string
	if runtime.GOOS == "windows" {
		if json.Unmarshal(output, &got) != nil {
			t.Fatal("invalid result", string(output))
		}
	} else {
		got = strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	}
	if len(got) != len(args) {
		t.Fatal("argument count changed")
	}
	for i := range args {
		if got[i] != args[i] {
			t.Fatalf("argument %d changed: %q", i, got[i])
		}
	}
}
