package store

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestDefaultsAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, DefaultState()) {
		t.Fatalf("unexpected initial state: %#v", state)
	}
	state.Settings.SendWithEnter = false
	state.Settings.ChatTitle = false
	state.Settings.Theme = "dark"
	state.ActiveID = "conversation-1"
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	state.Conversations = []Conversation{{ID: "conversation-1", Title: "你好", CreatedAt: now, UpdatedAt: now, Favorite: true, Messages: []Message{{ID: "message-1", Role: "assistant", Content: "回答", Attachments: []Attachment{{Name: "test.txt", MIME: "text/plain", Data: "aGVsbG8=", Size: 5}}, ProviderMessages: []json.RawMessage{json.RawMessage(`{"role":"tool","content":"done"}`)}, ToolActivity: []string{"Ran example"}}}}}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	// JSON indentation is not significant in opaque provider messages.
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(state)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("round trip mismatch\ngot: %s\nwant: %s", gotJSON, wantJSON)
	}
	state.Conversations[0].Title = "Replaced"
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil || got.Conversations[0].Title != "Replaced" {
		t.Fatalf("replace: %v, %#v", err, got)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".state-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary snapshots left behind: %v, %v", temps, err)
	}
}

func TestMissingSettingsUseDefaultsAndExplicitFalseSurvives(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"settings":{"theme":"unknown","fontSize":0,"sendWithEnter":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.Theme != "system" || state.Settings.FontSize != 16 || state.Settings.SidebarWidth != 240 || state.Settings.SendWithEnter || !state.Settings.ChatTitle {
		t.Fatalf("invalid migration: %#v", state.Settings)
	}
}

func TestCorruptStateIsNotOverwritten(t *testing.T) {
	for _, contents := range []string{`{"settings":`, `{} {}`} {
		t.Run(contents, func(t *testing.T) {
			dir := t.TempDir()
			s, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "state.json")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(); err == nil {
				t.Fatal("expected malformed state error")
			}
			unchanged, err := os.ReadFile(path)
			if err != nil || string(unchanged) != contents {
				t.Fatalf("corrupt state was modified: %s, %v", unchanged, err)
			}
		})
	}
}

func TestAtomicSnapshotsForExternalReaders(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := DefaultState()
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Use an independent reader, outside Store's mutex. Windows may
			// temporarily deny new opens while it replaces a file, so the same
			// bounded sharing-violation retry used by Load is appropriate.
			f, err := openSnapshot(filepath.Join(dir, "state.json"))
			var b []byte
			if err == nil {
				b, err = io.ReadAll(f)
				_ = f.Close()
			}
			if err == nil {
				var snapshot State
				err = json.Unmarshal(b, &snapshot)
			}
			if err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
			// Leave a scheduling window for Windows directory-entry replacement;
			// a continuous reopen loop can starve an otherwise valid writer.
			time.Sleep(time.Millisecond)
		}
	}()
	for i := 0; i < 20; i++ {
		state.Settings.FontSize = 10 + i
		if err := s.Save(state); err != nil {
			close(stop)
			wg.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatalf("external reader saw partial/missing snapshot: %v", err)
	default:
	}
}
