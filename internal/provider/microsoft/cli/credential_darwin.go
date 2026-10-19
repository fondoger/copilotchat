//go:build darwin

package cli

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

func readCredential(ctx context.Context, account string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", "copilot-cli", "-a", account, "-w").Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", errors.New("Copilot CLI work credential is unavailable in Keychain")
	}
	return strings.TrimSpace(string(data)), nil
}

func hideProcess(*exec.Cmd) {}
