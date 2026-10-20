package desktop

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/agentconfig"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestAgentSetupSelectionAndProtocols(t *testing.T) {
	a := newSettingsTestApp(t)
	a.settingsPage, a.extensionPage = "Extensions", "Agent setup"
	a.state.Settings.LocalForwarding = true
	a.state.Settings.LocalKey = "fixture-local-key"
	a.state.Settings.ForwardProfiles[0].Models = nil
	a.forwarder = localproxy.New(a.state.Settings.LocalKey)
	t.Cleanup(a.stopForwarding)
	a.providers[store.DefaultProviderID].models = []provider.Model{
		{ID: "claude-fixture", Name: "Claude Fixture", Endpoint: "/v1/messages"},
		{ID: "gpt-fixture", Name: "GPT Fixture", Endpoint: "/responses"},
	}
	a.syncForwardingProviders()
	tester := ui.NewTester(a.settingsView, 1000, 1500)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	for _, removed := range []string{"Provider source", "Model (optional)", "Generated agent configuration", "Choose ANTHROPIC_DEFAULT_FABLE_MODEL"} {
		if _, ok := tester.Find(removed); ok {
			t.Fatalf("%q should be absent or initially collapsed", removed)
		}
	}
	if _, ok := tester.Find("Copy configuration"); !ok {
		t.Fatal("copy button hidden with preview")
	}
	agentSetupScreenshot(t, tester, "agent-setup")
	settingsClick(t, tester, "Model defaults")
	var lastY float32
	for _, key := range agentconfig.ClaudeModelVariables {
		label, ok := tester.Find(key)
		selectBox, selectOK := tester.Find("Choose " + key)
		if !ok || !selectOK || selectBox.X < label.X+label.W || label.Y <= lastY {
			t.Fatalf("default must occupy a separate row with label left of select: %s %+v %+v", key, label, selectBox)
		}
		lastY = label.Y
		settingsClick(t, tester, "Choose "+key)
		if tester.HasText("Select Choose " + key + " gpt-fixture") {
			t.Fatal("Responses-only model offered for Claude")
		}
		settingsClick(t, tester, "Select Choose "+key+" claude-fixture")
		if a.selectedForwardProfile().ClaudeDefaults[key].ModelID != "claude-fixture" {
			t.Fatal("default choice not saved", key)
		}
	}
	settingsClick(t, tester, "Configuration")
	configText, configErr := a.agentConfiguration(a.selectedForwardProfile(), agentconfig.ClaudeCode)
	if !tester.HasText("Generated agent configuration") || configErr != nil || !strings.Contains(configText, "\"ANTHROPIC_DEFAULT_FABLE_MODEL\": \"claude-fixture\"") || strings.Contains(configText, "\"ANTHROPIC_MODEL\"") {
		t.Fatal("Claude config does not match defaults", tester.Texts())
	}
	agentSetupScreenshot(t, tester, "agent-setup-defaults")
	settingsClick(t, tester, agentconfig.Codex)
	configText, configErr = a.agentConfiguration(a.selectedForwardProfile(), agentconfig.Codex)
	if !tester.HasText("gpt-fixture") || configErr != nil || !strings.Contains(configText, "name = \"Copilot Chat Proxy\"") {
		t.Fatal("Codex config missing default model/provider")
	}
	settingsClick(t, tester, "Enable remote compaction")
	configText, configErr = a.agentConfiguration(a.selectedForwardProfile(), agentconfig.Codex)
	if configErr != nil || !strings.Contains(configText, "name = \"OpenAI\"") || !settingsSavedState(t, a).Settings.ForwardProfiles[0].CodexRemoteCompaction {
		t.Fatal("remote compaction did not update/persist config")
	}
	settingsClick(t, tester, "Configuration")
	agentSetupScreenshot(t, tester, "agent-setup-codex")
	a.providers[store.DefaultProviderID].models = a.providers[store.DefaultProviderID].models[:1]
	tester.Frame()
	if _, ok := tester.Find("Generated agent configuration"); ok {
		t.Fatal("unsupported protocol generated configuration")
	}
	if _, ok := tester.Find("Share github.com claude-fixture"); !ok {
		t.Fatal("model table vanished when configuration was unavailable")
	}
	a.provider(store.DefaultProviderID).Disabled = true
	tester.Frame()
	if len(a.forwardingModels(a.selectedForwardProfile())) != 0 {
		t.Fatal("disabled provider still selected")
	}
}

