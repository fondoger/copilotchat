package desktop

import "runtime"

// appFontFamily keeps Latin text in the platform UI font while giving Chinese
// an explicit, consistent fallback on Windows. DirectWrite's default fallback
// for an English UI can split a single simplified-Chinese title between
// Microsoft JhengHei UI and Yu Gothic UI, which also match weight 500 to
// different weights (400 and 600). An ordered family list avoids that mixture
// without changing the app's language or replacing the native text renderer.
func appFontFamily() string {
	if runtime.GOOS == "windows" {
		return "Segoe UI, Microsoft YaHei UI, Microsoft YaHei"
	}
	return ""
}

// chatTitleFontFamily uses a real CJK medium face where Windows provides one.
// At weight 500, YaHei UI still selects Regular (400), while Segoe UI selects
// Semibold (600). Noto Sans SC has Medium (500), balancing mixed-language
// titles without changing Latin glyphs or jumping Chinese to YaHei Bold (700).
// Systems without Noto retain the consistent YaHei fallback.
func chatTitleFontFamily() string {
	if runtime.GOOS == "windows" {
		return "Segoe UI, Noto Sans SC, Microsoft YaHei UI, Microsoft YaHei"
	}
	return appFontFamily()
}
