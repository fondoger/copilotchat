package desktop

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/store"
	"github.com/fondoger/copilotchat/internal/wam"
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
	host := p.Host
	if web, ok := store.WebProviderFor(p.Kind); ok {
		host = web.Host
	} else if store.IsGitHubProvider(p.Kind) {
		var err error
		host, err = github.NormalizeHost(form.Host)
		if err != nil {
			form.Error = err.Error()
			return
		}
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
		a.invalidateProviderCatalog(id)
		p.Login, r.credentialNotice = "", ""
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
					row := ui.ButtonBase(c.Key(provider.ID)).FillWidth().Height(28).Padding(4, 8).Gap(7).Radius(6).Justify(ui.Start).Label("Provider " + provider.Name)
					if provider.ID == a.providerSettingsID {
						row.Background(t.SurfaceHover).TextColor(t.Accent)
					}
					if provider.Disabled {
						row.TextColor(t.TextMuted)
					}
					row.Children(func() {
						icon := providerIcon(c, provider.Kind, 16)
						if provider.Disabled {
							icon.Opacity(.5)
						}
						ui.Text(c, provider.Name).SingleLine().FontWeight(500).Grow(1).MinWidth(0)
					}).OnClick(func() { a.providerSettingsID = provider.ID })
				}
			})
			ui.Row(c).FillWidth().Gap(2).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Padding(6, 0, 0, 0).Children(func() {
				a.addProviderButton(c)
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
		selected := a.provider(a.providerSettingsID)
		if selected != nil && store.IsWebProvider(selected.Kind) {
			ui.Column(c.Key("provider-detail-" + selected.ID)).Grow(1).MinWidth(0).MinHeight(0).Padding(16).Gap(14).Children(func() { a.providerSettings(c) })
		} else {
			a.closeWebSettings()
			ui.Scroll(c.Key("provider-detail-" + a.providerSettingsID)).Grow(1).MinWidth(0).Padding(16).Gap(14).Children(func() { a.providerSettings(c) })
		}
	})
}

func (a *app) addProviderButton(c *ui.Context) {
	open := ui.Local(c.Root(), "add-provider-menu", func() bool { return false })
	trigger := iconButton(c, providerAddIcon, "Add provider").OnClick(func() { *open = !*open })
	t := c.Theme()
	ui.PopoverBase(c.Key("add-provider-popup"), trigger, open, func(panel ui.Element) {
		_, height := c.Size()
		limit := min(float32(450), height-70)
		panel.Width(244).MaxHeight(limit+8).Margin(4, 0, 0, 0).Padding(4).Radius(8).Border(1, t.Border).Background(t.Background).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, .18))
		kinds := []string{store.ProviderKindGitHub}
		for _, entry := range a.installedPlugins {
			kinds = append(kinds, store.PluginKindPrefix+entry.ID)
		}
		kinds = append(kinds, store.ProviderKindDoubao, store.ProviderKindDeepSeekWeb, store.ProviderKindClaudeWeb, store.ProviderKindGeminiWeb, store.ProviderKindChatGPTWeb)
		ui.Scroll(c).MaxHeight(limit).Gap(2).Children(func() {
			for _, kind := range kinds {
				name := a.providerName(kind)
				item := ui.ButtonBase(c.Key(kind)).FillWidth().Height(34).Padding(6, 8).Gap(8).Radius(5).Justify(ui.Start).Label("Add " + name)
				if item.Hovered() {
					item.Background(t.SurfaceHover)
				}
				item.Children(func() {
					providerIcon(c, kind, 18)
					ui.Text(c, name).FontSize(12).SingleLine()
				}).OnClick(func() {
					count := 0
					for _, p := range a.state.Settings.Providers {
						if p.Kind == kind || (p.Kind == "" && kind == store.ProviderKindGitHub) {
							count++
						}
					}
					if count > 0 {
						name = fmt.Sprintf("%s %d", name, count+1)
					}
					_, _ = a.addProviderKind(kind, name, "")
					*open = false
				})
			}
		})
	})
}

type registrationForm struct{ clientID, resource, origins string }

func registrationDraft(grant *wam.Profile) registrationForm {
	if grant == nil {
		return registrationForm{}
	}
	return registrationForm{grant.ClientID, grant.Resource, strings.Join(grant.Origins, "\n")}
}

func (f registrationForm) grant() *wam.Profile {
	g := &wam.Profile{ClientID: strings.TrimSpace(f.clientID), Resource: strings.TrimSpace(f.resource)}
	for line := range strings.SplitSeq(f.origins, "\n") {
		if origin := strings.TrimSpace(line); origin != "" {
			g.Origins = append(g.Origins, origin)
		}
	}
	return g
}

