package desktop

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// Only a user-started preview owns a WebView. Switching chats or previews
// destroys it, so scrolling through history cannot accumulate browser processes.
type artifactPreview struct {
	key, chatID, source, err string
	view                     *mygo.WebView
}

func (a *app) closePreview() {
	if a.preview != nil && a.preview.view != nil {
		a.preview.view.Destroy()
	}
	a.preview = nil
}

func previewLanguage(language string) bool {
	switch strings.ToLower(language) {
	case "html", "htm", "svg":
		return true
	}
	return false
}

// Model-generated code lives in an opaque-origin sandbox, never in the trusted
// MyGo host page. Inline JS/CSS work, while network, native bridge, local files,
// permissions, popups and navigation outside the preview remain unavailable.
func artifactDocument(source string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; frame-src about:; connect-src 'none'; base-uri 'none'; form-action 'none'"><style>html,body{margin:0;width:100%;height:100%;overflow:hidden;background:white}iframe{border:0;width:100%;height:100%;display:block}</style></head><body><iframe title="HTML preview" sandbox="allow-scripts" referrerpolicy="no-referrer" srcdoc="` + html.EscapeString(source) + `"></iframe></body></html>`
}

func (a *app) openPreview(key, source string) {
	a.closePreview()
	p := &artifactPreview{key: key, chatID: a.state.ActiveID, source: source}
	a.preview = p
	if a.win == nil {
		p.err = "HTML preview is available in the desktop app."
		return
	}
	v, err := a.win.NewWebView(mygo.WebViewOptions{Page: mygo.PageOptions{DevTools: mygo.DevToolsDisabled}, BackgroundColor: "#ffffff"})
	if err != nil {
		p.err = "Could not open HTML preview: " + err.Error()
		return
	}
	p.view = v
	page := v.Page()
	page.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
	page.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
	page.OnWillDownload(func(e *mygo.DownloadEvent) { e.PreventDefault() })
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if e.URL != "about:blank" && e.URL != "about:srcdoc" {
			e.PreventDefault()
		}
	})
	page.LoadHTML(artifactDocument(source), "")
}

func (a *app) codeBlock(c *ui.Context, messageID string, b *markdown.CodeBlockView) {
	key := fmt.Sprintf("%s:%d", messageID, b.Index)
	source := b.Code()
	lineCount := strings.Count(strings.TrimSuffix(source, "\n"), "\n") + 1
	canPreview := previewLanguage(b.Language)
	codeSize := float32(a.state.Settings.FontSize) * .8
	state := ui.Local(c.Root(), "code-state:"+key, func() codeBlockState { return codeBlockState{collapsed: canPreview} })
	collapsed := state.collapsed && lineCount > 30
	background, header, border := ui.Hex("#ffffff"), ui.Hex("#f2edec").Alpha(.6), ui.Hex("#e4e4e7")
	if c.Theme().Dark {
		background, header, border = ui.Hex("#18181b").Alpha(.5), ui.Hex("#2a2a2a"), ui.Hex("#52525b").Alpha(.8)
	}
	box := ui.Column(c.Key(key)).Label("Code block, "+b.Language).FillWidth().Radius(8).Border(1, border).Background(background).Clip()
	_, _, over := box.PointerPosition()
	active := a.preview != nil && a.preview.key == key
	box.Children(func() {
		ui.Row(c).Height(40).Shrink(0).PaddingX(16).Gap(6).Background(header).BorderWidth(0, 0, 1, 0).BorderColor(border).Children(func() {
			ui.Text(c, strings.ToLower(b.Language)).FontSize(codeSize).FontWeight(500)
			ui.Spacer(c)
			bar := ui.Row(c.Key("code-toolbar")).Gap(0)
			if !over && !box.FocusWithin() {
				bar.Opacity(0)
			}
			bar.Children(func() {
				if lineCount > 30 && !active {
					icon, label := iCodeCollapse, "Collapse code"
					if collapsed {
						icon, label = iCodeExpand, "Expand code"
					}
					codeIconButton(c, icon, label, codeSize).OnClick(func() { state.collapsed = !state.collapsed })
				}
				if canPreview {
					icon, label := iCodePlay, "Run HTML preview"
					if active {
						icon, label = iCodePlayFill, "Close preview"
					}
					button := codeIconButton(c, icon, label, codeSize).Disabled(b.Open)
					if active {
						button.TextColor(ui.Hex("#3b82f6"))
					}
					button.OnClick(func() {
						if active {
							a.closePreview()
						} else {
							a.openPreview(key, source)
						}
					})
				}
				copyIcon := iCopy
				if !state.copiedAt.IsZero() && c.Now().Sub(state.copiedAt) < time.Second {
					copyIcon = iCodeCopied
				}
				codeIconButton(c, copyIcon, "Copy Source Code", codeSize).Disabled(b.Open).OnClick(func() {
					c.WriteClipboard(source)
					state.copiedAt = c.Now()
					c.After(time.Second)
				})
			})
		})
		if !active {
			ui.Column(c.Key("code-body")).FillWidth().Children(func() {
				if canPreview || collapsed {
					visibleSource := source
					if collapsed {
						visibleSource = strings.Join(strings.Split(source, "\n")[:20], "\n")
					}
					plainCodeBody(c, visibleSource, codeSize)
				} else {
					b.Body(c).Padding(12, 0, 0, 0).FontSize(codeSize).LineHeight(1.5)
				}
				if collapsed {
					codeExpandOverlay(c, lineCount-20, func() { state.collapsed = false })
				}
			})
		}
		if active {
			if p := a.preview; p.err != "" {
				ui.Text(c, p.err).Padding(12).FontSize(12).TextColor(c.Theme().Danger)
			} else {
				ui.WebView(c, p.view).Label("HTML preview").Height(420).FillWidth()
			}
		}
	})
}