func TestAgentDefaultsFollowModelIdentityAndProfiles(t *testing.T) {
	a := profileTestApp(t)
	a.state.Settings.LocalKey = localproxy.DefaultKey
	p := a.selectedForwardProfile()
	p.ClaudeDefaults = map[string]store.ForwardModelRef{
		"ANTHROPIC_DEFAULT_SONNET_MODEL": {ProviderID: "work", ModelID: "gpt-5.5"},
	}
	p.CodexModel = p.ClaudeDefaults["ANTHROPIC_DEFAULT_SONNET_MODEL"]
	p.CodexRemoteCompaction = true
	a.setForwardEnabled(p, 0, false)
	for _, agent := range []string{agentconfig.ClaudeCode, agentconfig.Codex} {
		config, err := a.agentConfiguration(p, agent)
		if err != nil || strings.Contains(config, "gpt-5.5 (2)") || !strings.Contains(config, "\"gpt-5.5\"") {
			t.Fatal("model selection did not follow automatic alias", agent, config, err)
		}
	}
	a.setForwardAlias(p, 1, "claude-work")
	config, err := a.agentConfiguration(p, agentconfig.ClaudeCode)
	if err != nil || !strings.Contains(config, "\"ANTHROPIC_DEFAULT_SONNET_MODEL\": \"claude-work\"") {
		t.Fatal("model selection did not follow manual rename", config, err)
	}
	original := p.ID
	a.addForwardProfile(true)
	copy := a.selectedForwardProfile()
	copy.ClaudeDefaults["ANTHROPIC_DEFAULT_SONNET_MODEL"] = store.ForwardModelRef{ProviderID: store.DefaultProviderID, ModelID: "gpt-5.5"}
	copy.CodexRemoteCompaction = false
	a.saveSettings()
	a.state = settingsSavedState(t, a)
	a.chooseForwardProfile(original)
	p = a.selectedForwardProfile()
	if p.ClaudeDefaults["ANTHROPIC_DEFAULT_SONNET_MODEL"].ProviderID != "work" || p.CodexModel.ProviderID != "work" || !p.CodexRemoteCompaction {
		t.Fatal("duplicated profile choices were shared or not persisted")
	}
	a.setForwardEnabled(p, 1, false)
	for _, agent := range []string{agentconfig.ClaudeCode, agentconfig.Codex} {
		if _, err := a.agentConfiguration(p, agent); err == nil {
			t.Fatal("disabled explicit model silently replaced", agent)
		}
	}
}

func TestAgentModelTableUsesPageScroll(t *testing.T) {
	a := newSettingsTestApp(t)
	a.settingsPage, a.extensionPage = "Extensions", "Agent setup"
	a.state.Settings.ForwardProfiles[0].Models = nil
	for i := 0; i < 30; i++ {
		id := strings.Repeat("a", i+1)
		a.providers[store.DefaultProviderID].models = append(a.providers[store.DefaultProviderID].models, provider.Model{ID: id, Endpoint: "/v1/messages"})
	}
	a.syncForwardingProviders()
	tester := ui.NewTester(a.settingsView, 900, 620)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	first, ok := tester.Find("Share github.com gpt-4o")
	last, lastOK := tester.Find("Share github.com " + strings.Repeat("a", 30))
	if !ok || !lastOK || last.Y-first.Y < 900 {
		t.Fatal("model table is not fully expanded", first, last)
	}
	config, _ := tester.Find("Configuration")
	if first.Y < config.Y {
		t.Fatal("model table precedes configuration")
	}
	alias, _ := tester.Find("Alias github.com gpt-4o")
	if first.X < alias.X+alias.W {
		t.Fatal("Enabled is not on the right")
	}
	// Scrolling with the cursor over models moves the entire page, including
	// the forwarding controls, not just an inner table.
	before, _ := tester.Find("Enable local forwarding")
	tester.Scroll(800, first.Y, 0, 200)
	tester.Advance(time.Second)
	after, _ := tester.Find("Enable local forwarding")
	if after.Y >= before.Y {
		t.Fatal("scrolling over models did not scroll the settings page", before, after)
	}
}

func agentSetupScreenshot(t *testing.T, tester *ui.Tester, name string) {
	t.Helper()
	if root := os.Getenv("COPILOT_CHAT_AGENT_SCREENSHOT"); root != "" {
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(root, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tester.Image()); err != nil {
			t.Fatal(err)
		}
	}
}
