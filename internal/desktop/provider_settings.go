package desktop

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/copilot"
)

type providerForm struct{ Name, Host, Error string }

var providerAddIcon = loadIcon("tabler-plus")
var providerRemoveIcon = loadIcon("tabler-minus")

func (a *app) autosaveProviderForm(id string, form *providerForm) {
	p := a.provider(id)
	r := a.providerState(id)
	if p == nil || r.authBusy || r.credentialsBusy {
		return
	}
	host, err := copilot.NormalizeHost(form.Host)
	if err != nil {
		form.Error = err.Error()
		return
	}
	name := strings.TrimSpace(form.Name)
	if name == "" {
		form.Error = "Enter a provider name."
		return
	}
	if r.client != nil && host != p.Host {
		form.Error = "Sign out before changing this account's domain."
		return
	}
	if host != p.Host {
		// A failed sign-out must never move an old token to another domain.
		if err := a.clearProviderCredentials(id); err != nil {
			form.Error = "Could not remove the previous domain's saved sign-in: " + err.Error()
			return
		}
		p.Login, r.credentialNotice = "", ""
		p.ModelsRefreshedAt = time.Time{}
		r.models, r.modelsLoaded = nil, false
	}
	form.Error = ""
	if p.Name == name && p.Host == host {
		return
	}
	p.Name, p.Host = name, host
	a.saveSettings()
}

func (a *app) providersPage(c *ui.Context) {
	t := c.Theme()
	fill := ui.Hex("#ffffff")
	if t.Dark {
		fill = ui.Hex("#1b1b1e")
	}
	if a.provider(a.providerSettingsID) == nil && len(a.state.Settings.Providers) > 0 {
		a.providerSettingsID = a.state.Settings.Providers[0].ID
	}
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(200).Shrink(0).Padding(8).Gap(6).Background(fill).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Scroll(c).Grow(1).MinHeight(0).Gap(4).Children(func() {
				for _, provider := range a.state.Settings.Providers {
					row := ui.ButtonBase(c.Key(provider.ID)).FillWidth().Height(28).Padding(4, 8).Radius(6).Justify(ui.Start).Label("Provider " + provider.Name)
					if provider.ID == a.providerSettingsID {
						row.Background(t.SurfaceHover).TextColor(t.Accent)
					}
					if provider.Disabled {
						row.TextColor(t.TextMuted)
					}
					row.Children(func() {
						ui.Text(c, provider.Name).SingleLine().FontWeight(500).Grow(1).MinWidth(0)
					}).OnClick(func() { a.providerSettingsID = provider.ID })
				}
			})
			ui.Row(c).FillWidth().Gap(2).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Padding(6, 0, 0, 0).Children(func() {
				iconButton(c, providerAddIcon, "Add provider").OnClick(func() {
					_, _ = a.addProvider(fmt.Sprintf("GitHub Copilot %d", len(a.state.Settings.Providers)+1), "github.com")
				})
				selected := a.provider(a.providerSettingsID)
				canRemove := selected != nil
				if canRemove {
					canRemove = !a.providerState(selected.ID).credentialsBusy
				}
				remove := iconButton(c, providerRemoveIcon, "Remove provider").Disabled(!canRemove)
				if !canRemove {
					remove.TextColor(t.TextMuted).Opacity(.4)
				}
				remove.OnClick(func() { _ = a.removeProvider(a.providerSettingsID) })
			})
		})
		ui.Scroll(c.Key("provider-detail-" + a.providerSettingsID)).Grow(1).MinWidth(0).Padding(16).Gap(14).Children(func() { a.providerSettings(c) })
	})
}

