package appinfo

import (
	"strings"

	"github.com/egoist/mygo"
)

func Version() string {
	if version := mygo.App.Version(); version != "" {
		return version
	}
	return "debug"
}

func Channel() string {
	version := Version()
	if version == "debug" {
		return "debug"
	}
	if strings.Contains(version, "-dev.") {
		return "dev"
	}
	return "stable"
}

func UserAgent() string { return "CopilotChatNative/" + Version() }
