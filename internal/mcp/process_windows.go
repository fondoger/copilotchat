//go:build windows

package mcp

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func terminateProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// taskkill /T also closes child processes created by package runners.
	// The arguments are fixed and the PID is numeric; no shell is involved.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	kill := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	configureProcess(kill)
	_ = kill.Run()
	_ = cmd.Process.Kill()
}
