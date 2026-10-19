package desktop

import (
	"fmt"
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
	s.profileID, s.profileError, s.modelID, s.notice = id, "", "", ""
	s.aliases = nil
	for _, p := range a.state.Settings.ForwardProfiles {
		if p.ID == id {
			s.profileName = p.Name
			break
		}
	}
}

func (a *app) addForwardProfile(duplicate bool) {
	name := "profile"
	var models []store.ForwardModel
	if p := a.selectedForwardProfile(); duplicate && p != nil {
		name, models = p.Name+"-copy", slices.Clone(p.Models)
	}
	if len(name) > 58 {
		name = name[:58]
	}
	base := name
	for n := 2; slices.ContainsFunc(a.state.Settings.ForwardProfiles, func(p store.ForwardProfile) bool { return p.Name == name }); n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	p := store.ForwardProfile{ID: provider.RequestID(), Name: name, Models: models}
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
	a.chooseForwardProfile("")
	a.saveSettings()
}

func (a *app) forwardingProfileSettings(c *ui.Context) {
	t, s := c.Theme(), &a.agentSetup
	ui.Text(c, "Profiles").FontWeight(500)
	p := a.selectedForwardProfile()
	options := []setupOption{}
	for _, profile := range a.state.Settings.ForwardProfiles {
		options = append(options, setupOption{profile.ID, profile.Name})
	}
	if p != nil {
		setupSelect(c, "forward-profile", "Forwarding profile", &s.profileID, options, func() { a.chooseForwardProfile(s.profileID) })
	}
	ui.Row(c).Gap(8).Wrap().Children(func() {
		ui.Button(c, "New profile").OnClick(func() { a.addForwardProfile(false) })
		ui.Button(c, "Duplicate profile").Disabled(p == nil).OnClick(func() { a.addForwardProfile(true) })
		ui.Button(c, "Delete profile").Disabled(p == nil).OnClick(func() {
			if p != nil && a.askForwarding("Delete profile?", "The profile URL will stop working and its active requests will be interrupted. Other profiles and provider accounts will remain available.", "Delete") {
				a.deleteForwardProfile(p.ID)
			}
		})
	})
	if p == nil {
		ui.Text(c, "Create a profile to choose models and generate agent configuration.").FontSize(12).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "Profile name / URL path").FontSize(12)
	ui.TextInput(c.Key("profile-name-"+p.ID), &s.profileName).FillWidth().Background(t.Surface).Label("Profile name").OnChange(func() { s.profileError = a.renameForwardProfile(p, s.profileName) })
	if s.profileError != "" {
		ui.Text(c, "Not saved: "+s.profileError).FontSize(11).TextColor(t.TextMuted)
	}
	ui.Text(c, localproxy.BaseURL(a.state.Settings.LocalPort, p.Name)).FontSize(12).Selectable()
	ui.Text(c, "Renaming changes the URL. Copy the updated configuration to your agent.").FontSize(11).TextColor(t.TextMuted)
}