// Only the operator's saved grant populates these fields. Manifest values are
// shown separately for comparison. Typing never touches settings or the vault.
func (a *app) registrationSettings(c *ui.Context, p *store.Provider, r *providerRuntime) bool {
	requested := r.plugin != nil && r.plugin.Manifest.WAMResource != ""
	if !requested && p.WAMGrant == nil {
		return false
	}
	saved := registrationDraft(p.WAMGrant)
	draft := ui.Local(c.Root(), "registration-"+p.ID, func() registrationForm { return saved })
	apply := func(grant *wam.Profile) {
		if err := a.setPluginGrant(p.ID, grant); err != nil {
			r.status = err.Error()
			return
		}
		*draft = registrationDraft(grant)
		if grant != nil {
			r.status = "Registration saved."
		}
	}
	busy := r.authBusy || r.credentialsBusy
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, "Windows sign-in registration").FontWeight(600)
		if requested {
			ui.Text(c, "Plugin request: "+r.plugin.Manifest.WAMResource+"\n"+strings.Join(r.plugin.Manifest.Network, "\n")).FontSize(12).TextColor(c.Theme().TextMuted).Selectable()
			ui.Text(c, "Enter the registration supplied by your service administrator.").FontSize(12)
			ui.Text(c, "Application (client) ID")
			ui.TextInput(c.Key("wam-client-"+p.ID), &draft.clientID).FillWidth().Label("Client ID").ReadOnly(busy)
			ui.Text(c, "Resource (audience) ID")
			ui.TextInput(c.Key("wam-resource-"+p.ID), &draft.resource).FillWidth().Label("Resource ID").ReadOnly(busy)
			ui.Text(c, "Trusted HTTPS origins (one per line)")
			ui.TextArea(c.Key("wam-origins-"+p.ID), &draft.origins).FillWidth().Lines(2, 4).Label("Trusted HTTPS origins").ReadOnly(busy)
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if requested {
				ui.Button(c, "Save registration").Disabled(busy || *draft == saved).OnClick(func() {
					apply(draft.grant())
				})
			}
			if p.WAMGrant != nil {
				ui.Button(c, "Clear registration").Disabled(r.credentialsBusy).OnClick(func() { apply(nil) })
			}
		})
	})
	return requested && *draft == saved && r.plugin.ValidateGrant(p.WAMGrant) == nil
}

