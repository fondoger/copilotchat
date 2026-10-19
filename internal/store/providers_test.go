package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyConversationsBindToDefaultProvider(t *testing.T) {
	dir := t.TempDir()
	disk, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"schemaVersion":1,"settings":{"fontSize":15,"model":"shared-id","titleModel":"title-id"},"conversations":[{"id":"chat","title":"Saved chat","model":"shared-id"}]}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := disk.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.FontSize != 15 || state.SchemaVersion != 2 || state.Settings.ActiveProviderID != DefaultProviderID || state.Conversations[0].ProviderID != DefaultProviderID || state.Settings.TitleProviderID != DefaultProviderID {
		t.Fatalf("unsafe migration: %#v", state)
	}
	if len(state.Settings.Providers) != 1 || state.Settings.Providers[0].Host != "github.com" {
		t.Fatal("missing default provider")
	}
}

func TestProviderPersistenceValidationAndStats(t *testing.T) {
	disk, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := DefaultState()
	refreshedAt := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	state.Settings.Providers = append(state.Settings.Providers, Provider{ID: "work", Name: "Work", Host: "EXAMPLE.GHE.COM", Login: "test-user", Disabled: true, ModelsRefreshedAt: refreshedAt, DisabledModels: []string{"same"}, PinnedModels: []string{"same"}})
	state.Settings.ActiveProviderID = "work"
	state.Conversations = []Conversation{{ID: "chat", ProviderID: "work", Model: "same", Messages: []Message{{ID: "reply", ProviderID: "work", Model: "same", Reasoning: "A thought", Stats: &MessageStats{InputTokens: 12, OutputTokens: 5, UsageAvailable: true, InputAvailable: true, OutputAvailable: true, DurationMS: 300}}}}}
	if err := disk.Save(state); err != nil {
		t.Fatal(err)
	}
	got, err := disk.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.Providers[1].Host != "example.ghe.com" || got.Settings.Providers[1].PinnedModels[0] != "same" || len(got.Settings.Providers[1].DisabledModels) != 1 || got.Settings.Providers[1].DisabledModels[0] != "same" || got.Conversations[0].Messages[0].Stats.OutputTokens != 5 {
		t.Fatalf("provider data lost: %#v", got)
	}
	if !got.Settings.Providers[1].Disabled || !got.Settings.Providers[1].ModelsRefreshedAt.Equal(refreshedAt) {
		t.Fatal("provider enablement or catalog refresh time was not persisted")
	}
	state.Settings.Providers[1].Host = "example.com"
	if err := disk.Save(state); err == nil {
		t.Fatal("invalid GitHub host accepted")
	}
	state.Settings.Providers[1].Host = "example.ghe.com"
	state.Settings.Providers[1].ID = DefaultProviderID
	if err := disk.Save(state); err == nil {
		t.Fatal("duplicate provider ID accepted")
	}
}

func TestExplicitEmptyProvidersSurviveWhileMissingProvidersMigrate(t *testing.T) {
	for _, test := range []struct {
		name, contents string
		wantProviders  int
	}{
		{"missing-legacy", `{"schemaVersion":1,"settings":{},"conversations":[]}`, 1},
		{"explicit-empty", `{"schemaVersion":2,"settings":{"providers":[]},"conversations":[{"id":"old","providerId":"deleted","model":"same"}]}`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			disk, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(test.contents), 0600); err != nil {
				t.Fatal(err)
			}
			state, err := disk.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Settings.Providers) != test.wantProviders {
				t.Fatal("missing and explicitly empty provider lists were conflated")
			}
			if err := disk.Save(state); err != nil {
				t.Fatal(err)
			}
			loaded, err := disk.Load()
			if err != nil || len(loaded.Settings.Providers) != test.wantProviders {
				t.Fatal("provider list changed after saving", err)
			}
			if test.wantProviders == 0 && (loaded.Settings.ActiveProviderID != "" || loaded.Conversations[0].ProviderID != "deleted") {
				t.Fatal("empty provider list changed previous conversation identity")
			}
		})
	}
}
