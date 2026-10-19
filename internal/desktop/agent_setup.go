package desktop

import (
	"slices"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/agentconfig"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
)

type agentSetupState struct {
	portText                   string
	portInitialized            bool
	providerID, agent, modelID string
	notice                     string
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

func (a *app) agentSetupSettings(c *ui.Context) {
	s := &a.agentSetup
	if s.agent == "" {
		s.agent = agentconfig.ClaudeCode
	}
	a.forwardingSettings(c)
	if a.forwarder == nil {
		return
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
	if len(models) == 0 && s.agent != agentconfig.ClaudeCode {
		ui.Text(c, "No compatible models are loaded. Claude Code needs Messages; Codex needs Responses. A Messages-only model cannot be used directly in Codex.").TextColor(c.Theme().TextMuted)
		ui.Button(c, "Refresh models").Disabled(r.modelsBusy).OnClick(func() { a.refreshProviderModels(p.ID) })
		return
	}
	if !slices.ContainsFunc(models, func(m provider.Model) bool { return m.ID == s.modelID }) {
		s.modelID = ""
		if s.agent != agentconfig.ClaudeCode {
			s.modelID = models[0].ID
		}
	}
	modelOptions := make([]setupOption, 0, len(models))
	if s.agent == agentconfig.ClaudeCode {
		modelOptions = append(modelOptions, setupOption{"", "Choose in Claude Code"})
	}
	var selected provider.Model
	for _, model := range models {
		modelOptions = append(modelOptions, setupOption{model.ID, modelDisplayName(model)})
		if model.ID == s.modelID {
			selected = model
		}
	}
	ui.Column(c).Gap(6).Children(func() {
		label := "Model"
		if s.agent == agentconfig.ClaudeCode {
			label = "Model (optional)"
		}
		ui.Text(c, label).FontWeight(500)
		setupSelect(c, "agent-model", "Agent model", &s.modelID, modelOptions, func() { s.notice = "" })
	})
	base := localproxy.BaseURL(a.state.Settings.LocalPort, p.ID)
	config, err := agentconfig.Generate(agentconfig.Options{Provider: *p, Model: selected, Agent: s.agent, BaseURL: base, Key: a.state.Settings.LocalKey})
	if err != nil {
		ui.Text(c, err.Error()).TextColor(c.Theme().TextMuted)
		return
	}
	file := "~/.claude/settings.json"
	instructions := "Merge these env fields into your settings JSON. Remove the old apiKeyHelper, custom headers and conflicting API keys or model aliases. Restart Claude Code, then choose a model with /model."
	if s.agent == agentconfig.Codex {
		file = "~/.codex/config.toml"
		instructions = "Merge the top-level model keys and provider tables into config.toml; replace matching entries rather than duplicating them. Remove the old provider auth command, env_key and custom headers. Restart Codex or ChatGPT Desktop."
	}
	ui.Text(c, file).FontWeight(500).Selectable()
	ui.Text(c, instructions).FontSize(12).TextColor(c.Theme().TextMuted)
	if s.agent == agentconfig.ClaudeCode && s.modelID == "" && len(models) > 0 {
		ui.Text(c, "For example: /model "+models[0].ID).FontSize(12).Selectable()
	}
	ui.Button(c, "Copy configuration").OnClick(func() {
		mygo.Clipboard.WriteText(config)
		s.notice = "Configuration copied."
	})
	if s.notice != "" {
		ui.Text(c, s.notice).FontSize(12).TextColor(c.Theme().TextMuted)
	}
	ui.TextArea(c.Key("agent-config-"+p.ID+"-"+s.agent+"-"+s.modelID), &config).ReadOnly(true).Height(230).Font("Consolas").FontSize(12).Label("Generated agent configuration")
	ui.Text(c, "Keep Copilot Chat running. This fixed local key authorizes enabled providers through the loopback endpoint; it is not your upstream account token. Reconnect expired accounts in Providers.").FontSize(12).TextColor(c.Theme().TextMuted)
}
