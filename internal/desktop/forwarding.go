package desktop

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/appinfo"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
)

func (a *app) syncForwardingProviders() {
	a.reconcileForwardModels()
	profiles := map[string]localproxy.Profile{}
	for i := range a.state.Settings.ForwardProfiles {
		profile := &a.state.Settings.ForwardProfiles[i]
		models := a.forwardingModels(profile)
		profiles[profile.Name] = localproxy.Profile{ID: profile.ID, Models: models,
			ClaudeDefaults: a.claudeDefaults(profile), ClaudeClassifierModel: a.classifierModel(profile)}
		a.syncForwardCatalog(profile.ID, models)
	}
	if a.forwarder == nil {
		return
	}
	a.forwarder.SetInspection(a.state.Settings.GatewayInspection)
	targets := map[string]localproxy.Target{}
	for _, p := range a.state.Settings.Providers {
		client, ok := a.clientForProvider(p.ID).(provider.RawClient)
		if ok {
			targets[p.ID] = localproxy.Target{Client: client}
		}
	}
	a.forwarder.Update(targets, profiles)
}

func (a *app) syncForwardCatalog(id string, models []localproxy.Model) {
	if a.storage == nil {
		return
	}
	if a.forwardCatalogs == nil {
		a.forwardCatalogs = map[string][32]byte{}
		a.forwardCatalogErrors = map[string]string{}
	}
	data, _ := json.Marshal(models)
	hash := sha256.Sum256(data)
	if a.forwardCatalogs[id] == hash {
		return
	}
	if err := a.storage.SaveAgentCatalog(id, localproxy.CodexCatalog(models)); err != nil {
		a.forwardCatalogErrors[id] = "Could not update the Codex model catalog: " + err.Error()
		return
	}
	a.forwardCatalogs[id] = hash
	delete(a.forwardCatalogErrors, id)
}

func (a *app) startForwarding() {
	if a.forwarder != nil {
		return
	}
	s := &a.state.Settings
	if a.agentSetup.portInitialized {
		port, err := strconv.Atoi(a.agentSetup.portText)
		if err != nil || port < 1024 || port > 65535 {
			a.forwardError = "Enter a local port from 1024 to 65535."
			return
		}
		s.LocalPort = port
	}
	if s.LocalPort < 1024 || s.LocalPort > 65535 {
		s.LocalPort = localproxy.DefaultPort
	}
	if s.LocalKey == "" || (strings.HasPrefix(s.LocalKey, "cc-local-") && len(s.LocalKey) == 73) {
		s.LocalKey = localproxy.DefaultKey
	}
	// Persist the key before exposing the endpoint so restart cannot silently
	// change a copied configuration after a failed settings write.
	if a.storage != nil {
		if err := a.storage.Save(a.state); err != nil {
			a.forwardError = "Could not save forwarding settings: " + err.Error()
			return
		}
	}
	var options []localproxy.Option
	if channel := appinfo.Channel(); channel == "debug" || channel == "dev" {
		options = append(options, localproxy.WithMCPErrorLog(log.Default()))
	}
	server := localproxy.New(s.LocalKey, options...)
	if err := server.Start(s.LocalPort); err != nil {
		server.Close()
		a.forwardError = err.Error()
		return
	}
	a.forwarder, a.forwardError = server, ""
	a.syncForwardingProviders()
}

func (a *app) stopForwarding() {
	if a.forwarder != nil {
		a.forwarder.Close()
		a.forwarder = nil
	}
}

func (a *app) askForwarding(title, detail, action string) bool {
	if a.confirmForwarding != nil {
		return a.confirmForwarding(title, detail, action)
	}
	parent := a.settingsWin
	if parent == nil || !parent.IsVisible() {
		a.show()
		parent = a.win
	}
	result, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Type: mygo.MessageWarning, Title: title, Message: title, Detail: detail, Buttons: []string{"Cancel", action}, DefaultButton: 0, CancelButton: 0})
	return err == nil && result.Button == 1
}

func (a *app) allowForwardingQuit() bool {
	if !a.state.Settings.LocalForwarding || a.forwardQuitApproved {
		return true
	}
	if !a.askForwarding("Quit Copilot Chat?", "Local forwarding will stop. Connected agents will lose access and active requests will be interrupted. Keep the app running in the tray to continue forwarding.", "Quit") {
		return false
	}
	a.forwardQuitApproved = true
	return true
}

func (a *app) changeStayInTray() {
	if !a.state.Settings.StayInTray && a.state.Settings.LocalForwarding {
		if !a.askForwarding("Turn off Keep running in the tray?", "Local forwarding is enabled. With tray mode off, closing Copilot Chat will stop forwarding and interrupt connected agents.", "Turn off") {
			a.state.Settings.StayInTray = true
			return
		}
	}
	a.syncTray()
	a.saveSettings()
}

func (a *app) startupControl(c *ui.Context) {
	enabled := a.openAtLogin
	ui.Switch(c.Key("start-at-login"), &enabled).Label("Open at login").OnChange(func() {
		a.startupError = ""
		if err := mygo.App.SetOpenAtLogin(enabled); err != nil {
			a.startupError = err.Error()
		}
		a.openAtLogin = mygo.App.OpenAtLogin()
	})
}

