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
	profileID, profileName, profileError  string
	renameID                              string
	renameOpen                            bool
	defaultsOpen, configOpen, optionsOpen bool
	notice, modelError                    string
	applying                              bool
}

type setupOption struct{ id, label string }

var modelDefaultInfoIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v6m0 4h.01"/></svg>`))

const classifierCostHint = "Safety checks are free with an official Claude subscription, but LLM Gateway calls may incur additional token charges. Manual mode avoids classifier calls by asking for approval; Bypass permissions also avoids them but disables this safety check."

var claudeModelDefaults = []struct{ key, label, hint string }{
	{"ANTHROPIC_DEFAULT_SONNET_MODEL", "Sonnet level balanced model", "Used for general-purpose subagents and the Auto mode security classifier.\n\n" + classifierCostHint},
	{"ANTHROPIC_DEFAULT_HAIKU_MODEL", "Haiku level fast model", "Used for web search and research subagents, and Auto mode security checks that request a Haiku level model."},
	{"CLAUDE_CODE_SUBAGENT_MODEL", "Subagent Model", "Recommended: Not set. Subagents select a Sonnet or Haiku level model based on the task. When set, this model is the fallback for subagent requests."},
}

func setupSelect(c *ui.Context, key, label string, value *string, options []setupOption, changed func()) {
	text := "Choose " + label
	for _, option := range options {
		if option.id == *value {
			text = option.label
		}
	}
	t := c.Theme()
	sel := ui.SelectBase(c.Key(key), value)
	sel.Trigger.FillWidth().Height(30).Padding(4, 8).Gap(6).Radius(5).Border(1, t.Border).Background(t.Surface).Label(label).Tooltip(text).Children(func() {
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
			model.ID, model.Name = m.ID, m.ID
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
		options = append(options, setupOption{id, m.ID})
		found = found || id == value
	}
	if !found {
		options = append(options, setupOption{value, ref.ModelID + " (unavailable)"})
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

// Resolve account-bound choices before generating public model IDs.
func (a *app) agentConfiguration(p *store.ForwardProfile, agent string) (string, error) {
	models := a.agentModels(p, agent)
	o := agentconfig.Options{Models: models, Agent: agent, BaseURL: localproxy.BaseURL(a.state.Settings.LocalPort, p.Name), Key: a.state.Settings.LocalKey, RemoteCompaction: p.CodexRemoteCompaction}
	if a.storage != nil {
		o.CatalogPath = a.storage.AgentCatalogPath(p.ID)
	}
	if agent == agentconfig.ClaudeCode || agent == agentconfig.ClaudeDesktop {
		o.ClaudeDefaults = a.claudeDefaults(p)
		keys := agentconfig.ClaudeModelVariables
		if agent == agentconfig.ClaudeDesktop {
			keys = nil
			for _, choice := range claudeModelDefaults {
				keys = append(keys, choice.key)
			}
		}
		for _, key := range keys {
			model := o.ClaudeDefaults[key]
			if model.Model.ID != "" && !agentconfig.Compatible(model.Model, agent) {
				return "", fmt.Errorf("%s is unavailable. Choose another model or Not set", key)
			}
		}
	}
	if agent == agentconfig.ClaudeCode {
		o.ClaudePermissionMode = p.ClaudePermissionMode
	} else if agent == agentconfig.Codex {
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
			return "", fmt.Errorf("Choose an enabled Messages or Responses model below for Codex")
		}
	}
	return agentconfig.Generate(o)
}

func (a *app) applyAgentConfiguration(agent, config string) {
	if a.agentSetup.applying {
		return
	}
	a.agentSetup.applying = true
	a.agentSetup.notice = "Applying configuration…"
	go func() {
		result, err := agentconfig.Apply(agent, config)
		a.update(func() {
			a.agentSetup.applying = false
			if err != nil {
				a.agentSetup.notice = "Apply failed: " + err.Error()
				return
			}
			restart := "Restart Claude Code CLI to use this configuration."
			if agent == agentconfig.ClaudeDesktop {
				restart = "Restart Claude Desktop to use this configuration."
			} else if agent == agentconfig.Codex {
				restart = "Restart Codex CLI or ChatGPT Desktop to use this configuration."
			}
			a.agentSetup.notice = "Applied to " + result.Path + ". " + restart
			if result.BackupPath != "" {
				a.agentSetup.notice += " Backup: " + result.BackupPath
			}
		})
	}()
}

func (a *app) llmGatewaySettings(c *ui.Context) {
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
	if agent != agentconfig.Codex && agent != agentconfig.ClaudeDesktop {
		agent = agentconfig.ClaudeCode
	}
	models := a.agentModels(p, agent)
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(10).Children(func() {
			ui.Text(c, "Agent").FontSize(12).FontWeight(500).Grow(1)
			ui.Row(c).Padding(2).Gap(2).Radius(6).Border(1, t.Border).Children(func() {
				for _, item := range []setupOption{{agentconfig.ClaudeCode, "Claude Code"}, {agentconfig.ClaudeDesktop, "Claude Desktop"}, {agentconfig.Codex, "Codex"}} {
					b := ui.ButtonBase(c.Key("agent-"+item.id)).Padding(5, 12).Radius(4).Label(item.id).OnClick(func() { p.Agent = item.id; s.notice = ""; a.saveSettings() })
					if item.id == agent {
						b.Background(t.Surface).Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, .1))
					}
					b.Children(func() { ui.Text(c, item.label).FontSize(12) })
				}
			})
		})
		if agent == agentconfig.ClaudeCode || agent == agentconfig.ClaudeDesktop {
			choices := a.claudeModelChoices(p)
			ui.Column(c).FillWidth().Children(func() {
				d := setupDisclosure(c, "model-defaults", "Model defaults", &s.defaultsOpen)
				d.Panel(func() {
					ui.Column(c).FillWidth().Gap(6).Children(func() {
						keys := agentconfig.ClaudeModelVariables
						if agent == agentconfig.ClaudeDesktop {
							keys = nil
							for _, choice := range claudeModelDefaults {
								keys = append(keys, choice.key)
							}
						}
						for _, key := range keys {
							label, hint := key, ""
							for _, choice := range claudeModelDefaults {
								if choice.key == key {
									hint = choice.hint
									if agent == agentconfig.ClaudeDesktop {
										label = choice.label
									}
								}
							}
							ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
								ui.Row(c).Width(0).Grow(1).MinWidth(0).Gap(5).AlignItems(ui.Center).Children(func() {
									if agent == agentconfig.ClaudeDesktop {
										ui.Text(c, label).FontSize(12).MinWidth(0)
									} else {
										prefix := "ANTHROPIC_DEFAULT_"
										if strings.HasPrefix(key, "CLAUDE_CODE_") {
											prefix = "CLAUDE_CODE_"
										}
										keyword := strings.TrimSuffix(strings.TrimPrefix(key, prefix), "_MODEL")
										ui.RichText(c, ui.Span{Text: prefix}, ui.Span{Text: keyword, Weight: 600}, ui.Span{Text: "_MODEL"}).FontSize(11).MinWidth(0)
									}
									if hint != "" {
										ui.Icon(c, modelDefaultInfoIcon).Size(14, 14).Shrink(0).TextColor(t.TextMuted).Label("About " + label).Tooltip(hint)
									}
								})
								ui.Column(c).Width(250).Shrink(0).Children(func() {
									a.agentModelSelect(c, p, "Choose "+label, p.ClaudeDefaults[key], choices, true, func(ref store.ForwardModelRef) {
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
					})
				})
			})
		}
		if agent == agentconfig.ClaudeCode {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Default permission mode").FontSize(12).Grow(1)
				ui.Column(c).Width(250).Children(func() {
					setupSelect(c, p.ID+"-permission-mode", "Choose default permission mode", &p.ClaudePermissionMode, []setupOption{
						{"", "Not set"}, {"default", "Manual"}, {"auto", "Auto"}, {"acceptEdits", "Accept edits"},
						{"plan", "Plan"}, {"dontAsk", "Don't ask"}, {"bypassPermissions", "Bypass permissions"},
					}, func() { s.notice = ""; a.saveSettings() })
				})
			})
		} else if agent == agentconfig.Codex {
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
		instructions := "Merge env, modelPicker and modelSettings into settings.json, and merge permissions.defaultMode without replacing other permission rules. Not set omits the mode; Apply removes any existing defaultMode. Restart Claude Code, then choose a model with /model."
		if agent == agentconfig.ClaudeDesktop {
			file = "Claude Desktop gateway configuration (JSON)"
			instructions = "Apply this JSON to Claude Desktop's gateway configuration, then restart Claude Desktop. Models are discovered from this profile's API."
		} else if agent == agentconfig.Codex {
			file = "~/.codex/config.toml"
			instructions = "Merge into config.toml. The model catalog is maintained automatically; restart Codex after model changes."
		}
		ui.Column(c).FillWidth().Gap(4).BorderWidth(1, 0, 1, 0).BorderColor(t.Border).PaddingY(8).Children(func() {
			var disclosure ui.CollapsibleParts
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				disclosure = setupDisclosure(c, "config-preview", "Configuration", &s.configOpen)
				disclosure.Trigger.Grow(1)
				ui.Button(c, "Copy configuration").Height(28).FontSize(12).Disabled(err != nil).OnClick(func() { mygo.Clipboard.WriteText(config); s.notice = "Configuration copied." })
				applyLabel := "Apply"
				if s.applying {
					applyLabel = "Applying…"
				}
				ui.Button(c, applyLabel).Height(28).FontSize(12).Label("Apply configuration").Disabled(err != nil || s.applying).OnClick(func() { a.applyAgentConfiguration(agent, config) })
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
