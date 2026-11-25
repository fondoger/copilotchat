//go:build !windows

package wam

import (
	"context"
	"errors"
)

func acquire(context.Context, uintptr, registration, string) (string, string, error) {
	return "", "", errors.New("Web Account Manager sign-in requires Windows")
}
