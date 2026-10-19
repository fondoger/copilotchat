//go:build windows

package broker

import (
	"os/exec"
	"syscall"
)

func hideProcess(cmd *exec.Cmd) {
	// Keep a hidden console window: MSAL's native broker uses the console HWND
	// when the caller does not supply a parent handle. CREATE_NO_WINDOW removes
	// that HWND and can leave the broker waiting without displaying its dialog.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
