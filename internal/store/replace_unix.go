//go:build !windows

package store

import "os"

func replaceSnapshot(source, destination string) error {
	return os.Rename(source, destination)
}

func openSnapshot(path string) (*os.File, error) { return os.Open(path) }
