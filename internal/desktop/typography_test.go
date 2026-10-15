package desktop

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
)

var chineseTypographySamples = []string{
	"问候与对话开启",
	"Git稀疏克隆详解",
	"车内后排触摸阅读灯",
}

// This regression reproduces the original failure with an English UI locale:
// a Chinese title used several fallback fonts and weights within one sentence.
// With an explicit CJK family, changing the UI locale must not change those
// glyphs. Comparing rendered pixels also catches changes to fallback selection
// that a check of the configured family string would miss.
func TestChineseTypographyStableAcrossUILocales(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows DirectWrite fallback regression")
	}
	hasChineseFont := false
	for _, f := range ui.FontFamilies() {
		if f.Name == "Microsoft YaHei UI" || f.Name == "Microsoft YaHei" {
			hasChineseFont = true
			break
		}
	}
	if !hasChineseFont {
		t.Skip("Chinese Windows font is not installed")
	}
	for _, scale := range []float32{1, 1.25, 2} {
		t.Run(fmt.Sprintf("scale-%g", scale), func(t *testing.T) {
			en := ui.NewTester(typographySampleView(appFontFamily(), "en-US"), 600, 250)
			zh := ui.NewTester(typographySampleView(appFontFamily(), "zh-Hans"), 600, 250)
			en.SetScale(scale)
			zh.SetScale(scale)
			if !bytes.Equal(en.Image().Pix, zh.Image().Pix) {
				t.Fatal("Chinese typography changes with UI locale despite explicit CJK fallback")
			}
		})
	}
}

func typographySampleView(family, lang string) func(*ui.Context) {
	return func(c *ui.Context) {
		theme := ui.LightTheme()
		theme.Font = family
		c.SetTheme(theme)
		ui.Column(c).Fill().Background(ui.Hex("#e3e2e6")).Padding(16).Gap(14).Lang(lang).Children(func() {
			for _, size := range []float32{13, 14, 16} {
				ui.Column(c).Gap(3).Children(func() {
					for _, title := range chineseTypographySamples {
						ui.Text(c, title).FontSize(size).FontWeight(500)
					}
				})
			}
		})
	}
}

// COPILOT_CHAT_SCREENSHOTS writes the same-size before/after comparison used
// to inspect this fix, including the 13px size that first exposed the issue.
func TestChineseTypographyComparison(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows DirectWrite fallback comparison")
	}
	view := func(c *ui.Context) {
		c.SetTheme(ui.LightTheme())
		ui.Row(c).Fill().Background(ui.Hex("#e3e2e6")).Padding(20).Gap(40).Children(func() {
			for _, family := range []string{"", appFontFamily()} {
				ui.Column(c).Width(360).Gap(18).Lang("en-US").Children(func() {
					label := "Before: default system fallback"
					if family != "" {
						label = "After: explicit Chinese fallback"
					}
					ui.Text(c, label).FontSize(14).Bold()
					for _, size := range []float32{13, 14, 16} {
						ui.Column(c).Gap(6).Children(func() {
							ui.Text(c, fmt.Sprintf("%g px / requested weight 500", size)).FontSize(12).TextColor(ui.Hex("#71717a"))
							for _, title := range chineseTypographySamples {
								ui.Text(c, title).Font(family).FontSize(size).FontWeight(500)
							}
						})
					}
				})
			}
		})
	}
	tt := ui.NewTester(view, 800, 420)
	writeViewScreenshot(t, tt, "typography-comparison.png")
}
