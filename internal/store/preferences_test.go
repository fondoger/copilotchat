package store

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyFontMigrationAndExplicitPreferences(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		font    int
		want    int
	}{
		{"legacy default", "", 15, 16},
		{"legacy custom smaller", "", 14, 14},
		{"legacy custom larger", "", 19, 19},
		{"current explicit fifteen", `"schemaVersion":1,`, 15, 15},
		{"invalid font", `"schemaVersion":1,`, 100, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			disk, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			contents := fmt.Sprintf(`{%s"settings":{"fontSize":%d,"theme":"dark","sendWithEnter":false}}`, tc.version, tc.font)
			if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			state, err := disk.Load()
			if err != nil {
				t.Fatal(err)
			}
			if state.SchemaVersion != CurrentSchemaVersion || state.Settings.FontSize != tc.want || state.Settings.Theme != "dark" || state.Settings.SendWithEnter {
				t.Fatalf("unexpected migration: %#v", state)
			}
			// Once saved with the new schema, choosing the former default is
			// an explicit preference and must not be migrated on the next load.
			state.Settings.FontSize = 15
			if err := disk.Save(state); err != nil {
				t.Fatal(err)
			}
			state, err = disk.Load()
			if err != nil || state.Settings.FontSize != 15 {
				t.Fatalf("migration repeated: %#v, %v", state, err)
			}
		})
	}
}

func TestSidebarWidthValidationAndPersistence(t *testing.T) {
	disk, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ width, want float32 }{
		{0, 240}, {180, 240}, {240, 240}, {327.5, 327.5}, {500, 500}, {900, 500},
		{float32(math.NaN()), 240}, {float32(math.Inf(1)), 240},
	} {
		state := DefaultState()
		state.Settings.SidebarWidth = tc.width
		if err := disk.Save(state); err != nil {
			t.Fatal(err)
		}
		got, err := disk.Load()
		if err != nil || got.Settings.SidebarWidth != tc.want {
			t.Fatalf("width %v: got %v, want %v, err %v", tc.width, got.Settings.SidebarWidth, tc.want, err)
		}
	}
}
