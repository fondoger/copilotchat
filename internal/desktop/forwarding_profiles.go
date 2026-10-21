package desktop

import (
	"fmt"
	"maps"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func (a *app) selectedForwardProfile() *store.ForwardProfile {
	profiles := a.state.Settings.ForwardProfiles
	for i := range profiles {
		if profiles[i].ID == a.agentSetup.profileID {
			return &profiles[i]
		}
	}
	if len(profiles) == 0 {
		return nil
	}
	a.chooseForwardProfile(profiles[0].ID)
	return &profiles[0]
}

func (a *app) chooseForwardProfile(id string) {
	s := &a.agentSetup
	if s.profileID == id {
		return
	}
	s.profileID, s.profileError, s.notice, s.modelError = id, "", "", ""
	s.defaultsOpen, s.configOpen = false, false
	for _, p := range a.state.Settings.ForwardProfiles {
		if p.ID == id {
			s.profileName = p.Name
			break
		}
	}
}

func (a *app) addForwardProfile(duplicate bool) {
	name := "profile"
	p := store.ForwardProfile{}
	if source := a.selectedForwardProfile(); duplicate && source != nil {
		name = source.Name + "-copy"
		p = *source
		p.Models = slices.Clone(source.Models)
		p.ClaudeDefaults = maps.Clone(source.ClaudeDefaults)
	}
	if len(name) > 58 {
		name = name[:58]
	}
	base := name
	for n := 2; slices.ContainsFunc(a.state.Settings.ForwardProfiles, func(p store.ForwardProfile) bool { return p.Name == name }); n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	p.ID, p.Name = provider.RequestID(), name
	a.state.Settings.ForwardProfiles = append(a.state.Settings.ForwardProfiles, p)
	a.chooseForwardProfile(p.ID)
	a.saveSettings()
}

func (a *app) renameForwardProfile(p *store.ForwardProfile, name string) string {
	if !localproxy.ValidProfileName(name) {
		return "Use 1–64 letters, numbers, hyphens or underscores; start with a letter or number."
	}
	if slices.ContainsFunc(a.state.Settings.ForwardProfiles, func(other store.ForwardProfile) bool { return other.ID != p.ID && other.Name == name }) {
		return "This profile name is already used."
	}
	p.Name = name
	a.saveSettings()
	return ""
}

func (a *app) deleteForwardProfile(id string) {
	a.syncForwardCatalog(id, nil)
	a.state.Settings.ForwardProfiles = slices.DeleteFunc(a.state.Settings.ForwardProfiles, func(p store.ForwardProfile) bool { return p.ID == id })
	if a.agentSetup.profileID == id {
		a.chooseForwardProfile("")
	}
	a.saveSettings()
}

func (a *app) forwardProfileMenu(m *ui.Menu, id string) {
	if m.Item("Rename").Chosen() {
		for _, p := range a.state.Settings.ForwardProfiles {
			if p.ID == id {
				a.agentSetup.renameID = id
				a.agentSetup.profileName = p.Name
				a.agentSetup.profileError = ""
				a.agentSetup.renameOpen = true
			}
		}
	}
	if m.Item("Duplicate").Chosen() {
		a.chooseForwardProfile(id)
		a.addForwardProfile(true)
	}
	m.Separator()
	if m.Item("Delete").Chosen() && a.askForwarding("Delete profile?", "The profile URL will stop working and its active requests will be interrupted. Other profiles and provider accounts will remain available.", "Delete") {
		a.deleteForwardProfile(id)
	}
}

func (a *app) forwardingProfileSettings(c *ui.Context) {
	t, s := c.Theme(), &a.agentSetup
	a.selectedForwardProfile()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(4).Wrap().PaddingY(4).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			for _, p := range a.state.Settings.ForwardProfiles {
				menu := func(m *ui.Menu) { a.forwardProfileMenu(m, p.ID) }
				tab := ui.Row(c.Key("profile-tab-" + p.ID)).Gap(0).Radius(6).ContextMenu(menu)
				if p.ID == s.profileID {
					tab.Background(t.Surface).Border(1, t.Border)
				}
				tab.Children(func() {
					ui.ButtonBase(c.Key("profile-select-" + p.ID)).Height(30).PaddingX(10).Radius(5).Label("Profile " + p.Name).Tooltip(p.Name).ContextMenu(menu).Children(func() {
						ui.Text(c, p.Name).FontSize(12).FontWeight(500).SingleLine().MaxWidth(170)
					}).OnClick(func() { a.chooseForwardProfile(p.ID) })
					if p.ID == s.profileID {
						ui.ButtonBase(c.Key("profile-actions-"+p.ID)).Size(24, 30).Radius(5).Label("Profile actions " + p.Name).Tooltip("Profile actions").Menu(menu).Children(func() {
							ui.Icon(c, settingsChevron).Size(12, 12).TextColor(t.TextMuted)
						})
					}
				})
			}
			ui.ButtonBase(c.Key("add-forward-profile")).Size(30, 30).Radius(5).Label("New profile").Tooltip("New profile").Children(func() {
				ui.Text(c, "+").FontSize(20).TextColor(t.TextMuted)
			}).OnClick(func() { a.addForwardProfile(false) })
		})
		if p := a.selectedForwardProfile(); p != nil {
			ui.Text(c, localproxy.BaseURL(a.state.Settings.LocalPort, p.Name)).FontSize(11).TextColor(t.TextMuted).Selectable()
		} else {
			ui.Text(c, "Create a profile to choose models and generate agent configuration.").FontSize(12).TextColor(t.TextMuted)
		}
	})
	ui.Modal(c.Key("rename-forward-profile"), &s.renameOpen, func() {
		ui.Text(c, "Rename profile").FontSize(16).FontWeight(600)
		ui.Text(c, "Renaming changes the base URL. Copy the updated configuration to your agent.").FontSize(12).TextColor(t.TextMuted).MaxWidth(360)
		ui.TextInput(c.Key("profile-name"), &s.profileName).Width(360).Background(t.Surface).Label("Profile name").AutoFocus().OnChange(func() { s.profileError = "" })
		if s.profileError != "" {
			ui.Text(c, s.profileError).FontSize(11).TextColor(t.TextMuted).MaxWidth(360)
		}
		ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
			ui.Button(c, "Cancel").OnClick(func() { s.renameOpen = false })
			ui.PrimaryButton(c, "Save profile name").OnClick(func() {
				for i := range a.state.Settings.ForwardProfiles {
					p := &a.state.Settings.ForwardProfiles[i]
					if p.ID == s.renameID {
						s.profileError = a.renameForwardProfile(p, s.profileName)
						s.renameOpen = s.profileError != ""
						s.notice = ""
						return
					}
				}
				s.renameOpen = false
			})
		})
	})
}
