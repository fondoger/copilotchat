package desktop

import (
	"unicode"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/text/language"
)

// Keep the Windows UI face for Latin text. For Chinese in a Simplified Chinese
// context, prefer Noto's real medium weight when the font is installed.
const windowsSidebarTitleFont = "Segoe UI Variable Text, Segoe UI"

func systemPrefersSimplifiedSidebarTitles() bool {
	return preferSimplifiedChinese(mygo.App.Region().Locale, mygo.App.PreferredLanguages(), windowsProfileLanguages)
}

func (a *app) sidebarTitleFont(title string) string {
	if a.simplifiedSidebarTitles {
		var han bool
		for _, r := range title {
			// Titles have no language tag. Keep the system's CJK fallback for
			// clearly non-Simplified text; Han alone cannot identify its language.
			if unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Bopomofo) {
				return windowsSidebarTitleFont
			}
			han = han || unicode.Is(unicode.Han, r)
		}
		if han {
			return windowsSidebarTitleFont + ", Noto Sans SC"
		}
	}
	return windowsSidebarTitleFont
}

func preferSimplifiedChinese(region string, display []string, profile func() []string) bool {
	// Use the same preference order as Windows Han fallback in MyGo. Display
	// languages omit languages the user added without a display language pack.
	if script := firstHanScript([]string{region}); script != "" {
		return script == "Hans"
	}
	if script := firstHanScript(display); script != "" {
		return script == "Hans"
	}
	return firstHanScript(profile()) == "Hans"
}

func firstHanScript(tags []string) string {
	for _, tag := range tags {
		script, _ := language.Make(tag).Script()
		switch script.String() {
		case "Hans", "Hant", "Hani", "Jpan", "Kore", "Hira", "Kana", "Hang", "Bopo", "Hanb":
			return script.String()
		}
	}
	return ""
}

func windowsProfileLanguages() []string {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Control Panel\International\User Profile`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	langs, _, _ := key.GetStringsValue("Languages")
	return langs
}
