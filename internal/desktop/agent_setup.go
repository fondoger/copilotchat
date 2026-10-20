package desktop

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/agentconfig"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

type agentSetupState struct {
	portText                              string
	portInitialized                       bool
	aliases                               map[forwardModelKey]*aliasDraft
	profileID, profileName, profileError  string
	renameID                              string
	renameOpen                            bool
	defaultsOpen, configOpen, optionsOpen bool
	notice, modelError                    string
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
	sel.Trigger.FillWidth().Height(30).Padding(4, 8).Gap(6).Radius(5).Border(1, t.Border).Background(t.Surface).Label(label).Children(func() {
		ui.Text(c, text).FontSize(12).Grow(1).MinWidth(0).SingleLine()
		ui.Icon(c, settingsChevron).Size(12, 12).Shrink(0)
	})
	sel.Popup(func(panel ui.Element) {
		panel.Padding(4).Radius(8).Background(t.Surface).Border(1, t.Border).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, .18)).Width(300).MaxHeight(280)
		ui.Scroll(c).MaxHeight(264).Children(func() {
			for _, option := range options {
				item := sel.Item(option.id).Padding(6, 10).Label("Select " + label + " " + option.label)
				if item.Highlighted() {
					item.Background(t.SurfaceHover)
				}
				item.Children(func() { ui.Text(c, option.label).FontSize(12).SingleLine() })
			}
		})
	})
	sel.Trigger.OnChange(changed)
}

func setupDisclosure(c *ui.Context, key, label string, open *bool) ui.CollapsibleParts {
	p := ui.CollapsibleBase(c.Key(key), open)
	p.Trigger.Gap(5).Height(30).Radius(5).Label(label).Children(func() {
		ui.Icon(c, iChevron).Size(13, 13).Rotate(90 * p.Progress()).TextColor(c.Theme().TextMuted)
		ui.Text(c, label).FontSize(12).FontWeight(500)
	})
	return p
}

func forwardRefID(ref store.ForwardModelRef) string {
	if ref.ModelID == "" {
		return ""
	}
	return ref.ProviderID + "\x00" + ref.ModelID
}

func sharedModelRef(m localproxy.Model) store.ForwardModelRef {
	return store.ForwardModelRef{ProviderID: m.ProviderID, ModelID: m.Model.ID}
}

func (a *app) agentModels(p *store.ForwardProfile, agent string) []localproxy.Model {
	var models []localproxy.Model
	for _, m := range a.forwardingModels(p) {
		if agentconfig.Compatible(m.Model, agent) {
			models = append(models, m)
		}
	}
	return models
}

func agentModelByRef(models []localproxy.Model, ref store.ForwardModelRef) (provider.Model, bool) {
	for _, m := range models {
		if sharedModelRef(m) == ref {
			model := m.Model
			model.ID, model.Name = m.Alias, m.Alias
			return model, true
		}
	}
	return provider.Model{}, false
}

func (a *app) agentModelSelect(c *ui.Context, p *store.ForwardProfile, label string, ref store.ForwardModelRef, models []localproxy.Model, optional bool, changed func(store.ForwardModelRef)) {
	value := forwardRefID(ref)
	options := []setupOption{}
	if optional {
		options = append(options, setupOption{"", "Not set"})
	}
	found := ref.ModelID == ""
	for _, m := range models {
		id := forwardRefID(sharedModelRef(m))
		options = append(options, setupOption{id, m.Alias})
		found = found || id == value
	}
	if !found {
		alias := ref.ModelID
		for _, m := range p.Models {
			if m.ProviderID == ref.ProviderID && m.ModelID == ref.ModelID {
				alias = m.Alias
			}
		}
		options = append(options, setupOption{value, alias + " (unavailable)"})
	}
	setupSelect(c, p.ID+"-"+label, label, &value, options, func() {
		selected := ref
		if value == "" {
			selected = store.ForwardModelRef{}
		}
		for _, m := range models {
			if forwardRefID(sharedModelRef(m)) == value {
				selected = sharedModelRef(m)
			}
		}
		changed(selected)
		a.agentSetup.notice = ""
		a.saveSettings()
	})
}

