package desktop

import (
	"strings"
	"testing"
)

func TestWindowsSidebarTitleFontRespectsLanguagePreferences(t *testing.T) {
	for _, tt := range []struct {
		name, region, title string
		display, profile    []string
		wantNoto            bool
	}{
		{name: "Chinese in English UI", region: "en-US", display: []string{"en-US"}, profile: []string{"en-US", "zh-Hans-CN"}, title: "Git 稀疏克隆详解", wantNoto: true},
		{name: "Chinese regional format", region: "zh-CN", display: []string{"en-US"}, title: "字体回退", wantNoto: true},
		{name: "Traditional region takes precedence", region: "zh-TW", display: []string{"zh-Hans"}, title: "字型回退"},
		{name: "Traditional display takes precedence", region: "en-US", display: []string{"en-US", "zh-Hant-HK"}, profile: []string{"zh-Hans"}, title: "字型回退"},
		{name: "Japanese first", region: "en-US", profile: []string{"ja-JP", "zh-Hans"}, title: "東京"},
		{name: "Unknown Han preference", region: "en-US", title: "東京"},
		{name: "Japanese title in Chinese context", region: "zh-CN", title: "Git の使い方"},
		{name: "Traditional title with Bopomofo", region: "zh-CN", title: "字型 ㄅ"},
		{name: "Latin title in Chinese context", region: "zh-CN", title: "Git basics"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{simplifiedSidebarTitles: preferSimplifiedChinese(tt.region, tt.display, func() []string { return tt.profile })}
			font := a.sidebarTitleFont(tt.title)
			if strings.Contains(font, "Noto Sans SC") != tt.wantNoto {
				t.Fatalf("title %q gets font families %q; want Simplified Chinese override: %t", tt.title, font, tt.wantNoto)
			}
		})
	}
}