func (a *app) providerSettings(c *ui.Context) {
	p := a.provider(a.providerSettingsID)
	if p == nil {
		ui.Text(c, "Add a provider to get started.").TextColor(c.Theme().TextMuted)
		return
	}
	id, r, t := p.ID, a.providerState(p.ID), c.Theme()
	githubProvider := store.IsGitHubProvider(p.Kind)
	providerName := a.providerName(p.Kind)
	form := ui.Local(c.Root(), "provider-form-"+id, func() providerForm { return providerForm{Name: p.Name, Host: p.Host} })
	ui.Row(c).FillWidth().Gap(12).Children(func() {
		providerIcon(c, p.Kind, 20)
		ui.Text(c, providerName).FontSize(16).FontWeight(600).Grow(1).MinWidth(0)
		enabled := !p.Disabled
		ui.Switch(c, &enabled).Label("Enable provider").OnChange(func() { a.setProviderEnabled(id, enabled) })
	})
	if store.IsWebProvider(p.Kind) {
		ui.Column(c).FillWidth().Shrink(0).Gap(8).Children(func() {
			ui.Text(c, "Provider name").FontWeight(500)
			ui.TextInput(c.Key("provider-name-"+id), &form.Name).FillWidth().Label("Provider name input").OnChange(func() { a.autosaveProviderForm(id, form) })
			if form.Error != "" {
				ui.Text(c, form.Error).FontSize(12).TextColor(t.Danger)
			}
		})
		if supportsWebAPIMode(p.Kind) {
			apiMode := p.ExperimentalAPIMode
			ui.Row(c).FillWidth().Gap(12).Children(func() {
				ui.Text(c, "Experimental API Mode").Grow(1).FontWeight(500)
				ui.Switch(c, &apiMode).Label("Experimental API Mode").OnChange(func() { a.setExperimentalAPIMode(id, apiMode) })
			})
			if p.ExperimentalAPIMode {
				if a.webAPILoginID != id {
					a.closeWebSettings()
					ui.Button(c, "Open "+strings.TrimSuffix(providerName, " Web")).Disabled(p.Disabled).OnClick(func() { a.openWebAPILogin(id) })
					return
				}
				ui.Button(c, "Done").OnClick(a.finishWebAPILogin)
			}
		}
		a.webProviderView(c, id)
		return
	}
	if githubProvider {
		ui.Text(c, "Use the models included with this GitHub Copilot account.").FontSize(12).TextColor(t.TextMuted)
	}
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, "Provider name").FontWeight(500)
		ui.TextInput(c.Key("provider-name-"+id), &form.Name).FillWidth().Label("Provider name input").ReadOnly(r.authBusy || r.credentialsBusy).OnChange(func() { a.autosaveProviderForm(id, form) })
		if githubProvider {
			ui.Text(c, "GitHub domain").FontWeight(500)
			ui.TextInput(c.Key("provider-host-"+id), &form.Host).FillWidth().Label("GitHub domain input").Placeholder("github.com or your-company.ghe.com").ReadOnly(r.client != nil || r.authBusy || r.credentialsBusy).OnChange(func() { a.autosaveProviderForm(id, form) })
			ui.Text(c, "github.com or a GitHub Enterprise Cloud domain ending in .ghe.com").FontSize(11).TextColor(t.TextMuted)
		}
		if form.Error != "" {
			ui.Text(c, form.Error).FontSize(12).TextColor(t.Danger)
		}
	})
	registrationReady := false
	if r.client == nil && store.PluginID(p.Kind) != "" {
		registrationReady = a.registrationSettings(c, p, r)
	}
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
			if githubProvider || (r.plugin != nil && r.plugin.Manifest.WAMResource != "") {
				ui.Button(c, "Sign out").Disabled(r.credentialsBusy).OnClick(func() { a.signOutProvider(id) })
			}
		})
	} else if !r.authBusy {
		if githubProvider {
			ui.PrimaryButton(c, "Sign in with GitHub").Disabled(p.Disabled || form.Error != "" || r.credentialsBusy).OnClick(func() { a.beginProviderLogin(id) })
		} else if r.plugin != nil && r.plugin.Manifest.WAMResource != "" {
			ui.PrimaryButton(c, "Sign in").Disabled(!registrationReady || p.Disabled || r.credentialsBusy || runtime.GOOS != "windows").OnClick(func() { a.beginProviderLogin(id) })
			if runtime.GOOS != "windows" {
				ui.Text(c, "This plugin requires Windows account sign-in.").FontSize(12).TextColor(t.TextMuted)
			}
		}
		if store.PluginID(p.Kind) != "" && (r.plugin == nil || r.credentialNotice != "") {
			ui.Button(c, "Forget saved sign-in").Disabled(r.credentialsBusy).OnClick(func() { a.signOutProvider(id) })
		}
	}
	if r.authBusy {
		if githubProvider && r.authCode.UserCode != "" {
			codeSite, codeLabel, openLabel := p.Host, "GitHub device code", "Open GitHub"
			ui.Text(c, "Enter this code on "+codeSite+":").FontWeight(500)
			ui.Row(c).Gap(8).Wrap().Children(func() {
				code := r.authCode.UserCode
				ui.TextInput(c.Key("device-code-"+id), &code).ReadOnly(true).Width(150).Font("Consolas").FontSize(18).Label(codeLabel)
				ui.Button(c, "Copy code").OnClick(func() { mygo.Clipboard.WriteText(r.authCode.UserCode) })
			})
			ui.Row(c).Gap(8).Wrap().Children(func() {
				ui.PrimaryButton(c, openLabel).OnClick(func() {
					if err := mygo.Shell.OpenExternal(r.authCode.VerificationURI); err != nil {
						r.status = "Could not open the sign-in page: " + err.Error()
					}
				})
				ui.Button(c, "Cancel").OnClick(func() { a.cancelProviderLogin(id); r.status = "Sign-in cancelled." })
			})
		} else {
			ui.Row(c).Gap(8).Children(func() {
				waiting := r.status
				if waiting == "" {
					waiting = "Requesting a sign-in code…"
					if !githubProvider {
						waiting = "Waiting for account sign-in…"
					}
				}
				ui.Text(c, waiting).Grow(1).MinWidth(0)
				ui.Button(c, "Cancel").OnClick(func() { a.cancelProviderLogin(id); r.status = "Sign-in cancelled." })
			})
		}
	}
	if r.status != "" && (!r.authBusy || r.authCode.UserCode != "") {
		ui.Text(c, r.status).FontSize(12).TextColor(t.TextMuted).Selectable()
	}
	if r.credentialNotice != "" {
		ui.Text(c, r.credentialNotice).FontSize(12).TextColor(t.Danger).Selectable()
	}
	ui.Divider(c)
	ui.Row(c).Children(func() {
		label := "Refresh models"
		if r.modelsBusy {
			label = "Refreshing…"
		}
		ui.Button(c, label).Disabled(p.Disabled || r.client == nil || r.modelsBusy).OnClick(func() { a.refreshProviderModels(id) })
	})
	ui.Divider(c)
	ui.Text(c, "Model visibility").FontWeight(500)
	ui.Text(c, "Checked models appear in the model picker.").FontSize(12).TextColor(t.TextMuted)
	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		ui.Button(c, "Select all").Disabled(len(r.models) == 0).OnClick(func() { a.setAllModelsEnabled(id, true) })
		ui.Button(c, "Deselect all").Disabled(len(r.models) == 0).OnClick(func() { a.setAllModelsEnabled(id, false) })
	})
	ui.Column(c).FillWidth().Border(1, t.Border).Radius(6).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "Model / ID").FontSize(11).FontWeight(500).TextColor(t.TextMuted).Grow(1).MinWidth(0)
			ui.Row(c).Width(60).Shrink(0).Justify(ui.Center).Children(func() {
				ui.Text(c, "Enabled").FontSize(11).FontWeight(500).TextColor(t.TextMuted)
			})
		})
		for index, model := range r.models {
			row := ui.Row(c.Key("model-filter-row-" + id + "-" + model.ID)).FillWidth().Gap(8).Padding(8)
			if index > 0 {
				row.BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
			}
			row.Children(func() {
				selected := modelEnabled(p, model.ID)
				details := ui.Column(c).Grow(1).MinWidth(0).Gap(2)
				if !selected {
					details.Opacity(.48)
				}
				details.Children(func() {
					ui.Text(c, modelDisplayName(model)).SingleLine().FontSize(12).FontWeight(500)
					ui.Text(c, model.ID).SingleLine().FontSize(10).TextColor(t.TextMuted)
					ui.Row(c).Gap(4).Children(func() { modelCapabilityIcons(c, model) })
				})
				ui.Row(c).Width(60).Shrink(0).Justify(ui.Center).Children(func() {
					ui.Checkbox(c, &selected, "").Width(18).Shrink(0).Label("Enable " + modelDisplayName(model)).OnChange(func() {
						a.setModelEnabled(id, model.ID, selected)
					})
				})
			})
		}
		if len(r.models) == 0 {
			ui.Text(c, "Connect and refresh models to populate this table.").Padding(8).FontSize(11).TextColor(t.TextMuted)
		}
	})
	ui.Text(c, "Each provider's sign-in is saved separately in the operating system credential store.").FontSize(12).TextColor(t.TextMuted)
}

