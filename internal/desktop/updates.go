package desktop

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/appinfo"
	"github.com/fondoger/copilotchat/internal/appupdate"
	"github.com/fondoger/copilotchat/internal/store"
)

var updateIntervals = []struct {
	label string
	hours int
}{
	{"Every hour", 1}, {"Every 6 hours", 6}, {"Every 12 hours", 12},
	{"Every day", 24}, {"Every week", 168},
}

func (a *app) startUpdates() {
	a.updater = appupdate.New(appupdate.NewMyGoClient(a.currentUpdateChannel()), func() {
		a.update(func() {
			if a.quitting {
				return
			}
			snapshot := a.updater.Snapshot()
			if !snapshot.LastCheck.IsZero() && !snapshot.LastCheck.Equal(a.state.Settings.LastUpdateCheck) {
				a.state.Settings.LastUpdateCheck = snapshot.LastCheck
				a.save()
			}
			a.handleChannelUpdate(snapshot)
		})
	})
	a.configureUpdates()
	ctx, cancel := context.WithCancel(context.Background())
	a.updateCancel = cancel
	go a.updater.Run(ctx)
}

func (a *app) configureUpdates() {
	if a.updater != nil {
		a.updater.Configure(a.state.Settings.AutomaticUpdates,
			time.Duration(a.state.Settings.UpdateIntervalHours)*time.Hour,
			a.state.Settings.LastUpdateCheck)
	}
}

func (a *app) currentUpdateChannel() string {
	if channel := a.state.Settings.UpdateChannel; channel != "" {
		return channel
	}
	if appinfo.Channel() == "dev" {
		return "dev"
	}
	return "stable"
}

func (a *app) restoreUpdateChannel() {
	s := &a.state.Settings
	previous, pending := s.UpdateChannel, s.PendingUpdate
	if pending != nil {
		if pending.Version == appinfo.Version() {
			s.UpdateChannel = pending.Channel
		}
		s.PendingUpdate = nil
	}
	s.UpdateChannel = a.currentUpdateChannel()
	if previous != s.UpdateChannel || pending != nil {
		a.save()
	}
}

func (a *app) changeUpdateChannel(channel string) {
	if a.updater == nil || channel == a.currentUpdateChannel() {
		return
	}
	if channel != "stable" && channel != "dev" {
		return
	}
	if channel == "dev" && runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" {
		a.updateError = "Dev releases are not available for macOS Intel."
		return
	}
	previous := a.updateChannelTarget
	a.updateChannelTarget, a.updateError = channel, ""
	if !a.updater.CheckWith(appupdate.NewMyGoClient(channel)) {
		a.updateChannelTarget = previous
		a.updateError = "Wait for the current update to finish."
	}
}

func (a *app) installAvailableUpdate() {
	a.updateError = ""
	if a.updater == nil {
		return
	}
	if channel := a.updateChannelTarget; channel != "" {
		previous := a.state.Settings.PendingUpdate
		a.state.Settings.PendingUpdate = &store.PendingUpdate{Channel: channel, Version: a.updateSnapshot().Version}
		if a.storage == nil {
			a.state.Settings.PendingUpdate = previous
			a.updateError = "Could not save the pending channel switch."
			return
		}
		if err := a.storage.Save(a.state); err != nil {
			a.state.Settings.PendingUpdate = previous
			a.updateError = "Could not save the pending channel switch: " + err.Error()
			return
		}
	}
	a.updater.Install()
}

func (a *app) handleChannelUpdate(snapshot appupdate.Snapshot) {
	if a.updateChannelTarget == "" {
		return
	}
	switch snapshot.Phase {
	case appupdate.Available:
		a.installAvailableUpdate()
	case appupdate.Ready:
		if pending := a.state.Settings.PendingUpdate; pending != nil && pending.Version == snapshot.Version {
			a.restartForUpdate()
		}
	case appupdate.Idle, appupdate.Error:
		if snapshot.Phase == appupdate.Idle {
			a.updateError = "No newer " + a.updateChannelTarget + " release is available. Channel unchanged."
		}
		a.updateChannelTarget = ""
		if a.state.Settings.PendingUpdate != nil {
			a.state.Settings.PendingUpdate = nil
			a.save()
		}
	}
}

