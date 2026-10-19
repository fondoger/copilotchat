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

// Allocate aliases once, in saved provider/catalog order. Keep absent models
// reserved so refresh, sign-out and re-enable cannot change an agent's routing.
func (a *app) reconcileForwardModels() {
	s := &a.state.Settings
	for i := range s.ForwardProfiles {
		a.reconcileProfileModels(&s.ForwardProfiles[i])
	}
}

func (a *app) reconcileProfileModels(profile *store.ForwardProfile) {
	profile.Models = slices.DeleteFunc(profile.Models, func(m store.ForwardModel) bool { return a.provider(m.ProviderID) == nil })
	seen, aliases := map[forwardModelKey]bool{}, map[string]bool{}
	for _, m := range profile.Models {
		seen[forwardModelKey{m.ProviderID, m.ModelID}], aliases[m.Alias] = true, true
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
			alias := m.ID
			for n := 2; aliases[alias]; n++ {
				alias = fmt.Sprintf("%s (%d)", m.ID, n)
			}
			profile.Models = append(profile.Models, store.ForwardModel{ProviderID: p.ID, ModelID: m.ID, Alias: alias, Enabled: true})
			seen[key], aliases[alias] = true, true
		}
	}
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
		if i != index && m.Alias == alias {
			return "This alias is already used by another model."
		}
	}
	profile.Models[index].Alias = alias
	a.saveSettings()
	return ""
}

func (a *app) forwardingModelSettings(c *ui.Context, profile *store.ForwardProfile) {
	t := c.Theme()
	ui.Text(c, "Shared models").FontWeight(500)
	ui.Text(c, "Choose this profile's models and aliases. Changes save automatically; other profiles stay independent.").FontSize(12).TextColor(t.TextMuted)
	ui.Row(c).Gap(8).Children(func() {
		for _, enabled := range []bool{true, false} {
			label := "Deselect all"
			if enabled {
				label = "Select all"
			}
			ui.Button(c, label).OnClick(func() {
				for i := range profile.Models {
					profile.Models[i].Enabled = enabled
				}
				a.saveSettings()
			})
		}
	})
	ui.Column(c).FillWidth().Border(1, t.Border).Radius(6).Children(func() {
		ui.Row(c).FillWidth().Gap(10).Padding(8).Background(t.Surface).Children(func() {
			ui.Text(c, "Enabled").Width(52).FontSize(11)
			ui.Text(c, "Provider / model").Grow(1).MinWidth(0).FontSize(11)
			ui.Text(c, "Alias").Grow(1).MinWidth(0).FontSize(11)
		})
		ui.Scroll(c.Key("forward-model-scroll")).MaxHeight(260).Children(func() {
			ui.Column(c).FillWidth().Children(func() {
				for i, m := range profile.Models {
					key := forwardModelKey{m.ProviderID, m.ModelID}
					if a.agentSetup.aliases == nil {
						a.agentSetup.aliases = map[forwardModelKey]*aliasDraft{}
					}
					draft := a.agentSetup.aliases[key]
					if draft == nil {
						draft = &aliasDraft{text: m.Alias}
						a.agentSetup.aliases[key] = draft
					}
					ui.Row(c.Key("forward-model-"+profile.ID+"-"+m.ProviderID+"-"+m.ModelID)).FillWidth().Gap(10).Padding(8).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
						enabled := m.Enabled
						ui.Row(c).Width(52).Shrink(0).Justify(ui.Center).Children(func() {
							ui.Checkbox(c, &enabled, "").Width(18).Label("Share " + m.ProviderID + " " + m.ModelID).OnChange(func() { profile.Models[i].Enabled = enabled; a.saveSettings() })
						})
						details := ui.Column(c).Grow(1).MinWidth(0).Gap(2)
						status := a.forwardingModelStatus(m)
						if !m.Enabled || status != "" {
							details.Opacity(.5)
						}
						details.Children(func() {
							ui.Text(c, a.provider(m.ProviderID).Name).FontSize(11).SingleLine()
							ui.Text(c, m.ModelID).FontSize(11).SingleLine().TextColor(t.TextMuted)
							if status != "" {
								ui.Text(c, status).FontSize(10).TextColor(t.TextMuted)
							}
						})
						ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
							ui.TextInput(c.Key("alias-"+profile.ID+"-"+m.ProviderID+"-"+m.ModelID), &draft.text).FillWidth().Background(t.Surface).Label("Alias " + m.ProviderID + " " + m.ModelID).OnChange(func() { draft.error = a.setForwardAlias(profile, i, draft.text) })
							if draft.error != "" {
								ui.Text(c, "Not saved: "+draft.error).FontSize(10).TextColor(t.TextMuted)
							}
						})
					})
				}
				if len(profile.Models) == 0 {
					ui.Text(c, "Sign in and load models in Providers first.").Padding(8).FontSize(12)
				}
			})
		})
	})
	ui.Text(c, "Claude Code discovers aliases containing ‘claude’ or ‘anthropic’. Other aliases can be selected explicitly below. Codex lists models supporting Responses.").FontSize(11).TextColor(t.TextMuted)
}
