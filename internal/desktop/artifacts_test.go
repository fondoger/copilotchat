package desktop

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func codeBlockTester(a *app, language, source string) *ui.Tester {
	options := markdown.Options{CodeBlock: func(c *ui.Context, block *markdown.CodeBlockView) { a.codeBlock(c, "test-code", block) }}
	return ui.NewTester(func(c *ui.Context) {
		a.theme(c)
		ui.Column(c).Fill().Padding(20).Children(func() { markdown.View(c, "```"+language+"\n"+source+"```", &options) })
	}, 640, 1100)
}

func TestCodeBlockToolbarHoverAndCopyForAllLanguages(t *testing.T) {
	for _, language := range []string{"go", "html", "text"} {
		t.Run(language, func(t *testing.T) {
			tt := codeBlockTester(featureApp(), language, "hello world\n")
			button, ok := tt.Find("Copy Source Code")
			if !ok || button.W != 28 || button.H != 28 {
				t.Fatalf("copy control geometry: %+v", button)
			}
			region := image.Rect(int(button.X), int(button.Y), int(button.X+button.W), int(button.Y+button.H))
			pixels := func() []byte {
				out := image.NewRGBA(image.Rect(0, 0, region.Dx(), region.Dy()))
				draw.Draw(out, out.Bounds(), tt.Image(), region.Min, draw.Src)
				return out.Pix
			}
			before := pixels()
			body, ok := tt.Find("hello world")
			if !ok {
				t.Fatal("code body missing")
			}
			tt.Move(body.X+4, body.Y+4)
			if bytes.Equal(before, pixels()) {
				t.Fatal("hover did not reveal code toolbar")
			}
			settingsClick(t, tt, "Copy Source Code")
			if tt.Clipboard() != "hello world\n" {
				t.Fatalf("wrong copied code: %q", tt.Clipboard())
			}
		})
	}
}

func TestHTMLCodeCollapseExpandAndPreviewRetainSource(t *testing.T) {
	var code strings.Builder
	for i := 1; i <= 42; i++ {
		fmt.Fprintf(&code, "<p>line %02d</p>\n", i)
	}
	a := featureApp()
	tt := codeBlockTester(a, "html", code.String())
	if !tt.HasText("<p>line 20</p>") || tt.HasText("<p>line 21</p>") || !tt.HasText("22 more lines") {
		t.Fatalf("long HTML did not show the first 20 lines: %q", tt.Texts())
	}
	settingsClick(t, tt, "22 more lines")
	if !tt.HasText("<p>line 42</p>") || tt.HasText("22 more lines") {
		t.Fatal("expand did not show complete HTML")
	}
	settingsClick(t, tt, "Copy Source Code")
	if tt.Clipboard() != code.String() {
		t.Fatal("expanded HTML copy changed source")
	}
	settingsClick(t, tt, "Run HTML preview")
	if a.preview == nil || a.preview.source != code.String() {
		t.Fatal("preview lost source")
	}
	settingsClick(t, tt, "Close preview")
	if a.preview != nil || !tt.HasText("<p>line 42</p>") {
		t.Fatal("closing preview lost expanded state or retained WebView")
	}
	settingsClick(t, tt, "Collapse code")
	if !tt.HasText("22 more lines") || tt.HasText("<p>line 42</p>") {
		t.Fatal("collapse did not restore snippet")
	}
}
