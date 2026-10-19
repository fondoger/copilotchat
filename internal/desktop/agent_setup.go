package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/agentconfig"
	"github.com/fondoger/copilotchat/internal/apikeyhelper"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/provider/microsoft"
	"github.com/fondoger/copilotchat/internal/store"
)

type agentSetupState struct {
	providerID, agent, modelID string
	helperPath, notice         string
}

type setupOption struct{ id, label string }

func setupSelect(c *ui.Context, key, label string, value *string, options []setupOption, changed func()) {
	text := "Choose " + label
	for _, option := range options {
		if option.id == *value {
			text = option.label
		}
	}
	t := c.Theme()
	sel := ui.SelectBase(c.Key(key), value)
	sel.Trigger.FillWidth().Height(32).Padding(5, 8).Gap(8).Radius(6).Border(1, t.Border).Background(t.Surface).Label(label).Children(func() {
		ui.Text(c, text).Grow(1).MinWidth(0).SingleLine()
		ui.Icon(c, settingsChevron).Size(14, 14).Shrink(0)
	})
	sel.Popup(func(panel ui.Element) {
		panel.Padding(4).Radius(8).Background(t.Surface).Border(1, t.Border).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, .18)).Width(300).MaxHeight(280)
		ui.Scroll(c).MaxHeight(264).Children(func() {
			for _, option := range options {
				item := sel.Item(option.id).Padding(6, 10).Label("Select " + label + " " + option.label)
				if item.Highlighted() {
					item.Background(t.SurfaceHover)
				}
				item.Children(func() { ui.Text(c, option.label).SingleLine() })
			}
		})
	})
	sel.Trigger.OnChange(changed)
}

func bundledHelperPath() string {
	root, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return ""
	}
	name := apikeyhelper.Name
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, name)
}

func (a *app) agentSetupSettings(c *ui.Context) {
	s := &a.agentSetup
	if s.agent == "" {
		s.agent = agentconfig.ClaudeCode
	}
	if s.helperPath == "" {
		s.helperPath = bundledHelperPath()
	}
	ui.Text(c, "Use a Copilot Chat account in your coding agent.").TextColor(c.Theme().TextMuted)
	options := []setupOption{}
	for _, p := range a.state.Settings.Providers {
		if !p.Disabled {
			options = append(options, setupOption{p.ID, p.Name})
		}
	}
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, "Provider").FontWeight(500)
		setupSelect(c, "agent-provider", "Provider source", &s.providerID, options, func() { s.modelID, s.notice = "", "" })
	})
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, "Agent").FontWeight(500)
		setupSelect(c, "agent-client", "Agent application", &s.agent, []setupOption{{agentconfig.ClaudeCode, agentconfig.ClaudeCode}, {agentconfig.Codex, agentconfig.Codex}}, func() { s.modelID, s.notice = "", "" })
	})
	p := a.provider(s.providerID)
	if p == nil || p.Disabled {
		ui.Text(c, "Select an enabled provider to generate its configuration.").TextColor(c.Theme().TextMuted)
		return
	}
	r := a.providers[p.ID]
	if r == nil || r.client == nil {
		ui.Text(c, "Sign in to this provider in Providers first.").TextColor(c.Theme().TextMuted)
		return
	}
	models := []provider.Model{}
	for _, model := range a.providerModels(p.ID) {
		if agentconfig.Compatible(model, s.agent) {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		ui.Text(c, "No compatible models are loaded. Claude Code needs Messages; Codex needs Responses. A Messages-only model cannot be used directly in Codex.").TextColor(c.Theme().TextMuted)
		ui.Button(c, "Refresh models").Disabled(r.modelsBusy).OnClick(func() { a.refreshProviderModels(p.ID) })
		return
	}
	if !slices.ContainsFunc(models, func(m provider.Model) bool { return m.ID == s.modelID }) {
		s.modelID = models[0].ID
	}
	modelOptions := make([]setupOption, 0, len(models))
	var selected provider.Model
	for _, model := range models {
		modelOptions = append(modelOptions, setupOption{model.ID, modelDisplayName(model)})
		if model.ID == s.modelID {
			selected = model
		}
	}
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, "Model").FontWeight(500)
		setupSelect(c, "agent-model", "Agent model", &s.modelID, modelOptions, func() { s.notice = "" })
	})
	base := microsoft.BaseURL
	if client, ok := r.client.(*github.Client); ok {
		base = client.APIBaseURL()
	}
	dir, err := store.DataDir(a.dataDir)
	if err != nil {
		ui.Text(c, "Could not locate the application settings directory.")
		return
	}
	config, err := agentconfig.Generate(agentconfig.Options{Provider: *p, Model: selected, Agent: s.agent, Helper: s.helperPath, DataDir: dir, BaseURL: base, OS: runtime.GOOS})
	if err != nil {
		ui.Text(c, err.Error()).TextColor(c.Theme().TextMuted)
		return
	}
	info, helperErr := os.Stat(s.helperPath)
	helperReady := helperErr == nil && !info.IsDir()
	if !helperReady {
		ui.Text(c, "The bundled credential helper is missing. Reinstall the app, or build it with scripts/build.ps1.").TextColor(c.Theme().TextMuted)
	}
	file := "~/.claude/settings.json"
	instructions := "Merge these fields into your settings JSON. Remove conflicting ANTHROPIC_AUTH_TOKEN / ANTHROPIC_API_KEY values, then restart Claude Code. All model aliases below use the selected model."
	if s.agent == agentconfig.Codex {
		file = "~/.codex/config.toml"
		instructions = "Merge the top-level model keys and provider tables into config.toml; replace matching entries rather than duplicating them. Restart Codex or ChatGPT Desktop. Use a current client that supports provider auth commands."
	}
	ui.Text(c, file).FontWeight(500).Selectable()
	ui.Text(c, instructions).FontSize(12).TextColor(c.Theme().TextMuted)
	ui.Button(c, "Copy configuration").Disabled(!helperReady).OnClick(func() {
		mygo.Clipboard.WriteText(config)
		s.notice = "Configuration copied."
	})
	if s.notice != "" {
		ui.Text(c, s.notice).FontSize(12).TextColor(c.Theme().TextMuted)
	}
	ui.TextArea(c.Key("agent-config-"+p.ID+"-"+s.agent+"-"+s.modelID), &config).ReadOnly(true).Height(300).Font("Consolas").FontSize(12).Label("Generated agent configuration")
	ui.Text(c, "The helper works while Copilot Chat is closed. It reads the selected account from the system credential store; this configuration contains no token. If sign-in expires, reconnect in Providers. Regenerate this configuration if you move the app.").FontSize(12).TextColor(c.Theme().TextMuted)
}
