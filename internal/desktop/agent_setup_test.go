package desktop

import (
	"encoding/json"
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
	a.settingsPage = "LLM Gateway"
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
	for _, key := range agentconfig.ClaudeModelVariables {
		settingsClick(t, tester, "Choose "+key)
		if _, ok := tester.Find("Select Choose " + key + " github-copilot/gpt-fixture"); !ok {
			t.Fatal("Responses model missing from Claude defaults")
		}
		settingsClick(t, tester, "Select Choose "+key+" github-copilot/claude-fixture")
		if a.selectedForwardProfile().ClaudeDefaults[key].ModelID != "claude-fixture" {
			t.Fatal("default choice not saved", key)
		}
	}
	settingsClick(t, tester, "Choose default permission mode")
	settingsClick(t, tester, "Select Choose default permission mode Manual")
	if settingsSavedState(t, a).Settings.ForwardProfiles[0].ClaudePermissionMode != "default" {
		t.Fatal("permission mode selection was not persisted")
	}
	settingsClick(t, tester, "Configuration")
	configText, configErr := a.agentConfiguration(a.selectedForwardProfile(), agentconfig.ClaudeCode)
	if !tester.HasText("Generated agent configuration") || configErr != nil || !strings.Contains(configText, "\"ANTHROPIC_DEFAULT_FABLE_MODEL\": \"github-copilot/claude-fixture\"") || strings.Contains(configText, "\"ANTHROPIC_MODEL\"") {
		t.Fatal("Claude config does not match defaults", tester.Texts())
	}
	agentSetupScreenshot(t, tester, "agent-setup-defaults")
	var codeConfig struct{ Env map[string]string }
	sonnet := a.claudeDefaults(a.selectedForwardProfile())["ANTHROPIC_DEFAULT_SONNET_MODEL"]
	if err := json.Unmarshal([]byte(configText), &codeConfig); err != nil || codeConfig.Env["CLAUDE_CODE_AUTO_MODE_MODEL"] != localproxy.ClassifierModelID(sonnet) {
		t.Fatal("Code classifier did not reuse the selected Sonnet default", configText, err)
	}
	settingsClick(t, tester, agentconfig.ClaudeDesktop)
	settingsClick(t, tester, "Share github.com gpt-fixture")
	for _, choice := range claudeModelDefaults {
		settingsClick(t, tester, "Choose "+choice.label)
		settingsClick(t, tester, "Select Choose "+choice.label+" github-copilot/gpt-fixture")
		if settingsSavedState(t, a).Settings.ForwardProfiles[0].ClaudeDefaults[choice.key].ModelID != "gpt-fixture" {
			t.Fatal("Desktop default selection was not persisted", choice.key)
		}
	}
	configText, configErr = a.agentConfiguration(a.selectedForwardProfile(), agentconfig.ClaudeDesktop)
	if configErr != nil || !strings.Contains(configText, `"inferenceGatewayApiKey": "copilotchat-claude-desktop"`) || !tester.HasText("Claude Desktop gateway configuration (JSON)") {
		t.Fatal("Claude Desktop configuration missing", configText, configErr)
	}
	settingsClick(t, tester, agentconfig.ClaudeCode)
	configText, configErr = a.agentConfiguration(a.selectedForwardProfile(), agentconfig.ClaudeCode)
	if configErr != nil || !strings.Contains(configText, "copilotchat-classifier-") || !strings.Contains(configText, "copilotchat-default-") || strings.Contains(configText, "github-copilot/gpt-fixture") {
		t.Fatal("unshared defaults or classifier were not generated separately", configText, configErr)
	}
	sonnet = a.claudeDefaults(a.selectedForwardProfile())["ANTHROPIC_DEFAULT_SONNET_MODEL"]
	if err := json.Unmarshal([]byte(configText), &codeConfig); err != nil || codeConfig.Env["CLAUDE_CODE_AUTO_MODE_MODEL"] != localproxy.ClassifierModelID(sonnet)+"[1m]" {
		t.Fatal("Desktop Sonnet change did not update the Code classifier", configText, err)
	}
	if len(a.forwardingModels(a.selectedForwardProfile())) != 1 {
		t.Fatal("classifier selection enabled ordinary sharing")
	}
	settingsClick(t, tester, agentconfig.ClaudeDesktop)
	settingsClick(t, tester, "Share github.com gpt-fixture")
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
	p.ClaudePermissionMode = "auto"
	p.CodexRemoteCompaction = true
	a.setForwardEnabled(p, 0, false)
	for _, agent := range []string{agentconfig.ClaudeCode, agentconfig.Codex} {
		config, err := a.agentConfiguration(p, agent)
		if err != nil || strings.Contains(config, "gpt-5.5 (2)") || !strings.Contains(config, "\"m365-copilot/gpt-5.5\"") {
			t.Fatal("model selection did not preserve provider prefix", agent, config, err)
		}
	}
	original := p.ID
	a.addForwardProfile(true)
	copy := a.selectedForwardProfile()
	copy.ClaudeDefaults["ANTHROPIC_DEFAULT_SONNET_MODEL"] = store.ForwardModelRef{ProviderID: store.DefaultProviderID, ModelID: "gpt-5.5"}
	copy.CodexRemoteCompaction = false
	copy.ClaudePermissionMode = "default"
	a.saveSettings()
	a.state = settingsSavedState(t, a)
	a.chooseForwardProfile(original)
	p = a.selectedForwardProfile()
	if p.ClaudeDefaults["ANTHROPIC_DEFAULT_SONNET_MODEL"].ProviderID != "work" || p.CodexModel.ProviderID != "work" || !p.CodexRemoteCompaction {
		t.Fatal("duplicated profile choices were shared or not persisted")
	}
	if p.ClaudePermissionMode != "auto" {
		t.Fatal("permission mode lost across profile duplication")
	}
	a.setForwardEnabled(p, 1, false)
	config, err := a.agentConfiguration(p, agentconfig.ClaudeCode)
	if err != nil || !strings.Contains(config, localproxy.DefaultModelID(a.claudeDefaults(p)["ANTHROPIC_DEFAULT_SONNET_MODEL"])+"[1m]") {
		t.Fatal("unshared default lost its account-bound private route", config, err)
	}
	if _, err := a.agentConfiguration(p, agentconfig.Codex); err == nil {
		t.Fatal("unshared Codex model silently replaced")
	}
	a.provider("work").Disabled = true
	for _, agent := range []string{agentconfig.ClaudeCode, agentconfig.Codex} {
		if _, err := a.agentConfiguration(p, agent); err == nil {
			t.Fatal("disabled account silently replaced", agent)
		}
	}
}

func TestAgentModelTableUsesPageScroll(t *testing.T) {
	a := newSettingsTestApp(t)
	a.settingsPage = "LLM Gateway"
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
	if !ok || !lastOK || last.Y-first.Y < 700 {
		t.Fatal("model table is not fully expanded", first, last)
	}
	config, _ := tester.Find("Configuration")
	if first.Y < config.Y {
		t.Fatal("model table precedes configuration")
	}
	label, _ := tester.Find("gpt-4o")
	if first.X < label.X+label.W {
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
