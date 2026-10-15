//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveRetriesTemporaryWindowsReader(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := DefaultState()
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	// os.Open does not grant FILE_SHARE_DELETE on Windows, like many
	// external scanners. The previous snapshot must remain readable.
	reader, err := os.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	closed := make(chan struct{})
	go func() { time.Sleep(60 * time.Millisecond); _ = reader.Close(); close(closed) }()
	state.ActiveID = "new-snapshot"
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	<-closed
	got, err := s.Load()
	if err != nil || got.ActiveID != state.ActiveID {
		t.Fatalf("replacement after reader release: %#v %v", got, err)
	}
}

func TestFailedWindowsReplacementPreservesSnapshot(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := DefaultState()
	state.ActiveID = "previous"
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	state.ActiveID = "replacement"
	if err := s.Save(state); err == nil {
		t.Fatal("replacement unexpectedly succeeded despite a non-shareable reader")
	}
	_ = reader.Close()
	got, err := s.Load()
	if err != nil || got.ActiveID != "previous" {
		t.Fatalf("failed save did not preserve old state: %#v %v", got, err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".state-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("failed save leaked temporary snapshots: %v %v", temps, err)
	}
}
