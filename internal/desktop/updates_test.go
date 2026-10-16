package desktop

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	// Render restart protection, but never invoke the real application relaunch.
	a.draft = "An unsent message"
	tester.Frame()
	if !tester.HasText("Send or clear your draft before restarting.") {
		t.Fatal("ready update did not explain why restarting is blocked")
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
	if !tester.HasText("Development builds cannot update themselves.") {
		t.Fatal("development build update limitation is missing")
	}
	assertInWindow(t, tester, "Enable automatic updates", 900, 620)
	assertInWindow(t, tester, "Update check frequency", 900, 620)
	assertInWindow(t, tester, "Check for updates", 900, 620)
	settingsClick(t, tester, "Check for updates")
	if checks.Load() != 0 || a.updateSnapshot().Phase != appupdate.Disabled {
		t.Fatal("development build attempted an update check")
	}
}

func TestUpdateRestartProtectsWorkWithoutBlockingStaleCurrentDraft(t *testing.T) {
	image := store.Attachment{Name: "unsent.png", MIME: "image/png", Data: testPreviewPNG}
	for _, tc := range []struct {
		name, want string
		setup      func(*app)
	}{
		{"streaming", "current response", func(a *app) { a.streaming = true }},
		{"editing", "message edit", func(a *app) { a.editMessageID = "editing" }},
		{"current text", "your draft", func(a *app) { a.draft = "Unsent text" }},
		{"current image", "your draft", func(a *app) { a.attachments = []store.Attachment{image} }},
		{"other chat text", "other chats", func(a *app) { a.drafts["other"] = chatDraft{Text: "Other unsent text"} }},
		{"other chat image", "other chats", func(a *app) { a.drafts["other"] = chatDraft{Attachments: []store.Attachment{image}} }},
		{"empty drafts", "", func(a *app) { a.draft = " \n\t"; a.drafts["other"] = chatDraft{Text: " \n\t"} }},
		{"stale current draft", "", func(a *app) {
			a.drafts["current"] = chatDraft{Text: "Old draft", Attachments: []store.Attachment{image}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{state: store.DefaultState(), drafts: map[string]chatDraft{}}
			a.state.ActiveID = "current"
			tc.setup(a)
			got := a.updateRestartBlockReason()
			if (tc.want == "" && got != "") || (tc.want != "" && !strings.Contains(got, tc.want)) {
				t.Fatalf("restart protection = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeletingOtherChatClearsDraftAndUpdateRestartBlock(t *testing.T) {
	a := newSettingsTestApp(t)
	a.state.Conversations = []store.Conversation{
		{ID: "first", Title: "First chat"},
		{ID: "second", Title: "Second chat"},
	}
	a.state.ActiveID = "first"
	a.draft = "Unsent text in the first chat"
	a.attachments = []store.Attachment{{Name: "unsent.png", MIME: "image/png", Data: testPreviewPNG}}
	a.selectChat("second")
	if !strings.Contains(a.updateRestartBlockReason(), "other chats") {
		t.Fatal("cached draft in the first chat did not block restarting")
	}

	a.deleteID, a.deleteOpen = "first", true
	tester := ui.NewTester(a.dialogs, 900, 620)
	settingsClick(t, tester, "Delete")
	if a.conversation("first") != nil || a.state.ActiveID != "second" {
		t.Fatal("delete dialog did not remove only the first chat")
	}
	if _, exists := a.drafts["first"]; exists {
		t.Fatal("deleted chat retained its cached draft")
	}
	if reason := a.updateRestartBlockReason(); reason != "" {
		t.Fatalf("deleted chat still blocks restarting: %s", reason)
	}
}
