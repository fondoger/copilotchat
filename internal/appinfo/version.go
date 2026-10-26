package appinfo

import "github.com/egoist/mygo"

func Version() string {
	if version := mygo.App.Version(); version != "" {
		return version
	}
	return "dev"
}

func UserAgent() string { return "CopilotChatNative/" + Version() }