func (a *app) stopUpdates() {
	if a.updateCancel != nil {
		a.updateCancel()
	}
	if a.updater != nil {
		a.updater.Close()
	}
}

func (a *app) updateSnapshot() appupdate.Snapshot {
	if a.updater != nil {
		return a.updater.Snapshot()
	}
	return appupdate.Snapshot{Phase: appupdate.Disabled}
}

// Cancellation stops network work immediately, but MyGo may already be in its
// final file replacement. Let that finish before the process exits.
func (a *app) deferQuitForUpdate() bool {
	if a.updater == nil {
		return false
	}
	a.updater.Close()
	select {
	case <-a.updater.Done():
		return false
	default:
		if !a.updateQuitWaiting {
			a.updateQuitWaiting = true
			go func() {
				<-a.updater.Done()
				a.update(func() {
					a.updateQuitWaiting = false
					mygo.App.Quit()
				})
			}()
		}
		return true
	}
}

func (a *app) checkForUpdates() {
	a.updateError = ""
	if a.updater != nil {
		if a.updateChannelTarget != "" {
			a.updater.CheckWith(appupdate.NewMyGoClient(a.updateChannelTarget))
		} else {
			a.updater.Check()
		}
	}
}

func (a *app) openUpdateSettings() {
	a.openSettings("General")
	a.checkForUpdates()
}

func (a *app) updateSettings(c *ui.Context) {
	snapshot := a.updateSnapshot()
	ui.Text(c, "Updates").FontSize(14).FontWeight(600)
	settingsCard(c, func() {
		settingsRow(c, "Channel", "", func() {
			channel := "Stable"
			if a.currentUpdateChannel() == "dev" {
				channel = "Dev"
			}
			channels := []string{"Stable", "Dev"}
			if runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" {
				channels = []string{"Stable"}
			}
			ui.Select(c.Key("update-channel"), &channel, channels).Height(26).Label("Update channel").
				Disabled(snapshot.Phase == appupdate.Disabled || snapshot.Phase == appupdate.Checking || snapshot.Phase == appupdate.Downloading || snapshot.Phase == appupdate.Ready).
				OnChange(func() { a.changeUpdateChannel(strings.ToLower(channel)) })
		})
		settingsRow(c, "Automatic updates", "Download and install updates in the background. Use the new version on your next launch.", func() {
			ui.Switch(c.Key("automatic-updates"), &a.state.Settings.AutomaticUpdates).Label("Enable automatic updates").OnChange(func() {
				a.configureUpdates()
				a.saveSettings()
			})
		})
		settingsRow(c, "Check frequency", "Changes take effect immediately.", func() {
			selected := "Every day"
			options := make([]string, 0, len(updateIntervals))
			for _, interval := range updateIntervals {
				options = append(options, interval.label)
				if interval.hours == a.state.Settings.UpdateIntervalHours {
					selected = interval.label
				}
			}
			ui.Select(c.Key("update-frequency"), &selected, options).Height(26).Label("Update check frequency").Disabled(!a.state.Settings.AutomaticUpdates).OnChange(func() {
				for _, interval := range updateIntervals {
					if interval.label == selected {
						a.state.Settings.UpdateIntervalHours = interval.hours
						break
					}
				}
				a.configureUpdates()
				a.saveSettings()
			})
		})
		lastCheck := "Not checked yet"
		if !a.state.Settings.LastUpdateCheck.IsZero() {
			lastCheck = "Last checked: " + a.state.Settings.LastUpdateCheck.Local().Format("2006-01-02 15:04")
		}
		settingsRow(c, "Version "+appinfo.Version(), lastCheck, func() {
			ui.Button(c.Key("check-updates"), "Check for updates").Height(28).
				Disabled(snapshot.Phase == appupdate.Checking || snapshot.Phase == appupdate.Downloading || snapshot.Phase == appupdate.Ready).
				OnClick(a.checkForUpdates)
		})
	})
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, updateStatus(snapshot)).FontSize(12).TextColor(c.Theme().TextMuted)
		if snapshot.Phase == appupdate.Downloading {
			progress := float64(0)
			if snapshot.Total > 0 {
				progress = min(1, float64(snapshot.Downloaded)/float64(snapshot.Total))
			}
			ui.Progress(c.Key("update-download-progress"), progress)
		}
		if snapshot.Phase == appupdate.Available || (snapshot.Phase == appupdate.Error && snapshot.Version != "") {
			ui.Button(c.Key("install-update"), "Download and install").OnClick(a.installAvailableUpdate)
		}
		if snapshot.Phase == appupdate.Ready {
			blocked := a.updateRestartBlockReason()
			ui.Button(c.Key("restart-update"), "Restart to update").Disabled(blocked != "").OnClick(a.restartForUpdate)
			if blocked != "" {
				ui.Text(c, blocked).FontSize(12).TextColor(c.Theme().TextMuted)
			}
		}
		if a.updateError != "" {
			ui.Text(c, a.updateError).FontSize(12).TextColor(c.Theme().TextMuted)
		}
		if snapshot.Version != "" && snapshot.Notes != "" {
			open := ui.Local(c.Root(), "update-release-notes-open", func() bool { return false })
			ui.Collapsible(c.Key("update-release-notes"), "What's new in "+snapshot.Version, open, func() {
				ui.Text(c, snapshot.Notes).FontSize(12).Selectable()
			})
		}
	})
}

