package desktop

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/appupdate"
	"github.com/fondoger/copilotchat/internal/store"
)

type desktopUpdateClient struct {
	enabled bool
	check   func(context.Context) (*appupdate.Release, error)
}

func (c desktopUpdateClient) Enabled() bool { return c.enabled }

func (c desktopUpdateClient) Check(ctx context.Context) (*appupdate.Release, error) {
	return c.check(ctx)
}

func newDesktopUpdateTestApp(t *testing.T, client desktopUpdateClient) (*app, <-chan struct{}) {
	t.Helper()
	a := newSettingsTestApp(t)
	changed := make(chan struct{}, 1)
	a.updater = appupdate.New(client, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	a.configureUpdates()
	t.Cleanup(a.updater.Close)
	return a, changed
}

func waitForDesktopUpdate(t *testing.T, a *app, changed <-chan struct{}, phase appupdate.Phase) appupdate.Snapshot {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		snapshot := a.updateSnapshot()
		if snapshot.Phase == phase {
			return snapshot
		}
		select {
		case <-changed:
		case <-timer.C:
			t.Fatalf("update phase = %s, want %s; snapshot: %+v", snapshot.Phase, phase, snapshot)
		}
	}
}

func TestSettingsAutomaticUpdatesTogglePersists(t *testing.T) {
	a := newSettingsTestApp(t)
	tester := ui.NewTester(a.settingsView, 900, 620)
	for _, want := range []bool{false, true} {
		settingsClick(t, tester, "Enable automatic updates")
		if a.state.Settings.AutomaticUpdates != want {
			t.Fatalf("automatic updates = %v, want %v", a.state.Settings.AutomaticUpdates, want)
		}
		if got := settingsSavedState(t, a).Settings.AutomaticUpdates; got != want {
			t.Fatalf("saved automatic updates = %v, want %v", got, want)
		}
	}
}

func TestSettingsUpdateFrequencySelectionPersists(t *testing.T) {
	a := newSettingsTestApp(t)
	tester := ui.NewTester(a.settingsView, 900, 620)
	for _, option := range []struct {
		label string
		hours int
	}{{"Every hour", 1}, {"Every 6 hours", 6}, {"Every 12 hours", 12}, {"Every week", 168}, {"Every day", 24}} {
		settingsClick(t, tester, "Update check frequency")
		settingsClick(t, tester, option.label)
		if a.state.Settings.UpdateIntervalHours != option.hours {
			t.Fatalf("selected interval = %d, want %d", a.state.Settings.UpdateIntervalHours, option.hours)
		}
		if got := settingsSavedState(t, a).Settings.UpdateIntervalHours; got != option.hours {
			t.Fatalf("saved interval = %d, want %d", got, option.hours)
		}
	}
}

