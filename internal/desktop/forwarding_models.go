package desktop

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/agentconfig"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
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
		if p.Disabled || r == nil {
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
func (a *app) setForwardEnabled(profile *store.ForwardProfile, index int, enabled bool) {
	for i := range profile.Models {
		if index == -1 || i == index {
			profile.Models[i].Enabled = enabled
		}
	}
	a.agentSetup.modelError, a.agentSetup.notice = "", ""
	a.saveSettings()
}

func (a *app) forwardingModelStatus(m store.ForwardModel) string {
	p := a.provider(m.ProviderID)
	if p == nil || p.Disabled {
		return "Provider disabled"
	}
	if a.clientForProvider(m.ProviderID) == nil {
		return "Sign in to provider"
	}
	// Provider model visibility only controls the app's chat picker. Gateway
	// profiles keep independent selections from the complete provider catalog.
	if _, ok := a.modelForProvider(m.ProviderID, m.ModelID); !ok {
		return "Model unavailable"
	}
	return ""
}

func (a *app) forwardingModels(profile *store.ForwardProfile) []localproxy.Model {
	return a.profileForwardingModels(profile, true)
}

func (a *app) claudeModelChoices(profile *store.ForwardProfile) []localproxy.Model {
	models := a.profileForwardingModels(profile, false)
	return slices.DeleteFunc(models, func(m localproxy.Model) bool {
		return !agentconfig.Compatible(m.Model, agentconfig.ClaudeCode)
	})
}

func (a *app) profileForwardingModels(profile *store.ForwardProfile, enabledOnly bool) []localproxy.Model {
	var models []localproxy.Model
	if profile == nil {
		return nil
	}
	for _, m := range profile.Models {
		if (enabledOnly && !m.Enabled) || a.clientForProvider(m.ProviderID) == nil {
			continue
		}
		model, ok := a.modelForProvider(m.ProviderID, m.ModelID)
		if !ok {
			continue
		}
		models = append(models, localproxy.Model{ProviderID: m.ProviderID, Model: model})
	}
	identities := []localproxy.ProviderIdentity{}
	for _, p := range a.state.Settings.Providers {
		kind := p.Kind
		if kind == "" {
			kind = store.ProviderKindGitHub
		}
		identities = append(identities, localproxy.ProviderIdentity{ID: p.ID, Kind: kind, Host: p.Host, Name: p.Name, Login: p.Login, AccountID: a.providerState(p.ID).accountID})
	}
	return localproxy.NameModels(models, identities)
}

func claudeModelByRef(models []localproxy.Model, ref store.ForwardModelRef) localproxy.Model {
	for _, model := range models {
		if sharedModelRef(model) == ref {
			return model
		}
	}
	// Preserve the selected identity when offline, but no protocol capabilities:
	// future requests fail rather than silently using a different model/account.
	return localproxy.Model{ProviderID: ref.ProviderID, Model: provider.Model{ID: ref.ModelID}}
}

func (a *app) claudeDefaults(profile *store.ForwardProfile) map[string]localproxy.Model {
	models := a.claudeModelChoices(profile)
	defaults := map[string]localproxy.Model{}
	for _, key := range agentconfig.ClaudeModelVariables {
		if ref := profile.ClaudeDefaults[key]; ref.ModelID != "" {
			defaults[key] = claudeModelByRef(models, ref)
		}
	}
	return defaults
}

func (a *app) forwardingModelSettings(c *ui.Context, profile *store.ForwardProfile) {
	t, s := c.Theme(), &a.agentSetup
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
			enabled := 0
			for _, m := range profile.Models {
				if _, exists := a.modelForProvider(m.ProviderID, m.ModelID); exists && m.Enabled {
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
				}).OnClick(func() { a.setForwardEnabled(profile, -1, enabled) })
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
			hasModels := false
			for _, p := range a.state.Settings.Providers {
				if p.Disabled || !slices.ContainsFunc(profile.Models, func(m store.ForwardModel) bool {
					_, exists := a.modelForProvider(m.ProviderID, m.ModelID)
					return m.ProviderID == p.ID && exists
				}) {
					continue
				}
				hasModels = true
				ui.Text(c, p.Name).FillWidth().Padding(5, 8).FontSize(11).FontWeight(500).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
				for i, m := range profile.Models {
					if m.ProviderID != p.ID {
						continue
					}
					if _, exists := a.modelForProvider(m.ProviderID, m.ModelID); !exists {
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
								a.setForwardEnabled(profile, i, enabled)
							})
						})
					})
				}
			}
			if !hasModels {
				ui.Text(c, "Sign in and load models in Providers first.").Padding(8).FontSize(12)
			}
		})
		ui.Text(c, "Model IDs include their provider. Multiple accounts also include the account name.").FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, "Claude Code and Codex list Messages and Responses models.").FontSize(11).TextColor(t.TextMuted)
	})
}
