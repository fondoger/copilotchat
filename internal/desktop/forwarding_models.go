package desktop

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/store"
)

type forwardModelKey struct{ provider, model string }

// Preserve per-profile choices across catalog refreshes and sign-in changes.
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
			profile.Models = append(profile.Models, store.ForwardModel{ProviderID: p.ID, ModelID: m.ID, Enabled: true})
			seen[key] = true
		}
	}
}

// index -1 toggles all models; IDs are independent of selection.
func (a *app) setForwardEnabled(profile *store.ForwardProfile, index int, enabled bool) string {
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
		models = append(models, localproxy.Model{ProviderID: m.ProviderID, Model: model})
	}
	identities := []localproxy.ProviderIdentity{}
	for _, p := range a.state.Settings.Providers {
		kind := p.Kind
		if kind == "" {
			kind = store.ProviderKindGitHub
		}
		identities = append(identities, localproxy.ProviderIdentity{ID: p.ID, Kind: kind, Name: p.Name, Login: p.Login})
	}
	return localproxy.NameModels(models, identities)
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
		ui.Text(c, "Model IDs include their provider. Multiple accounts also include the account name.").FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, "Claude Code lists Messages and Responses models. Codex lists Responses models.").FontSize(11).TextColor(t.TextMuted)
	})
}
