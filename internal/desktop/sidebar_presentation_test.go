package desktop

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSidebarRangeHidesHoverActionsUntilOrdinarySelection(t *testing.T) {
	a := newSidebarTestApp(t)
	tt := ui.NewTester(a.sidebar, 300, 600)
	settingsClick(t, tt, "Current conversation")
	if err := tt.ClickWith(ui.Shift, "Yesterday conversation"); err != nil {
		t.Fatal(err)
	}
	row, _ := tt.Find("Another conversation")
	tt.Move(row.X+48, row.Y+row.H/2)
	for _, label := range []string{"Favorite Another conversation", "Delete Another conversation"} {
		if _, found := tt.Find(label); found {
			t.Fatalf("multi-selection exposed %s", label)
		}
	}
	settingsClick(t, tt, "Another conversation")
	if _, found := tt.Find("Favorite Another conversation"); !found {
		t.Fatal("ordinary selection did not restore hover actions")
	}
}

func TestSidebarFavoritesAnimateBothDirections(t *testing.T) {
	a := newSidebarTestApp(t)
	tt := ui.NewTester(a.sidebar, 300, 600)
	tt.Frame()
	for _, favorite := range []bool{true, false} {
		before := append([]byte(nil), tt.Image().Pix...)
		a.conversation("second").Favorite = favorite
		tt.Frame()
		if !tt.FramePending() {
			t.Fatal("favorite reorder did not schedule animation frames")
		}
		tt.Advance(80 * time.Millisecond)
		middle := append([]byte(nil), tt.Image().Pix...)
		tt.Advance(2 * time.Second)
		after := tt.Image().Pix
		if bytes.Equal(before, middle) || bytes.Equal(middle, after) {
			t.Fatal("favorite reorder jumped to its destination instead of moving through intermediate frames")
		}
		current, _ := tt.Find("Current conversation")
		second, _ := tt.Find("Another conversation")
		if (second.Y < current.Y) != favorite {
			t.Fatal("favorite reorder settled in the wrong order")
		}
	}
}