// Generate from stable model references, never a previously displayed alias.
// An unavailable explicit choice is an error rather than silently routing it
// to another provider that has since inherited the same automatic alias.
func (a *app) agentConfiguration(p *store.ForwardProfile, agent string) (string, error) {
	models := a.agentModels(p, agent)
	o := agentconfig.Options{Agent: agent, BaseURL: localproxy.BaseURL(a.state.Settings.LocalPort, p.Name), Key: a.state.Settings.LocalKey, RemoteCompaction: p.CodexRemoteCompaction}
	if a.storage != nil {
		o.CatalogPath = a.storage.AgentCatalogPath(p.ID)
	}
	if agent == agentconfig.ClaudeCode {
		o.ClaudeDefaults = map[string]provider.Model{}
		for _, key := range agentconfig.ClaudeModelVariables {
			ref := p.ClaudeDefaults[key]
			if ref.ModelID == "" {
				continue
			}
			model, ok := agentModelByRef(models, ref)
			if !ok {
				return "", fmt.Errorf("%s is unavailable. Choose another model or Not set", key)
			}
			o.ClaudeDefaults[key] = model
		}
	} else {
		if err := a.forwardCatalogErrors[p.ID]; err != "" {
			return "", fmt.Errorf("%s", err)
		}
		ref := p.CodexModel
		if ref.ModelID == "" && len(models) > 0 {
			ref = sharedModelRef(models[0])
		}
		var ok bool
		o.Model, ok = agentModelByRef(models, ref)
		if !ok {
			return "", fmt.Errorf("Choose an enabled Responses model below for Codex")
		}
	}
	return agentconfig.Generate(o)
}

func (a *app) agentSetupSettings(c *ui.Context) {
	// The enclosing settings pane owns scrolling. Models remain fully expanded.
	ui.Column(c).FillWidth().Gap(14).Children(func() {
		a.forwardingSettings(c)
		a.reconcileForwardModels()
		a.forwardingProfileSettings(c)
		profile := a.selectedForwardProfile()
		if profile == nil {
			return
		}
		ui.Column(c.Key("profile-content-" + profile.ID)).FillWidth().Gap(12).Children(func() {
			a.agentConfigurationSettings(c, profile)
			a.forwardingModelSettings(c, profile)
		})
	})
}