type codeBlockState struct {
	collapsed bool
	copiedAt  time.Time
}

func codeIconButton(c *ui.Context, icon *ui.SVG, label string, size float32) ui.Element {
	b := ui.ButtonBase(c.Key(label)).Label(label).Tooltip(label).Size(28, 28).Padding(0).Radius(6)
	if b.Hovered() {
		fill := ui.Hex("#e4e4e7")
		if c.Theme().Dark {
			fill = ui.Hex("#3f3f46")
		}
		b.Background(fill)
	}
	return b.Children(func() { ui.Icon(c, icon).Size(size, size) })
}

// HTML stays plain text even after expansion. Small stable chunks avoid one
// expensive text layout and do not create a syntax-highlighting state.
func plainCodeBody(c *ui.Context, source string, size float32) {
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	ui.ScrollHorizontal(c).FillWidth().Children(func() {
		ui.Column(c).Padding(16).Shrink(0).Font("Consolas").FontSize(size).LineHeight(1.5).Selectable().Children(func() {
			for start := 0; start < len(lines); start += 32 {
				ui.Text(c.Key(fmt.Sprint(start)), strings.Join(lines[start:min(start+32, len(lines))], "\n")).NoWrap()
			}
		})
	})
}

func codeExpandOverlay(c *ui.Context, remaining int, expand func()) {
	fill, pill, border, ink := ui.Hex("#ffffff"), ui.Hex("#ffffff").Alpha(.8), ui.Hex("#e4e4e7").Alpha(.5), ui.Hex("#71717a")
	if c.Theme().Dark {
		fill, pill, border, ink = ui.Hex("#18181b"), ui.Hex("#27272a").Alpha(.8), ui.Hex("#3f3f46").Alpha(.5), ui.Hex("#a1a1aa")
	}
	ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Height(96).Justify(ui.End).AlignItems(ui.Center).Padding(0, 0, 8, 0).
		LinearGradient(ui.LinearGradient{From: fill.Alpha(0), To: fill.Alpha(.95), Angle: 180}).Children(func() {
		label := fmt.Sprintf("%d more lines", remaining)
		button := ui.ButtonBase(c).Label(label).Padding(2, 8).Gap(4).Radius(99).Border(1, border).Background(pill).TextColor(ink)
		if button.Hovered() {
			button.Background(fill)
		}
		button.Children(func() {
			ui.Icon(c, iCodeExpand).Size(11.2, 11.2)
			ui.Text(c, label).FontSize(12).FontWeight(500)
		}).OnClick(expand)
	})
}
