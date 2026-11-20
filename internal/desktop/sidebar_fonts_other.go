//go:build !windows

package desktop

func systemPrefersSimplifiedSidebarTitles() bool { return false }

func (*app) sidebarTitleFont(string) string { return "" }
