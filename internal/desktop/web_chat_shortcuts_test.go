package desktop

import (
	"context"
	"testing"
	"time"
)

func TestWebChatShortcutsRejectUnownedCalls(t *testing.T) {
	a, _ := doubaoTestApp()
	bridge := &webChatShortcuts{app: a}
	bridge.Invoke(context.Background(), "sidebar")
	bridge.DraftChanged(context.Background())
	if a.sidebarHidden {
		t.Fatal("caller without an owned page executed a command")
	}
	a.webChatCommand("arbitrary-command")
	if a.sidebarHidden {
		t.Fatal("unknown command executed")
	}
	a.webChatCommand("sidebar")
	if !a.sidebarHidden {
		t.Fatal("sidebar command ignored")
	}
	a.webChatCommand("models")
	if !a.webModelToggle {
		t.Fatal("model menu command ignored")
	}
	a.webModelToggle, a.streaming = false, true
	a.webChatCommand("models")
	if a.webModelToggle {
		t.Fatal("model menu opened during native streaming")
	}
}

func TestDoubaoParkedViewKeepsIdentityAndCannotPublish(t *testing.T) {
	a, p := doubaoTestApp()
	p.focusPending = true
	a.parkDoubaoChat()
	if a.doubaoChat != nil || a.doubaoParked != p || p.chatID != "local" || p.focusPending {
		t.Fatal("parking changed identity or retained pending focus")
	}
	a.applyDoubaoSnapshot(p, doubaoSnapshot{URL: "https://www.doubao.com/chat/123", CreatedURL: "https://www.doubao.com/chat/123", Title: "Inactive"})
	if a.current().WebURL != "" {
		t.Fatal("inactive view published state")
	}
	if p.idleTimer == nil {
		t.Fatal("parking did not schedule resource release")
	}
	destroyed := 0
	p.off = append(p.off, func() { destroyed++ })
	// Trigger expiry without waiting five minutes. This verifies cleanup ownership,
	// not WebView2 process exit or immediate operating-system memory reclamation.
	a.expireDoubao(p)
	if a.doubaoParked != nil || destroyed != 1 || p.idleTimer.Stop() {
		t.Fatal("expiry failed to release subscriptions and stop its timer")
	}
	other := &doubaoView{}
	a.doubaoParked = other
	a.expireDoubao(p)
	if a.doubaoParked != other || destroyed != 1 {
		t.Fatal("stale expiry released a replacement view")
	}
	a.doubaoParked = nil
	// Provider removal releases immediately rather than waiting for expiry.
	p = &doubaoView{providerID: "doubao", idleTimer: time.NewTimer(time.Hour)}
	a.doubaoParked = p
	a.closeDoubaoProvider("doubao")
	if a.doubaoParked != nil || p.idleTimer.Stop() {
		t.Fatal("provider cleanup retained cached page or timer")
	}
}
