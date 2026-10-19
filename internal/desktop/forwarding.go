package desktop

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
)

func (a *app) syncForwardingProviders() {
	if a.forwarder == nil {
		return
	}
	targets := map[string]localproxy.Target{}
	for _, p := range a.state.Settings.Providers {
		client, ok := a.clientForProvider(p.ID).(provider.RawClient)
		if ok {
			targets[p.ID] = localproxy.Target{Client: client, Models: a.providerModels(p.ID)}
		}
	}
	a.forwarder.Update(targets)
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
	if s.LocalKey == "" {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			a.forwardError = "Could not generate a local API key."
			return
		}
		s.LocalKey = "cc-local-" + hex.EncodeToString(key[:])
	}
	// Persist the key before exposing the endpoint so restart cannot silently
	// change a copied configuration after a failed settings write.
	if a.storage != nil {
		if err := a.storage.Save(a.state); err != nil {
			a.forwardError = "Could not save forwarding settings: " + err.Error()
			return
		}
	}
	server := localproxy.New(s.LocalKey)
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
	s := &a.state.Settings
	if !a.agentSetup.portInitialized {
		a.agentSetup.portText = strconv.Itoa(s.LocalPort)
		a.agentSetup.portInitialized = true
	}
	settingsCard(c, func() {
		settingsRow(c, "Local forwarding", "Use your signed-in providers from coding agents on this computer.", func() {
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
		settingsRow(c, "Local port", "Change while forwarding is off.", func() {
			ui.TextInput(c.Key("forward-port"), &a.agentSetup.portText).Width(100).Disabled(s.LocalForwarding).Label("Local forwarding port").OnChange(func() {
				port, err := strconv.Atoi(a.agentSetup.portText)
				if err == nil && port >= 1024 && port <= 65535 {
					s.LocalPort = port
					a.saveSettings()
				}
			})
		})
	})
	tray := "Recommended: enable Keep running in the tray in General settings."
	if s.StayInTray {
		tray = "Keep running in the tray is enabled."
	}
	startup := "Recommended: enable Open at login in General settings."
	if a.openAtLogin {
		startup = "Open at login is enabled."
	}
	ui.Text(c, tray).FontSize(12).TextColor(c.Theme().TextMuted)
	ui.Text(c, startup).FontSize(12).TextColor(c.Theme().TextMuted)
	ui.Button(c, "Open General settings").OnClick(func() { a.settingsPage = "General" })
	if a.forwardError != "" {
		ui.Text(c, a.forwardError).TextColor(c.Theme().TextMuted)
	}
	if a.forwarder != nil {
		if err := a.forwarder.Err(); err != nil {
			ui.Text(c, "Forwarding stopped: "+err.Error())
		} else {
			ui.Text(c, fmt.Sprintf("Listening on 127.0.0.1:%d", s.LocalPort)).FontSize(12)
		}
	} else if s.LocalForwarding {
		ui.Button(c, "Retry forwarding").OnClick(a.startForwarding)
	}
}
