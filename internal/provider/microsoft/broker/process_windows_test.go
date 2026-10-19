//go:build windows

package broker

import (
	"os/exec"
	"testing"
)

func TestWindowsBrokerKeepsHiddenConsoleHandle(t *testing.T) {
	cmd := exec.Command("copilot")
	hideProcess(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("broker process must not show a console window")
	}
	if cmd.SysProcAttr.CreationFlags&0x08000000 != 0 {
		t.Fatal("CREATE_NO_WINDOW prevents the native broker from using a console parent HWND")
	}
}
