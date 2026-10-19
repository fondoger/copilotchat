//go:build !windows

package wam

import (
	"context"
	"errors"
)

func acquire(context.Context, uintptr, string) (string, string, error) {
	return "", "", errors.New("native Microsoft sign-in requires Windows; use Copilot CLI sign-in on this platform")
}
