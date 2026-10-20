package desktop

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/store"
)

type forwardModelKey struct{ provider, model string }
type aliasDraft struct{ text, error string }

// Keep catalog order stable. Only profile selection changes automatic aliases;
// a temporary sign-out or missing catalog must not change public routes.
func (a *app) reconcileForwardModels() {
	s := &a.state.Settings
	for i := range s.ForwardProfiles {
		a.reconcileProfileModels(&s.ForwardProfiles[i])
	}
}

func (a *app) reconcileProfileModels(profile *store.ForwardProfile) {
	profile.Models = slices.DeleteFunc(profile.Models, func(m store.ForwardModel) bool { return a.provider(m.ProviderID) == nil })
	seen := map[forwardModelKey]bool{}
	for _, m := range profile.Models {
		seen[forwardModelKey{m.ProviderID, m.ModelID}] = true
	}
	for _, p := range a.state.Settings.Providers {
		r := a.providers[p.ID]
		if r == nil {
			continue
		}
		for _, m := range r.models {
			key := forwardModelKey{p.ID, m.ID}
			if m.ID == "" || seen[key] {
				continue
			}
			profile.Models = append(profile.Models, store.ForwardModel{ProviderID: p.ID, ModelID: m.ID, AliasMode: "auto", Enabled: true})
			seen[key] = true
		}
	}
	a.reconcileForwardAliases(profile)
}

func nextForwardAlias(base string, used map[string]bool) string {
	alias := base
	for n := 2; used[alias]; n++ {
		alias = fmt.Sprintf("%s (%d)", base, n)
	}
	return alias
}

func (a *app) reconcileForwardAliases(profile *store.ForwardProfile) {
	legacy, used := map[string]bool{}, map[string]bool{}
	for i := range profile.Models {
		m := &profile.Models[i]
		// Older profiles did not record edits. Recognize their original default
		// allocation and preserve any different saved name as a manual alias.
		if m.AliasMode == "" {
			m.AliasMode = "manual"
			if m.Alias == "" || m.Alias == nextForwardAlias(m.ModelID, legacy) {
				m.AliasMode = "auto"
			}
		}
		legacy[m.Alias] = true
		if m.Enabled && m.AliasMode == "manual" {
			used[m.Alias] = true
		}
	}
	for i := range profile.Models {
		m := &profile.Models[i]
		if m.AliasMode == "manual" {
			continue
		}
		alias := m.ModelID
		if m.Enabled {
			alias = nextForwardAlias(alias, used)
			used[alias] = true
		}
		m.Alias = alias
		if profile.ID == a.agentSetup.profileID {
			if draft := a.agentSetup.aliases[forwardModelKey{m.ProviderID, m.ModelID}]; draft != nil && draft.error == "" {
				draft.text = alias
			}
		}
	}
}

// index -1 toggles all models. Validate manual collisions atomically so a
// select-all action never silently renames an alias the user chose.
func (a *app) setForwardEnabled(profile *store.ForwardProfile, index int, enabled bool) string {
	used := map[string]bool{}
	for i, m := range profile.Models {
		selected := m.Enabled
		if index == -1 || i == index {
			selected = enabled
		}
		if selected && m.AliasMode == "manual" {
			if used[m.Alias] {
				return fmt.Sprintf("Alias %q is already used. Rename one of these models before enabling both.", m.Alias)
			}
			used[m.Alias] = true
		}
	}
	for i := range profile.Models {
		if index == -1 || i == index {
			profile.Models[i].Enabled = enabled
		}
	}
	a.saveSettings()
	return ""
}

func (a *app) forwardingModelStatus(m store.ForwardModel) string {
	p := a.provider(m.ProviderID)
	if p == nil || p.Disabled {
		return "Provider disabled"
	}
	if a.clientForProvider(m.ProviderID) == nil {
		return "Sign in to provider"
	}
	if !modelEnabled(p, m.ModelID) {
		return "Hidden in Providers"
	}
	if _, ok := a.modelForProvider(m.ProviderID, m.ModelID); !ok {
		return "Model unavailable"
	}
	return ""
}

func (a *app) forwardingModels(profile *store.ForwardProfile) []localproxy.Model {
	var models []localproxy.Model
	if profile == nil {
		return nil
	}
	for _, m := range profile.Models {
		if !m.Enabled || a.forwardingModelStatus(m) != "" {
			continue
		}
		model, _ := a.modelForProvider(m.ProviderID, m.ModelID)
		models = append(models, localproxy.Model{ProviderID: m.ProviderID, Alias: m.Alias, Model: model})
	}
	return models
}

