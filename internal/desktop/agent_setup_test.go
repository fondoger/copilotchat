package desktop

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/agentconfig"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestAgentSetupSelectionAndProtocols(t *testing.T) {
	a := newSettingsTestApp(t)
	a.settingsPage = "Extensions"
	a.state.Settings.LocalForwarding = true
	a.state.Settings.LocalKey = "fixture-local-key"
	a.forwarder = localproxy.New(a.state.Settings.LocalKey)
	t.Cleanup(a.stopForwarding)
	a.providers[store.DefaultProviderID].models = []provider.Model{
		{ID: "claude-fixture", Name: "Claude Fixture", Endpoint: "/v1/messages"},
		{ID: "gpt-fixture", Name: "GPT Fixture", Endpoint: "/responses"},
	}
	a.syncForwardingProviders()
	tester := ui.NewTester(a.settingsView, 1000, 1500)
	settingsClick(t, tester, "Agent setup extension")
	if _, ok := tester.Find("Provider source"); ok {
		t.Fatal("provider selector remains")
	}
	if a.agentSetup.modelID != "" {
		t.Fatal("Claude model should be optional")
	}
	if !tester.HasText("Choose in Claude Code") {
		t.Fatal("missing optional model selection")
	}
	if _, ok := tester.Find("Generated agent configuration"); !ok {
		t.Fatal("configuration missing")
	}
	settingsClick(t, tester, "Agent model")
	settingsClick(t, tester, "Select Agent model claude-fixture")
	if a.agentSetup.modelID != "claude-fixture" {
		t.Fatal("explicit compatible model selection failed")
	}
	settingsClick(t, tester, "Agent application")
	settingsClick(t, tester, "Select Agent application "+agentconfig.Codex)
	if a.agentSetup.modelID != "gpt-fixture" || !tester.HasText("~/.codex/config.toml") {
		t.Fatal("Codex retained incompatible model")
	}
	if root := os.Getenv("COPILOT_CHAT_AGENT_SCREENSHOT"); root != "" {
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(root, "agent-setup.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tester.Image()); err != nil {
			t.Fatal(err)
		}
	}
	a.providers[store.DefaultProviderID].models = a.providers[store.DefaultProviderID].models[:1]
	tester = ui.NewTester(a.settingsView, 1000, 1500)
	if _, ok := tester.Find("Generated agent configuration"); ok {
		t.Fatal("unsupported protocol generated configuration")
	}
	a.provider(store.DefaultProviderID).Disabled = true
	tester = ui.NewTester(a.settingsView, 1000, 1500)
	if len(a.forwardingModels(a.selectedForwardProfile())) != 0 {
		t.Fatal("disabled provider still selected")
	}
}
