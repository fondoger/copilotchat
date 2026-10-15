//go:build windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Windows readers (including scanners and backup software) may open a file
// without FILE_SHARE_DELETE, temporarily preventing a rename over it. Retry
// only these transient errors; never remove the destination to make room.
func replaceSnapshot(source, destination string) error {
	return retrySharingViolation(func() error { return os.Rename(source, destination) })
}

func openSnapshot(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Extended paths keep custom CreateFile opens compatible with long user
	// profile paths. FILE_SHARE_DELETE lets an existing reader finish reading
	// its snapshot while a writer atomically replaces the directory entry.
	if !strings.HasPrefix(absolute, `\\?\`) {
		if strings.HasPrefix(absolute, `\\`) {
			absolute = `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`)
		} else {
			absolute = `\\?\` + absolute
		}
	}
	wide, err := syscall.UTF16PtrFromString(absolute)
	if err != nil {
		return nil, err
	}
	var file *os.File
	err = retrySharingViolation(func() error {
		handle, err := syscall.CreateFile(wide, syscall.GENERIC_READ,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
			nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			return &os.PathError{Op: "open", Path: path, Err: err}
		}
		file = os.NewFile(uintptr(handle), path)
		return nil
	})
	return file, err
}

func retrySharingViolation(operation func() error) error {
	deadline := time.Now().Add(time.Second)
	delay := 2 * time.Millisecond
	for {
		err := operation()
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, syscall.Errno(32)) && !errors.Is(err, syscall.Errno(33)) {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		if delay > remaining {
			delay = remaining
		}
		time.Sleep(delay)
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}