type modelChoice struct{ ProviderID, ModelID string }

func (a *app) titleModelSelect(c *ui.Context) ui.Element {
	choice := modelChoice{a.state.Settings.TitleProviderID, a.state.Settings.TitleModel}
	label := "Same as chat model"
	if choice.ModelID != "" {
		label = choice.ModelID
		if model, ok := a.modelForProvider(choice.ProviderID, choice.ModelID); ok {
			label = modelDisplayName(model)
		}
	}
	sel := ui.SelectBase(c.Key("title-model"), &choice)
	t := c.Theme()
	sel.Trigger.Height(30).MinWidth(180).MaxWidth(300).Padding(4, 8).Gap(8).Radius(6).Border(1, t.Border).Background(t.Surface).Label("Model").Children(func() {
		ui.Text(c, label).Grow(1).MinWidth(0).SingleLine()
		ui.Icon(c, settingsChevron).Size(14, 14).Shrink(0)
	})
	sel.Popup(func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0).Padding(4).Radius(8).Border(1, t.Border).Background(t.Background).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, .18)).MaxHeight(300).Width(320)
		ui.Scroll(c).MaxHeight(280).Children(func() {
			sel.Item(modelChoice{}).Padding(6, 10).Children(func() { ui.Text(c, "Same as chat model") })
			for _, p := range a.state.Settings.Providers {
				if a.clientForProvider(p.ID) == nil {
					continue
				}
				if a.enabledProviderCount() > 1 {
					ui.Row(c).Gap(7).Padding(6, 10).TextColor(t.TextMuted).Children(func() {
						providerIcon(c, p.Kind, 14)
						ui.Text(c, p.Name).FontSize(11).SingleLine()
					})
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
		a.state.Settings.TitleProviderID, a.state.Settings.TitleModel = choice.ProviderID, choice.ModelID
		a.saveSettings()
	})
	return sel.Trigger
}