func (a *app) providerSettings(c *ui.Context) {
	p := a.provider(a.providerSettingsID)
	if p == nil {
		ui.Text(c, "Add a provider to get started.").TextColor(c.Theme().TextMuted)
		return
	}
	id, r, t := p.ID, a.providerState(p.ID), c.Theme()
	form := ui.Local(c.Root(), "provider-form-"+id, func() providerForm { return providerForm{Name: p.Name, Host: p.Host} })
	ui.Row(c).FillWidth().Gap(12).Children(func() {
		ui.Text(c, "GitHub Copilot").FontSize(16).FontWeight(600).Grow(1)
		enabled := !p.Disabled
		ui.Switch(c, &enabled).Label("Enable provider").OnChange(func() { a.setProviderEnabled(id, enabled) })
	})
	ui.Text(c, "Use the models included with this GitHub Copilot account.").FontSize(12).TextColor(t.TextMuted)
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, "Provider name").FontWeight(500)
		ui.TextInput(c.Key("provider-name-"+id), &form.Name).FillWidth().Label("Provider name input").ReadOnly(r.authBusy || r.credentialsBusy).OnChange(func() { a.autosaveProviderForm(id, form) })
		ui.Text(c, "GitHub domain").FontWeight(500)
		ui.TextInput(c.Key("provider-host-"+id), &form.Host).FillWidth().Label("GitHub domain input").Placeholder("github.com or your-company.ghe.com").ReadOnly(r.client != nil || r.authBusy || r.credentialsBusy).OnChange(func() { a.autosaveProviderForm(id, form) })
		ui.Text(c, "github.com or a GitHub Enterprise Cloud domain ending in .ghe.com").FontSize(11).TextColor(t.TextMuted)
		if form.Error != "" {
			ui.Text(c, form.Error).FontSize(12).TextColor(t.Danger)
		}
	})
	if r.client != nil {
		identity := p.Host
		if p.Login != "" {
			identity += " · " + p.Login
		}
		connection, color := "Connected", t.Success
		if p.Disabled {
			connection, color = "Disabled", t.TextMuted
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			ui.Box(c).Size(7, 7).Radius(4).Background(color)
			ui.Text(c, connection+" · "+identity).TextColor(color).Grow(1).MinWidth(0)
			ui.Button(c, "Sign out").Disabled(r.credentialsBusy).OnClick(func() { a.signOutProvider(id) })
		})
	} else if !r.authBusy {
		ui.Row(c).Children(func() {
			ui.PrimaryButton(c, "Sign in with GitHub").Disabled(p.Disabled || form.Error != "" || r.credentialsBusy).OnClick(func() { a.beginProviderLogin(id) })
		})
	}
	if r.authBusy {
		if r.authCode.UserCode != "" {
			ui.Text(c, "Enter this code on "+p.Host+":").FontWeight(500)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				code := r.authCode.UserCode
				ui.TextInput(c.Key("device-code-"+id), &code).ReadOnly(true).Width(150).Font("Consolas").FontSize(18).Label("GitHub device code")
				ui.Button(c, "Copy code").OnClick(func() { mygo.Clipboard.WriteText(r.authCode.UserCode) })
			})
			ui.Row(c).Gap(8).Wrap().Children(func() {
				ui.PrimaryButton(c, "Open GitHub").OnClick(func() {
					if err := mygo.Shell.OpenExternal(r.authCode.VerificationURI); err != nil {
						r.status = "Could not open GitHub: " + err.Error()
					}
				})
				ui.Button(c, "Cancel").OnClick(func() { a.cancelProviderLogin(id); r.status = "Sign-in cancelled." })
			})
		} else {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, "Requesting a sign-in code…").Grow(1)
				ui.Button(c, "Cancel").OnClick(func() { a.cancelProviderLogin(id); r.status = "Sign-in cancelled." })
			})
		}
	}
	if r.status != "" {
		ui.Text(c, r.status).FontSize(12).TextColor(t.TextMuted).Selectable()
	}
	if r.credentialNotice != "" {
		ui.Text(c, r.credentialNotice).FontSize(12).TextColor(t.Danger).Selectable()
	}
	ui.Divider(c)
	ui.Text(c, "Default model").FontWeight(500)
	a.settingsModelSelect(c, "default-model", &a.state.Settings.Model, false).Disabled(p.Disabled || r.client == nil || len(a.providerModels(id)) == 0)
	ui.Row(c).Children(func() {
		label := "Refresh models"
		if r.modelsBusy {
			label = "Refreshing…"
		}
		ui.Button(c, label).Disabled(p.Disabled || r.client == nil || r.modelsBusy).OnClick(func() { a.refreshProviderModels(id) })
	})
	ui.Divider(c)
	ui.Text(c, "Model visibility").FontWeight(500)
	filter := map[string]string{"all": "All", "whitelist": "Whitelist", "blacklist": "Blacklist"}[p.ModelFilter]
	ui.Select(c.Key("model-filter-"+id), &filter, []string{"All", "Whitelist", "Blacklist"}).Label("Model visibility").OnChange(func() { p.ModelFilter = strings.ToLower(filter); a.saveSettings() })
	explanation := "All models appear in the model picker."
	if p.ModelFilter == "whitelist" {
		explanation = "Only checked models appear in the model picker."
	}
	if p.ModelFilter == "blacklist" {
		explanation = "Checked models are hidden from the model picker."
	}
	ui.Text(c, explanation).FontSize(12).TextColor(t.TextMuted)
	ui.Column(c).FillWidth().Border(1, t.Border).Radius(6).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Box(c).Width(18).Shrink(0)
			ui.Text(c, "Model / ID").FontSize(11).FontWeight(500).TextColor(t.TextMuted).Grow(1).MinWidth(0)
			ui.Text(c, "Visibility").FontSize(11).FontWeight(500).TextColor(t.TextMuted).Width(54).Shrink(0)
		})
		for index, model := range r.models {
			row := ui.Row(c.Key("model-filter-row-" + id + "-" + model.ID)).FillWidth().Gap(8).Padding(8)
			if !modelAllowed(p, model.ID) {
				row.Opacity(.48)
			}
			if index > 0 {
				row.BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
			}
			row.Children(func() {
				selected := slices.Contains(p.ModelIDs, model.ID)
				label := "Allow " + modelDisplayName(model)
				if p.ModelFilter == "blacklist" {
					label = "Hide " + modelDisplayName(model)
				}
				if p.ModelFilter == "all" {
					selected = true
				}
				ui.Checkbox(c, &selected, "").Width(18).Shrink(0).Label(label).Disabled(p.ModelFilter == "all").OnChange(func() {
					if selected && !slices.Contains(p.ModelIDs, model.ID) {
						p.ModelIDs = append(p.ModelIDs, model.ID)
					}
					if !selected {
						p.ModelIDs = slices.DeleteFunc(p.ModelIDs, func(value string) bool { return value == model.ID })
					}
					a.saveSettings()
				})
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, modelDisplayName(model)).SingleLine().FontSize(12).FontWeight(500)
					ui.Text(c, model.ID).SingleLine().FontSize(10).TextColor(t.TextMuted)
					ui.Row(c).Gap(4).Children(func() { modelCapabilityIcons(c, model) })
				})
				visibility, color := "Hidden", t.TextMuted
				if modelAllowed(p, model.ID) {
					visibility, color = "Shown", t.Success
				}
				ui.Text(c, visibility).FontSize(11).TextColor(color).Width(54).Shrink(0)
			})
		}
		if len(r.models) == 0 {
			ui.Text(c, "Connect and refresh models to populate this table.").Padding(8).FontSize(11).TextColor(t.TextMuted)
		}
	})
	ui.Text(c, "Each provider's sign-in is saved separately in the operating system credential store.").FontSize(12).TextColor(t.TextMuted)
}

