//go:build !windows && !darwin

package cli

import (
	"context"
	"errors"
	"os/exec"
)

func readCredential(ctx context.Context, _ string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", errors.New("Copilot CLI credential reuse is supported on Windows and macOS")
}
func hideProcess(*exec.Cmd) {}