func updateStatus(snapshot appupdate.Snapshot) string {
	switch snapshot.Phase {
	case appupdate.Disabled:
		return "Updates require an official release installed in a writable folder. Debug builds cannot update themselves."
	case appupdate.Checking:
		return "Checking GitHub Releases…"
	case appupdate.Available:
		return "Version " + snapshot.Version + " is available."
	case appupdate.Downloading:
		if snapshot.Total > 0 {
			return fmt.Sprintf("Downloading %s · %.1f / %.1f MB", snapshot.Version, float64(snapshot.Downloaded)/1e6, float64(snapshot.Total)/1e6)
		}
		return "Downloading and verifying version " + snapshot.Version + "…"
	case appupdate.Ready:
		return "Version " + snapshot.Version + " is installed. Restart when ready, or use it on your next launch."
	case appupdate.Error:
		return "Update failed: " + snapshot.Error
	default:
		if !snapshot.LastCheck.IsZero() {
			return "You're up to date."
		}
		return "Updates are downloaded from fondoger/copilotchat on GitHub."
	}
}

// Never discard a response, unsent attachment, message edit or another chat's
// draft just to restart. The installed update can wait until a normal launch.
func (a *app) updateRestartBlockReason() string {
	if a.streaming {
		return "Wait for the current response before restarting."
	}
	if a.editMessageID != "" {
		return "Save or cancel your message edit before restarting."
	}
	if strings.TrimSpace(a.draft) != "" || len(a.attachments) > 0 {
		return "Send or clear your draft before restarting."
	}
	for id, draft := range a.drafts {
		if id != a.state.ActiveID && (strings.TrimSpace(draft.Text) != "" || len(draft.Attachments) > 0) {
			return "Send or clear drafts in other chats before restarting."
		}
	}
	return ""
}

func (a *app) restartForUpdate() {
	if a.updateSnapshot().Phase != appupdate.Ready || a.updateRestartBlockReason() != "" {
		return
	}
	if a.storage != nil {
		if err := a.storage.Save(a.state); err != nil {
			a.updateError = "Could not save your conversations: " + err.Error()
			return
		}
	}
	if a.updateChannelTarget != "" {
		// Selecting a channel already requested the automatic restart.
		a.forwardQuitApproved = true
	}
	mygo.App.Relaunch()
}
