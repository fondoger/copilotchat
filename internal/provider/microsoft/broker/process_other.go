//go:build !windows

package broker

import "os/exec"

func hideProcess(cmd *exec.Cmd) {}