func (a *app) forwardingSettings(c *ui.Context) {
	s, page, t := &a.state.Settings, &a.agentSetup, c.Theme()
	if !page.portInitialized {
		page.portText = strconv.Itoa(s.LocalPort)
		page.portInitialized = true
	}
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		ui.Row(c).FillWidth().Gap(10).Children(func() {
			ui.Text(c, "Local forwarding").FontSize(13).FontWeight(500).Grow(1)
			status := "Off"
			if a.forwarder != nil && a.forwarder.Err() == nil {
				status = fmt.Sprintf("127.0.0.1:%d", s.LocalPort)
			}
			ui.Text(c, status).FontSize(11).TextColor(t.TextMuted)
			ui.Switch(c.Key("forwarding"), &s.LocalForwarding).Label("Enable local forwarding").OnChange(func() {
				a.forwardError = ""
				if s.LocalForwarding {
					a.startForwarding()
				} else {
					a.stopForwarding()
				}
				a.saveSettings()
			})
		})
		tray, startup := "Tray: recommended", "Open at login: recommended"
		if s.StayInTray {
			tray = "Tray: enabled"
		}
		if a.openAtLogin {
			startup = "Open at login: enabled"
		}
		ui.Text(c, tray+" · "+startup).FontSize(11).TextColor(t.TextMuted)
		ui.Row(c).FillWidth().Gap(10).Children(func() {
			ui.Text(c, "HTTP request inspection (GoVisual)").FontSize(12).Grow(1)
			if a.forwarder != nil {
				if url := a.forwarder.InspectionURL(); url != "" {
					ui.Button(c, "Open dashboard").Height(26).FontSize(11).OnClick(func() {
						if err := mygo.Shell.OpenExternal(url); err != nil {
							a.forwardError = "Could not open GoVisual: " + err.Error()
						}
					})
				}
				if url := a.forwarder.InspectionMCPURL(); url != "" {
					ui.Button(c, "Copy MCP URL").Height(26).FontSize(11).OnClick(func() { mygo.Clipboard.WriteText(url) })
				}
			}
			ui.Switch(c.Key("gateway-inspection"), &s.GatewayInspection).Label("Enable HTTP request inspection").OnChange(a.saveSettings)
		})
		ui.Text(c, "Optional request and response capture; may slightly reduce performance. Keeps up to 100 requests in memory, capped at 1 MiB per body.").FontSize(11).TextColor(t.TextMuted)
		if s.GatewayInspection && a.forwarder == nil {
			ui.Text(c, "Enable local forwarding to open the dashboard.").FontSize(11).TextColor(t.TextMuted)
		}
		d := setupDisclosure(c, "forward-options", "Options", &page.optionsOpen)
		d.Panel(func() {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				ui.Row(c).FillWidth().Gap(12).Children(func() {
					ui.Text(c, "Local port").FontSize(12).Grow(1)
					ui.TextInput(c.Key("forward-port"), &page.portText).Width(100).Height(28).Background(t.Surface).Disabled(s.LocalForwarding).Label("Local forwarding port").OnChange(func() {
						port, err := strconv.Atoi(page.portText)
						if err == nil && port >= 1024 && port <= 65535 {
							s.LocalPort = port
							a.saveSettings()
						}
					})
				})
				key := s.LocalKey
				if key == "" || (strings.HasPrefix(key, "cc-local-") && len(key) == 73) {
					key = localproxy.DefaultKey
				}
				ui.Row(c).FillWidth().Children(func() {
					ui.Text(c, "Local API key").FontSize(12).Grow(1)
					ui.Text(c, key).FontSize(12).Selectable()
				})
				ui.Row(c).FillWidth().Children(func() {
					ui.Text(c, "Keep running in the tray").FontSize(12).Grow(1)
					ui.Switch(c.Key("forward-tray"), &s.StayInTray).Label("Keep running in the tray").OnChange(a.changeStayInTray)
				})
				ui.Row(c).FillWidth().Children(func() {
					ui.Text(c, "Open at login").FontSize(12).Grow(1)
					a.startupControl(c)
				})
				if a.startupError != "" {
					ui.Text(c, a.startupError).FontSize(11).TextColor(t.TextMuted)
				}
				ui.Text(c, "Keep Copilot Chat running to serve agents. Change the port while forwarding is off.").FontSize(11).TextColor(t.TextMuted)
			})
		})
		if a.forwardError != "" {
			ui.Text(c, a.forwardError).FontSize(11).TextColor(t.TextMuted)
		}
		if a.forwarder != nil {
			if err := a.forwarder.Err(); err != nil {
				ui.Text(c, "Forwarding stopped: "+err.Error()).FontSize(11)
				ui.Button(c, "Retry forwarding").OnClick(func() { a.stopForwarding(); a.startForwarding() })
			}
		} else if s.LocalForwarding {
			ui.Button(c, "Retry forwarding").OnClick(a.startForwarding)
		}
	})
}
