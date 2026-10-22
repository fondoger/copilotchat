package desktop

import (
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/localproxy"
)

func TestGatewayInspectionTogglePersistsWithoutRestart(t *testing.T) {
	a := newSettingsTestApp(t)
	if a.state.Settings.GatewayInspection {
		t.Fatal("inspection enabled by default")
	}
	a.forwarder = localproxy.New(localproxy.DefaultKey)
	t.Cleanup(a.stopForwarding)
	server := a.forwarder
	tester := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.forwardingSettings(c) }) }, 800, 800)
	code := func() int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/__viz/", nil)
		r.Host, r.RemoteAddr = "127.0.0.1:39321", "127.0.0.1:12345"
		a.forwarder.ServeHTTP(w, r)
		return w.Code
	}
	settingsClick(t, tester, "Enable HTTP request inspection")
	if !settingsSavedState(t, a).Settings.GatewayInspection || code() != 200 || a.forwarder != server {
		t.Fatal("enabling inspection was not persisted or restarted forwarding")
	}
	settingsClick(t, tester, "Enable HTTP request inspection")
	if settingsSavedState(t, a).Settings.GatewayInspection || code() != 404 || a.forwarder != server {
		t.Fatal("disabling inspection left dashboard active or restarted forwarding")
	}
}

func TestForwardingQuitAndTrayConfirmation(t *testing.T) {
	a := newSettingsTestApp(t)
	a.state.Settings.LocalForwarding = true
	a.state.Settings.StayInTray = true
	calls := 0
	a.confirmForwarding = func(title, detail, action string) bool {
		calls++
		if !strings.Contains(detail, "forwarding") {
			t.Fatal("missing consequence")
		}
		return false
	}
	if a.allowForwardingQuit() {
		t.Fatal("cancelled exit allowed")
	}
	a.state.Settings.StayInTray = false
	a.changeStayInTray()
	if !a.state.Settings.StayInTray || calls != 2 {
		t.Fatal("tray cancellation ignored")
	}
	a.confirmForwarding = func(string, string, string) bool { return true }
	if !a.allowForwardingQuit() || !a.forwardQuitApproved {
		t.Fatal("confirmed quit rejected")
	}
	a.confirmForwarding = func(string, string, string) bool { t.Fatal("duplicate quit prompt"); return false }
	if !a.allowForwardingQuit() {
		t.Fatal("approved quit lost")
	}
	a.state.Settings.LocalForwarding = false
	a.forwardQuitApproved = false
	if !a.allowForwardingQuit() {
		t.Fatal("disabled forwarding blocked exit")
	}
}

func TestForwardingKeepsKeyAcrossBindFailureAndRestart(t *testing.T) {
	a := newSettingsTestApp(t)
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { occupied.Close() })
	_, port, _ := net.SplitHostPort(occupied.Addr().String())
	a.state.Settings.LocalPort, _ = strconv.Atoi(port)
	a.state.Settings.LocalForwarding = true
	a.startForwarding()
	if a.forwarder != nil || a.forwardError == "" {
		t.Fatal("occupied port was not reported")
	}
	key := a.state.Settings.LocalKey
	if key != localproxy.DefaultKey {
		t.Fatal("local key is not readable")
	}
	saved, err := a.storage.Load()
	if err != nil || saved.Settings.LocalKey != key {
		t.Fatal("local key was not persisted before binding")
	}
	occupied.Close()
	a.startForwarding()
	if a.forwarder == nil || a.forwardError != "" {
		t.Fatal(a.forwardError)
	}
	t.Cleanup(a.stopForwarding)
	a.stopForwarding()
	a.state = saved
	a.startForwarding()
	if a.forwarder == nil || a.state.Settings.LocalKey != key {
		t.Fatal("restart changed the local key")
	}
}

func TestForwardingRejectsInvalidPort(t *testing.T) {
	a := newSettingsTestApp(t)
	a.state.Settings.LocalForwarding = true
	a.agentSetup.portInitialized = true
	a.agentSetup.portText = "invalid"
	a.startForwarding()
	if a.forwarder != nil || a.forwardError == "" {
		t.Fatal("invalid port silently accepted")
	}
}
