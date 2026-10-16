package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateSettingsDefaultsForFirstLaunchAndLegacyState(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
	}{
		{"first launch", ""},
		{"unversioned", `{"settings":{"theme":"dark","chatTitle":false},"conversations":[{"id":"saved","title":"Saved conversation"}]}`},
		{"existing schema", `{"schemaVersion":2,"settings":{"theme":"dark","chatTitle":false},"conversations":[{"id":"saved","title":"Saved conversation"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.contents != "" {
				if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(tc.contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			disk, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			state, err := disk.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !state.Settings.AutomaticUpdates || state.Settings.UpdateIntervalHours != 24 || !state.Settings.LastUpdateCheck.IsZero() {
				t.Fatalf("unexpected update defaults: %+v", state.Settings)
			}
			if state.SchemaVersion != 2 {
				t.Fatalf("update preferences changed the schema: %d", state.SchemaVersion)
			}
			if tc.contents != "" && (state.Settings.Theme != "dark" || state.Settings.ChatTitle || len(state.Conversations) != 1 || state.Conversations[0].ID != "saved") {
				t.Fatalf("adding update defaults changed existing state: %+v", state)
			}
		})
	}
}

func TestUpdateSettingsPersistAcrossRestart(t *testing.T) {
	checkedAt := time.Date(2026, 10, 16, 8, 30, 0, 123456789, time.UTC)
	for _, interval := range []int{1, 6, 12, 24, 168} {
		t.Run(fmt.Sprintf("%dh", interval), func(t *testing.T) {
			dir := t.TempDir()
			disk, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			state := DefaultState()
			state.Settings.AutomaticUpdates = false
			state.Settings.UpdateIntervalHours = interval
			state.Settings.LastUpdateCheck = checkedAt
			if err := disk.Save(state); err != nil {
				t.Fatal(err)
			}
			restarted, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			got, err := restarted.Load()
			if err != nil {
				t.Fatal(err)
			}
			if got.Settings.AutomaticUpdates || got.Settings.UpdateIntervalHours != interval || !got.Settings.LastUpdateCheck.Equal(checkedAt) {
				t.Fatalf("update preferences changed after restart: %+v", got.Settings)
			}
		})
	}
}

func TestInvalidUpdateIntervalsNormalizeWithoutEnablingUpdates(t *testing.T) {
	for _, interval := range []int{-24, 0, 2, 23, 25, 169} {
		t.Run(fmt.Sprint(interval), func(t *testing.T) {
			dir := t.TempDir()
			contents := fmt.Sprintf(`{"schemaVersion":2,"settings":{"automaticUpdates":false,"updateIntervalHours":%d}}`, interval)
			if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			disk, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			state, err := disk.Load()
			if err != nil {
				t.Fatal(err)
			}
			if state.Settings.AutomaticUpdates || state.Settings.UpdateIntervalHours != 24 || !state.Settings.LastUpdateCheck.IsZero() {
				t.Fatalf("invalid interval changed enablement or check time: %+v", state.Settings)
			}
			// Save also normalizes values supplied by callers before persisting.
			state.Settings.UpdateIntervalHours = interval
			if err := disk.Save(state); err != nil {
				t.Fatal(err)
			}
			persisted := DefaultState()
			data, err := os.ReadFile(filepath.Join(dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &persisted); err != nil {
				t.Fatal(err)
			}
			if persisted.Settings.AutomaticUpdates || persisted.Settings.UpdateIntervalHours != 24 {
				t.Fatalf("invalid update settings were persisted: %+v", persisted.Settings)
			}
		})
	}
}