func TestChannelSwitchCommitsOnlyAfterTargetVersionStarts(t *testing.T) {
	previousVersion := mygo.App.Version()
	t.Cleanup(func() { mygo.App.SetVersion(previousVersion) })
	for _, tc := range []struct{ running, channel string }{
		{"0.5.11-dev.42", "dev"},
		{"0.5.10", "stable"},
	} {
		t.Run(tc.running, func(t *testing.T) {
			mygo.App.SetVersion("0.5.10")
			finish := make(chan struct{})
			var a *app
			release := &appupdate.Release{Version: "0.5.11-dev.42", Install: func(ctx context.Context, progress func(int64, int64)) error {
				state, err := a.storage.Load()
				if err != nil {
					return err
				}
				if state.Settings.UpdateChannel != "stable" || state.Settings.PendingUpdate == nil || state.Settings.PendingUpdate.Version != "0.5.11-dev.42" {
					return errors.New("channel intent was not saved before installation")
				}
				progress(1, 2)
				select {
				case <-finish:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			a, changed := newDesktopUpdateTestApp(t, desktopUpdateClient{enabled: true})
			a.state.Settings.UpdateChannel = "stable"
			a.state.Settings.AutomaticUpdates = false
			a.configureUpdates()
			a.updateChannelTarget = "dev"
			a.draft = "Keep this draft before restarting."
			a.streaming = true // Do not invoke the real application relaunch in this test.
			a.updater.CheckWith(desktopUpdateClient{enabled: true, check: func(context.Context) (*appupdate.Release, error) { return release, nil }})
			a.handleChannelUpdate(waitForDesktopUpdate(t, a, changed, appupdate.Available))
			waitForDesktopUpdate(t, a, changed, appupdate.Downloading)
			close(finish)
			a.handleChannelUpdate(waitForDesktopUpdate(t, a, changed, appupdate.Ready))
			if a.currentUpdateChannel() != "stable" || a.draft == "" {
				t.Fatal("channel changed or draft was lost before restart")
			}
			mygo.App.SetVersion(tc.running)
			restarted := &app{state: settingsSavedState(t, a), storage: a.storage}
			restarted.restoreComposer()
			restarted.restoreUpdateChannel()
			state := settingsSavedState(t, restarted)
			if state.Settings.UpdateChannel != tc.channel || state.Settings.PendingUpdate != nil {
				t.Fatal("restart did not confirm the actual installed version", state.Settings.UpdateChannel)
			}
		})
	}
}

func TestChannelSwitchFailureKeepsOriginalChannel(t *testing.T) {
	for _, hasRelease := range []bool{false, true} {
		t.Run(map[bool]string{false: "no newer release", true: "download failure"}[hasRelease], func(t *testing.T) {
			a, changed := newDesktopUpdateTestApp(t, desktopUpdateClient{enabled: true})
			a.state.Settings.UpdateChannel, a.updateChannelTarget = "stable", "dev"
			a.updater.CheckWith(desktopUpdateClient{enabled: true, check: func(context.Context) (*appupdate.Release, error) {
				if !hasRelease {
					return nil, nil
				}
				return &appupdate.Release{Version: "0.5.11-dev.42", Install: func(context.Context, func(int64, int64)) error { return errors.New("synthetic download failure") }}, nil
			}})
			if hasRelease {
				a.handleChannelUpdate(waitForDesktopUpdate(t, a, changed, appupdate.Available))
				a.handleChannelUpdate(waitForDesktopUpdate(t, a, changed, appupdate.Error))
			} else {
				a.handleChannelUpdate(waitForDesktopUpdate(t, a, changed, appupdate.Idle))
			}
			if a.currentUpdateChannel() != "stable" || a.state.Settings.PendingUpdate != nil || a.updateChannelTarget != "" {
				t.Fatal("failed switch changed the channel or retained stale intent")
			}
		})
	}
}

func TestManualUpdateCheckAndInstallWorkWithAutomaticUpdatesOff(t *testing.T) {
	var checks, installs atomic.Int32
	finishInstall := make(chan struct{})
	release := &appupdate.Release{
		Version: "9.9.9", Notes: "Synthetic release notes.",
		Install: func(ctx context.Context, progress func(int64, int64)) error {
			installs.Add(1)
			progress(1_000_000, 2_000_000)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-finishInstall:
				return nil
			}
		},
	}
	a, changed := newDesktopUpdateTestApp(t, desktopUpdateClient{enabled: true, check: func(context.Context) (*appupdate.Release, error) {
		checks.Add(1)
		return release, nil
	}})
	tester := ui.NewTester(a.settingsView, 900, 620)
	settingsClick(t, tester, "Enable automatic updates")
	if a.state.Settings.AutomaticUpdates || settingsSavedState(t, a).Settings.AutomaticUpdates {
		t.Fatal("automatic updates did not stay disabled")
	}
	settingsClick(t, tester, "Check for updates")
	snapshot := waitForDesktopUpdate(t, a, changed, appupdate.Available)
	tester.Frame()
	if checks.Load() != 1 || installs.Load() != 0 || snapshot.LastCheck.IsZero() || !tester.HasText("Version 9.9.9 is available.") {
		t.Fatalf("manual check did not expose an available release: %+v", snapshot)
	}
	assertInWindow(t, tester, "Download and install", 900, 620)
	settingsClick(t, tester, "Download and install")
	waitForDesktopUpdate(t, a, changed, appupdate.Downloading)
	tester.Frame()
	if !tester.HasText("Downloading") {
		t.Fatal("download status is not visible")
	}
	close(finishInstall)
	waitForDesktopUpdate(t, a, changed, appupdate.Ready)
	tester.Frame()
	if installs.Load() != 1 || !tester.HasText("Version 9.9.9 is installed.") {
		t.Fatalf("manual installation did not reach ready state: %+v", a.updateSnapshot())
	}
	assertInWindow(t, tester, "Restart to update", 900, 620)
	a.draft = "An unsent message"
	tester.Frame()
	if !a.prepareUpdateRestart() {
		t.Fatal("an unsent draft blocked restarting after a manual installation")
	}
	if got := settingsSavedState(t, a).Drafts[a.state.ActiveID].Text; got != a.draft {
		t.Fatal("restart preparation did not persist the draft")
	}
	chat := ui.NewTester(a.view, 1060, 720)
	if !chat.HasText("Update ready") {
		t.Fatal("installed update is not discoverable from the sidebar")
	}
}

func TestUpdateCheckErrorsAreVisible(t *testing.T) {
	a, changed := newDesktopUpdateTestApp(t, desktopUpdateClient{enabled: true, check: func(context.Context) (*appupdate.Release, error) {
		return nil, errors.New("synthetic network failure")
	}})
	tester := ui.NewTester(a.settingsView, 900, 620)
	settingsClick(t, tester, "Check for updates")
	snapshot := waitForDesktopUpdate(t, a, changed, appupdate.Error)
	tester.Frame()
	if !tester.HasText("Update failed: synthetic network failure") {
		t.Fatal("update check error is not visible")
	}
	if !snapshot.LastCheck.IsZero() || !settingsSavedState(t, a).Settings.LastUpdateCheck.IsZero() {
		t.Fatal("failed check was recorded as successful")
	}
	if slices.Contains(tester.Texts(), "Download and install") || slices.Contains(tester.Texts(), "Restart to update") {
		t.Fatal("failed check exposed actions for a nonexistent update")
	}
}

func TestDevelopmentBuildExplainsUpdateAvailability(t *testing.T) {
	var checks atomic.Int32
	a, _ := newDesktopUpdateTestApp(t, desktopUpdateClient{enabled: false, check: func(context.Context) (*appupdate.Release, error) {
		checks.Add(1)
		return nil, nil
	}})
	tester := ui.NewTester(a.settingsView, 900, 620)
	assertInWindow(t, tester, "Enable automatic updates", 900, 620)
	assertInWindow(t, tester, "Update check frequency", 900, 620)
	assertInWindow(t, tester, "Check for updates", 900, 620)
	settingsClick(t, tester, "Check for updates")
	if checks.Load() != 0 || a.updateSnapshot().Phase != appupdate.Disabled {
		t.Fatal("development build attempted an update check")
	}
}

func readyDesktopUpdateTestApp(t *testing.T) *app {
	t.Helper()
	a, changed := newDesktopUpdateTestApp(t, desktopUpdateClient{enabled: true, check: func(context.Context) (*appupdate.Release, error) {
		return &appupdate.Release{Version: "9.9.9", Install: func(context.Context, func(int64, int64)) error { return nil }}, nil
	}})
	a.checkForUpdates()
	waitForDesktopUpdate(t, a, changed, appupdate.Available)
	a.installAvailableUpdate()
	waitForDesktopUpdate(t, a, changed, appupdate.Ready)
	return a
}

func TestUpdateRestartPreservesDraftsAttachmentsAndMessageEdit(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			a := readyDesktopUpdateTestApp(t)
			a.state.Conversations = []store.Conversation{
				{ID: "first", Title: "First chat"},
				{ID: "second", Title: "Second chat", Messages: []store.Message{{ID: "editing", Role: role, Content: "Original message"}}},
			}
			a.state.ActiveID = "first"
			a.draft = "First draft\n草稿 🙂"
			a.attachments = []store.Attachment{{Name: "notes.txt", MIME: "text/plain", Data: "ZmlsZQ==", Size: 4}}
			a.selectChat("second")
			a.draft = "Second draft"
			a.attachments = []store.Attachment{{Name: "unsent.png", MIME: "image/png", Data: testPreviewPNG}}
			a.editMessageID, a.editText = "editing", "Uncommitted edit\n🙂"
			a.state.Drafts["second"] = store.Draft{Text: "Stale current draft"}
			if !a.prepareUpdateRestart() {
				t.Fatalf("drafts or a message edit blocked restarting: %s", a.updateError)
			}

			restarted := &app{state: settingsSavedState(t, a), storage: a.storage}
			restarted.restoreComposer()
			if restarted.state.ActiveID != "second" || restarted.draft != a.draft || !slices.Equal(restarted.attachments, a.attachments) {
				t.Fatal("restart lost the current draft or attachments, or restored stale content")
			}
			if restarted.editMessageID != "editing" || restarted.editText != a.editText || restarted.current().Messages[0].Content != "Original message" || restarted.streaming {
				t.Fatal("restart lost the edit, committed it, or triggered regeneration")
			}
			restarted.selectChat("first")
			if restarted.draft != "First draft\n草稿 🙂" || len(restarted.attachments) != 1 || restarted.attachments[0].Data != "ZmlsZQ==" {
				t.Fatal("restart lost another chat's draft or attachment bytes")
			}
			restarted.draft, restarted.attachments = "", nil
			restarted.save()
			state := settingsSavedState(t, restarted)
			if _, exists := state.Drafts["first"]; exists || state.MessageEdit != nil {
				t.Fatal("clearing a draft or leaving an edit retained stale persisted buffers")
			}
		})
	}
}

func TestUpdateRestartBlocksLiveResponseAndSaveFailure(t *testing.T) {
	a := readyDesktopUpdateTestApp(t)
	a.streaming = true
	a.draft = "Keep this draft"
	if a.prepareUpdateRestart() {
		t.Fatal("restart interrupted a live response")
	}
	a.streaming = false
	a.state.Settings.Providers = append(a.state.Settings.Providers, a.state.Settings.Providers[0])
	if a.prepareUpdateRestart() || a.updateError == "" {
		t.Fatal("restart did not stop and report a snapshot save failure")
	}
	if len(settingsSavedState(t, a).Drafts) != 0 {
		t.Fatal("failed restart preparation replaced the previous snapshot")
	}
}