func (a *app) setForwardAlias(profile *store.ForwardProfile, index int, alias string) string {
	if alias == "" || alias != strings.TrimSpace(alias) || utf8.RuneCountInString(alias) > 120 || strings.ContainsFunc(alias, unicode.IsControl) {
		return "Use 1–120 characters, without control characters or spaces at either end."
	}
	for i, m := range profile.Models {
		if i != index && m.Enabled && profile.Models[index].Enabled && m.Alias == alias {
			return "This alias is already used by another model."
		}
	}
	profile.Models[index].Alias = alias
	profile.Models[index].AliasMode = "manual"
	a.saveSettings()
	return ""
}

func (a *app) forwardingModelSettings(c *ui.Context, profile *store.ForwardProfile) {
	t, s := c.Theme(), &a.agentSetup
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
			enabled := 0
			for _, m := range profile.Models {
				if m.Enabled {
					enabled++
				}
			}
			ui.Text(c, fmt.Sprintf("Models · %d enabled", enabled)).FontSize(12).FontWeight(500).Grow(1)
			for _, enabled := range []bool{true, false} {
				label := "Deselect all"
				if enabled {
					label = "Select all"
				}
				ui.ButtonBase(c).Height(26).PaddingX(6).Radius(4).Label(label).Children(func() {
					ui.Text(c, label).FontSize(11).TextColor(t.TextMuted)
				}).OnClick(func() { s.modelError = a.setForwardEnabled(profile, -1, enabled); s.notice = "" })
			}
		})
		if s.modelError != "" {
			ui.Text(c, s.modelError).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Column(c).FillWidth().Border(1, t.Border).Radius(6).Children(func() {
			ui.Row(c).FillWidth().Gap(10).Padding(6, 8).Children(func() {
				ui.Text(c, "Model").Width(0).Grow(1).MinWidth(0).FontSize(11).TextColor(t.TextMuted)
				ui.Text(c, "Alias").Width(0).Grow(1).MinWidth(0).FontSize(11).TextColor(t.TextMuted)
				ui.Text(c, "Enabled").Width(48).FontSize(11).TextColor(t.TextMuted)
			})
			// One table in the page scroll; no nested Scroll or height limit.
			for _, p := range a.state.Settings.Providers {
				if !slices.ContainsFunc(profile.Models, func(m store.ForwardModel) bool { return m.ProviderID == p.ID }) {
					continue
				}
				ui.Text(c, p.Name).FillWidth().Padding(5, 8).FontSize(11).FontWeight(500).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
				for i, m := range profile.Models {
					if m.ProviderID != p.ID {
						continue
					}
					key := forwardModelKey{m.ProviderID, m.ModelID}
					if s.aliases == nil {
						s.aliases = map[forwardModelKey]*aliasDraft{}
					}
					draft := s.aliases[key]
					if draft == nil {
						draft = &aliasDraft{text: m.Alias}
						s.aliases[key] = draft
					}
					ui.Row(c.Key("forward-model-"+profile.ID+"-"+m.ProviderID+"-"+m.ModelID)).FillWidth().Gap(10).Padding(5, 8).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
						details := ui.Column(c).Width(0).Grow(1).MinWidth(0).Gap(2)
						status := a.forwardingModelStatus(m)
						if !m.Enabled || status != "" {
							details.Opacity(.5)
						}
						details.Children(func() {
							ui.Text(c, m.ModelID).FontSize(12).SingleLine().Tooltip(m.ModelID)
							if status != "" {
								ui.Text(c, status).FontSize(10).TextColor(t.TextMuted)
							}
						})
						ui.Column(c).Width(0).Grow(1).MinWidth(0).Gap(3).Children(func() {
							ui.TextInput(c.Key("alias-"+profile.ID+"-"+m.ProviderID+"-"+m.ModelID), &draft.text).FillWidth().Height(28).FontSize(12).Background(t.Surface).Label("Alias " + m.ProviderID + " " + m.ModelID).OnChange(func() {
								draft.error = a.setForwardAlias(profile, i, draft.text)
								s.modelError, s.notice = "", ""
							})
							if draft.error != "" {
								ui.Text(c, "Not saved: "+draft.error).FontSize(10).TextColor(t.TextMuted)
							}
						})
						enabled := m.Enabled
						ui.Row(c).Width(48).Shrink(0).Justify(ui.Center).Children(func() {
							ui.Checkbox(c, &enabled, "").Width(18).Label("Share " + m.ProviderID + " " + m.ModelID).OnChange(func() {
								s.modelError = a.setForwardEnabled(profile, i, enabled)
								s.notice = ""
							})
						})
					})
				}
			}
			if len(profile.Models) == 0 {
				ui.Text(c, "Sign in and load models in Providers first.").Padding(8).FontSize(12)
			}
		})
		ui.Text(c, "Automatic aliases renumber when models are unchecked. Edited aliases stay unchanged.").FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, "Claude Code discovers aliases containing ‘claude’ or ‘anthropic’. Codex lists Responses models.").FontSize(11).TextColor(t.TextMuted)
	})
}
