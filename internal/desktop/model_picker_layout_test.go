package desktop

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestModelPickerLongListKeepsPinsOutsideScrollbar(t *testing.T) {
	for _, size := range []struct{ width, height int }{{1060, 720}, {720, 480}, {320, 480}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			a := providerTestApp(t)
			a.provider("work").Disabled = true
			models := make([]provider.Model, 40)
			for i := range models {
				models[i] = provider.Model{ID: fmt.Sprintf("model-%02d", i), Name: fmt.Sprintf("Model %02d with an extended display name", i), Vision: true, Reasoning: true, Tools: true}
			}
			a.providerState(store.DefaultProviderID).models = models
			a.state.Settings.Model = models[0].ID
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Row(c).FillWidth().Height(50).Children(func() { a.modelPicker(c) })
			}, size.width, size.height)
			settingsClick(t, tt, "Choose model")
			assertInWindow(t, tt, "Model options", float32(size.width), float32(size.height))
			panel, _ := tt.Find("Model options")
			if want := min(float32(360), float32(size.width)-24); math.Abs(float64(panel.W-want)) > 1 {
				t.Fatalf("popup width = %.1f, want %.1f", panel.W, want)
			}
			list, ok := tt.Find("Model list")
			if !ok {
				t.Fatal("model list is missing")
			}
			pin, ok := tt.Find("Pin " + models[0].Name + " from Personal")
			if !ok || pin.W < 28 || pin.X+pin.W > list.X+list.W-12 {
				t.Fatalf("pin hit area overlaps the scrollbar track: pin=%+v list=%+v", pin, list)
			}
			// Hover shows the scrollbar. The pin's rightmost pixel must still
			// click the button, rather than starting a scroll-track drag.
			tt.Move(pin.X+pin.W-1, pin.Y+pin.H/2)
			writeViewScreenshot(t, tt, fmt.Sprintf("model-picker-%dx%d.png", size.width, size.height))
			tt.ClickAt(pin.X+pin.W-1, pin.Y+pin.H/2)
			if !slices.Contains(a.provider(store.DefaultProviderID).PinnedModels, models[0].ID) {
				t.Fatal("scrollbar intercepted the pin button click")
			}
			// The narrower popup must preserve access to the final model.
			tt.Scroll(list.X+list.W/2, list.Y+list.H/2, 0, 10000)
			last := "Choose " + models[len(models)-1].Name + " from Personal"
			assertInWindow(t, tt, last, float32(size.width), float32(size.height))
			settingsClick(t, tt, last)
			if a.state.Settings.Model != models[len(models)-1].ID {
				t.Fatal("last model could not be selected after scrolling")
			}
		})
	}
}