type modelChoice struct{ ProviderID, ModelID string }

func (a *app) settingsModelSelect(c *ui.Context, key string, value *string, inherit bool) ui.Element {
	providerID := a.providerSettingsID
	if providerID == "" {
		providerID = a.state.Settings.ActiveProviderID
	}
	if inherit {
		providerID = a.state.Settings.TitleProviderID
	}
	choice := modelChoice{a.state.Settings.ActiveProviderID, *value}
	if inherit {
		choice.ProviderID = providerID
	}
	label := "Choose a model"
	if inherit && *value == "" {
		label = "Same as chat model"
	} else if *value != "" && choice.ProviderID == providerID {
		label = *value
		if model, ok := a.modelForProvider(providerID, *value); ok {
			label = modelDisplayName(model)
		}
	}
	sel := ui.SelectBase(c.Key(key), &choice)
	t := c.Theme()
	sel.Trigger.Height(30).MinWidth(180).MaxWidth(300).Padding(4, 8).Gap(8).Radius(6).Border(1, t.Border).Background(t.Surface).Label("Model").Children(func() {
		ui.Text(c, label).Grow(1).MinWidth(0).SingleLine()
		ui.Icon(c, settingsChevron).Size(14, 14).Shrink(0)
	})
	sel.Popup(func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0).Padding(4).Radius(8).Border(1, t.Border).Background(t.Background).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, .18)).MaxHeight(300).Width(320)
		ui.Scroll(c).MaxHeight(280).Children(func() {
			if inherit {
				sel.Item(modelChoice{}).Padding(6, 10).Children(func() { ui.Text(c, "Same as chat model") })
			}
			for _, p := range a.state.Settings.Providers {
				if !inherit && p.ID != providerID {
					continue
				}
				if a.clientForProvider(p.ID) == nil {
					continue
				}
				if inherit && a.enabledProviderCount() > 1 {
					ui.Text(c, p.Name).FontSize(11).TextColor(t.TextMuted).Padding(6, 10)
				}
				for _, model := range a.providerModels(p.ID) {
					item := sel.Item(modelChoice{p.ID, model.ID}).Padding(6, 10).Radius(6).Label("Select " + modelDisplayName(model) + " from " + p.Name)
					if item.Highlighted() {
						item.Background(t.SurfaceHover)
					}
					item.Children(func() { ui.Text(c, modelDisplayName(model)).SingleLine() })
				}
			}
		})
	})
	sel.Trigger.OnChange(func() {
		*value = choice.ModelID
		if inherit {
			a.state.Settings.TitleProviderID = choice.ProviderID
		} else {
			a.state.Settings.ActiveProviderID = choice.ProviderID
			a.state.Settings.ReasoningEffort = ""
			a.state.Settings.ThinkingBudgetTokens = 0
		}
		a.saveSettings()
	})
	return sel.Trigger
}