func (a *app) agentConfigurationSettings(c *ui.Context, p *store.ForwardProfile) {
	s, t := &a.agentSetup, c.Theme()
	agent := p.Agent
	if agent != agentconfig.Codex {
		agent = agentconfig.ClaudeCode
	}
	models := a.agentModels(p, agent)
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(10).Children(func() {
			ui.Text(c, "Agent").FontSize(12).FontWeight(500).Grow(1)
			ui.Row(c).Padding(2).Gap(2).Radius(6).Border(1, t.Border).Children(func() {
				for _, item := range []setupOption{{agentconfig.ClaudeCode, "Claude Code"}, {agentconfig.Codex, "Codex"}} {
					b := ui.ButtonBase(c.Key("agent-"+item.id)).Padding(5, 12).Radius(4).Label(item.id).OnClick(func() { p.Agent = item.id; s.notice = ""; a.saveSettings() })
					if item.id == agent {
						b.Background(t.Surface).Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, .1))
					}
					b.Children(func() { ui.Text(c, item.label).FontSize(12) })
				}
			})
		})
		if agent == agentconfig.ClaudeCode {
			ui.Column(c).FillWidth().Children(func() {
				d := setupDisclosure(c, "model-defaults", "Model defaults", &s.defaultsOpen)
				d.Panel(func() {
					ui.Column(c).FillWidth().Gap(6).Children(func() {
						for _, key := range agentconfig.ClaudeModelVariables {
							prefix := "ANTHROPIC_DEFAULT_"
							if key == "CLAUDE_CODE_SUBAGENT_MODEL" {
								prefix = "CLAUDE_CODE_"
							}
							keyword := strings.TrimSuffix(strings.TrimPrefix(key, prefix), "_MODEL")
							ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
								ui.RichText(c, ui.Span{Text: prefix}, ui.Span{Text: keyword, Weight: 600}, ui.Span{Text: "_MODEL"}).FontSize(11).Grow(1).MinWidth(0)
								ui.Column(c).Width(174).Shrink(0).Children(func() {
									a.agentModelSelect(c, p, "Choose "+key, p.ClaudeDefaults[key], models, true, func(ref store.ForwardModelRef) {
										if p.ClaudeDefaults == nil {
											p.ClaudeDefaults = map[string]store.ForwardModelRef{}
										}
										if ref.ModelID == "" {
											delete(p.ClaudeDefaults, key)
										} else {
											p.ClaudeDefaults[key] = ref
										}
									})
								})
							})
						}
						ui.Text(c, "Optional. Not set leaves the variable out of your configuration.").FontSize(11).TextColor(t.TextMuted)
					})
				})
			})
		} else {
			ref := p.CodexModel
			if ref.ModelID == "" && len(models) > 0 {
				ref = sharedModelRef(models[0])
			}
			ui.Row(c).FillWidth().Gap(12).Children(func() {
				ui.Text(c, "Default model").FontSize(12).Grow(1)
				ui.Column(c).Width(220).Children(func() {
					a.agentModelSelect(c, p, "Codex model", ref, models, false, func(ref store.ForwardModelRef) { p.CodexModel = ref })
				})
			})
			ui.Row(c).FillWidth().Gap(12).Children(func() {
				ui.Text(c, "Remote compaction").FontSize(12).Grow(1)
				ui.Switch(c.Key("remote-compaction"), &p.CodexRemoteCompaction).Label("Enable remote compaction").OnChange(func() { s.notice = ""; a.saveSettings() })
			})
			ui.Text(c, "Uses provider name OpenAI when enabled; Copilot Chat Proxy otherwise.").FontSize(11).TextColor(t.TextMuted)
		}
		config, err := a.agentConfiguration(p, agent)
		file := "~/.claude/settings.json"
		instructions := "Merge the env fields, then restart Claude Code. Choose a model with /model."
		if agent == agentconfig.Codex {
			file = "~/.codex/config.toml"
			instructions = "Merge into config.toml. The model catalog is maintained automatically; restart Codex after model changes."
		}
		ui.Column(c).FillWidth().Gap(4).BorderWidth(1, 0, 1, 0).BorderColor(t.Border).PaddingY(8).Children(func() {
			var disclosure ui.CollapsibleParts
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				disclosure = setupDisclosure(c, "config-preview", "Configuration", &s.configOpen)
				disclosure.Trigger.Grow(1)
				ui.Button(c, "Copy configuration").Height(28).FontSize(12).Disabled(err != nil).OnClick(func() { mygo.Clipboard.WriteText(config); s.notice = "Configuration copied." })
			})
			ui.Text(c, file).FontSize(11).Selectable().TextColor(t.TextMuted)
			if err != nil {
				ui.Text(c, err.Error()).FontSize(11).TextColor(t.TextMuted)
			} else {
				disclosure.Panel(func() {
					ui.Text(c, instructions).FontSize(11).TextColor(t.TextMuted)
					// Text grows with the configuration; it introduces no nested scroll.
					ui.Text(c.Key("agent-config"), config).Font("monospace").FontSize(11).Selectable().Label("Generated agent configuration").FillWidth().Padding(10).Radius(5).Background(t.Surface)
				})
			}
			if s.notice != "" {
				ui.Text(c, s.notice).FontSize(11).TextColor(t.TextMuted)
			}
		})
	})
}
