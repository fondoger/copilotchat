package desktop

import "github.com/egoist/mygo/ui"

// ChatWise uses a compact, filled circular primary action in the composer.
func composerSendButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	background, foreground := ui.Hex("#27272a"), ui.Hex("#ffffff")
	if c.Theme().Dark {
		background, foreground = ui.Hex("#e4e4e7"), ui.Hex("#18181b")
	}
	button := ui.ButtonBase(c).Label(label).Tooltip(label).Size(24, 24).Padding(4).Radius(12).Background(background).TextColor(foreground)
	if button.Hovered() {
		button.Opacity(.85)
	}
	return button.Children(func() { ui.Icon(c, icon).FontSize(16) })
}
