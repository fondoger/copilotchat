package desktop

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Most SVGs come from the owner's ChatWise installation; additional icons are
// obtained from their upstream projects. Geometry stays unchanged; CSS-mask
// black paint becomes currentColor for native tinting. Sources and licenses
// are recorded beside the assets.
//
//go:embed assets/icons/*.svg
var iconAssets embed.FS

func loadIcon(name string) *ui.SVG {
	data, err := iconAssets.ReadFile("assets/icons/" + name + ".svg")
	if err != nil {
		panic(fmt.Sprintf("load UI icon %s: %v", name, err))
	}
	return ui.MustParseSVG(data)
}

var (
	iMenu         = loadIcon("tabler-menu-2")
	iPanel        = loadIcon("chatwise-sidebar-open")
	iPanelClosed  = loadIcon("chatwise-sidebar-closed")
	iNew          = loadIcon("custom-edit")
	iChat         = loadIcon("copilot-mono")
	iEmptyChat    = loadIcon("bi-chat-square-quote")
	iSettings     = loadIcon("tabler-settings")
	iSearch       = loadIcon("fe-search")
	iNativeSearch = loadIcon("lucide-globe")
	iAttach       = loadIcon("ri-attachment-2")
	iArrow        = loadIcon("ri-arrow-up-line")
	iStop         = loadIcon("bi-stop-fill")
	iCopy         = loadIcon("mingcute-copy-2-line")
	iRetry        = loadIcon("mingcute-refresh-3-line")
	iMore         = loadIcon("tabler-dots")
	iClose        = loadIcon("tabler-x")
	iChevron      = loadIcon("tabler-chevron-right")
	iStar         = loadIcon("tabler-star")
)

func iconButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Size(28, 28).Padding(0).Radius(6).Background(ui.Transparent).TextColor(c.Theme().Text.Alpha(.75))
	if b.Hovered() {
		if c.Theme().Dark {
			b.Background(ui.Hex("#3f3f46").Alpha(.7))
		} else {
			b.Background(ui.RGBA(0, 0, 0, .08))
		}
	}
	return b.Children(func() { ui.Icon(c, icon).Size(17.04, 17.04) })
}

func composerButton(c *ui.Context, icon *ui.SVG, label string, iconSize float32, text string) ui.Element {
	ink, hover := ui.Hex("#52525b"), ui.Hex("#e4e4e7")
	if c.Theme().Dark {
		ink, hover = ui.Hex("#e4e4e7"), ui.Hex("#52525b")
	}
	b := ui.ButtonBase(c).Label(label).Tooltip(label).MinWidth(24).Height(24).Padding(0, 3).Gap(4).Radius(6).Background(ui.Transparent).TextColor(ink)
	if text != "" {
		b.PaddingX(6)
	}
	if b.Hovered() {
		b.Background(hover)
	}
	return b.Children(func() {
		ui.Icon(c, icon).Size(iconSize, iconSize)
		if text != "" {
			ui.Text(c, text).FontSize(12)
		}
	})
}

func trayPNG() []byte {
	im := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	ink := color.NRGBA{55, 65, 81, 255}
	white := color.NRGBA{255, 255, 255, 255}
	for y := 4; y < 26; y++ {
		for x := 3; x < 29; x++ {
			if (x > 6 && x < 25) || (y > 7 && y < 22) {
				im.SetNRGBA(x, y, ink)
			}
		}
	}
	for y := 24; y < 30; y++ {
		for x := 6; x < 12-(y-24); x++ {
			im.SetNRGBA(x, y, ink)
		}
	}
	for _, cx := range []int{10, 21} {
		for y := 12; y < 18; y++ {
			for x := cx - 1; x <= cx+1; x++ {
				im.SetNRGBA(x, y, white)
			}
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return b.Bytes()
}

func (a *app) syncTray() {
	if !a.state.Settings.StayInTray {
		if a.tray != nil {
			a.tray.Destroy()
			a.tray = nil
		}
		return
	}
	if a.tray != nil {
		return
	}
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Open Copilot Chat", Click: func(*mygo.MenuItem, *mygo.Window) { a.show() }},
		{Label: "New Chat", Click: func(*mygo.MenuItem, *mygo.Window) { a.newChat(); a.show() }},
		{Label: "Settings", Click: func(*mygo.MenuItem, *mygo.Window) { a.show(); a.openSettings("General") }},
		mygo.Separator(),
		{Label: "Quit", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
	opts := mygo.TrayOptions{Icon: trayPNG(), ToolTip: "Copilot Chat"}
	if runtime.GOOS == "linux" {
		opts.Menu = menu
	}
	t, err := mygo.NewTray(opts)
	if err != nil {
		a.status = "Could not create tray icon: " + err.Error()
		a.state.Settings.StayInTray = false
		a.saveSettings()
		return
	}
	a.tray = t
	t.OnClick(a.show)
	t.OnRightClick(func() { t.SetMenu(menu); t.PopUpMenu(); t.SetMenu(nil) })
}
