package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

func catalogFixture() ProviderCatalog {
	return ProviderCatalog{
		Version: 1, ProviderID: "account-1", Kind: ProviderKindGitHub, Host: "github.com",
		Binding: "opaque-fingerprint", AccountID: "123", Login: "example",
		FetchedAt: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
		Models: []provider.Model{{
			ID: "model-1", Name: "Example", Vision: true, PDF: true,
			Endpoint: "/messages", Endpoints: []string{"/messages", "/chat/completions"},
			MaxOutputTokens: 8192, ContextWindow: 200000, CreatedAt: "2026-01-01",
			Lifecycle: "preview", DeprecatedAt: "2027-01-01", RetiresAt: "2027-02-01", Line: "example",
			Capabilities:      json.RawMessage(`{"nested":{"supported":true},"unknown":[1,"future"]}`),
			StructuredOutputs: true, Tools: true, Reasoning: true, VisionKnown: true, PDFKnown: true,
			ToolsKnown: true, NativeSearch: true, NativeSearchKnown: true, ReasoningKnown: true,
			ThinkingEfforts: []string{"low", "high"}, AdaptiveThinking: true, MinThinkingBudget: 1024, MaxThinkingBudget: 8192,
		}},
	}
}

func newCatalogStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProviderCatalogRoundTripAndReplacement(t *testing.T) {
	s := newCatalogStore(t)
	want := catalogFixture()
	want.Version = 99 // Save owns the format version.
	if err := s.SaveProviderCatalog(want); err != nil {
		t.Fatal(err)
	}
	want.Version = 1
	got, err := s.LoadProviderCatalog(want.ProviderID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata round trip: got %#v, error %v", got, err)
	}
	want.Models[0].Name = "Refreshed"
	want.FetchedAt = want.FetchedAt.Add(time.Hour)
	if err := s.SaveProviderCatalog(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadProviderCatalog(want.ProviderID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement: got %#v, error %v", got, err)
	}
	temps, err := filepath.Glob(filepath.Join(s.dir, "provider-catalogs", ".snapshot-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary snapshots remain: %v, %v", temps, err)
	}
}

func TestProviderCatalogSafeIDsEmptyModelsAndDeletion(t *testing.T) {
	s := newCatalogStore(t)
	for _, id := range []string{"../../escape", `C:\absolute\name`, "account-1"} {
		catalog := catalogFixture()
		catalog.ProviderID, catalog.Models = id, nil
		if err := s.SaveProviderCatalog(catalog); err != nil {
			t.Fatal(err)
		}
		path := s.providerCatalogPath(id)
		if filepath.Dir(path) != filepath.Join(s.dir, "provider-catalogs") || len(filepath.Base(path)) != 64+len(".json") {
			t.Fatalf("unsafe catalog path: %s", path)
		}
		got, err := s.LoadProviderCatalog(id)
		if err != nil || got.Models == nil || len(got.Models) != 0 {
			t.Fatalf("empty catalog must be a cache hit: %#v, %v", got, err)
		}
		if err := s.DeleteProviderCatalog(id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LoadProviderCatalog(id); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted catalog still exists: %v", err)
		}
		if err := s.DeleteProviderCatalog(id); err != nil {
			t.Fatalf("delete missing catalog: %v", err)
		}
	}
}

func TestProviderCatalogRejectsInvalidSnapshotsWithoutChangingState(t *testing.T) {
	s := newCatalogStore(t)
	state := DefaultState()
	state.Settings.Theme = "dark"
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadProviderCatalog("account-1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing catalog: %v", err)
	}
	valid, err := json.Marshal(catalogFixture())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(valid, &fields); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"bad JSON": []byte(`{"version":`), "trailing JSON": append(bytes.Clone(valid), []byte(` {}`)...),
		"oversize": bytes.Repeat([]byte(" "), maxProviderCatalogBytes+1),
	}
	for _, field := range []string{"version", "providerId", "kind", "host", "binding", "fetchedAt", "models"} {
		original := fields[field]
		delete(fields, field)
		cases["missing "+field], err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		fields[field] = original
	}
	for name, replacement := range map[string]any{
		"version": 2, "providerId": "another-account", "kind": " ", "host": "", "binding": "",
		"fetchedAt": "0001-01-01T00:00:00Z", "models": nil,
	} {
		original := fields[name]
		fields[name] = replacement
		cases["invalid "+name], err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		fields[name] = original
	}
	badModels := catalogFixture()
	badModels.Models[0].ID = ""
	cases["empty model ID"], _ = json.Marshal(badModels)
	badModels.Models = make([]provider.Model, maxProviderCatalogModels+1)
	for i := range badModels.Models {
		badModels.Models[i].ID = "valid"
	}
	cases["too many models"], _ = json.Marshal(badModels)
	path := s.providerCatalogPath("account-1")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoadProviderCatalog("account-1"); err == nil {
				t.Fatal("invalid catalog accepted")
			}
			got, err := s.Load()
			if err != nil || !reflect.DeepEqual(got, state) {
				t.Fatalf("cache broke state loading: %#v, %v", got, err)
			}
		})
	}
	stateAfter, err := os.ReadFile(s.path)
	if err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("cache reads changed state: %v", err)
	}
}

func TestProviderCatalogFailedSavePreservesSnapshot(t *testing.T) {
	s := newCatalogStore(t)
	want := catalogFixture()
	if err := s.SaveProviderCatalog(want); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*ProviderCatalog){
		"provider ID": func(c *ProviderCatalog) { c.ProviderID = "" },
		"kind":        func(c *ProviderCatalog) { c.Kind = "" }, "host": func(c *ProviderCatalog) { c.Host = "" },
		"binding": func(c *ProviderCatalog) { c.Binding = "" }, "fetch time": func(c *ProviderCatalog) { c.FetchedAt = time.Time{} },
		"model ID":         func(c *ProviderCatalog) { c.Models[0].ID = " " },
		"model count":      func(c *ProviderCatalog) { c.Models = make([]provider.Model, maxProviderCatalogModels+1) },
		"size":             func(c *ProviderCatalog) { c.Models[0].Name = strings.Repeat("x", maxProviderCatalogBytes) },
		"invalid metadata": func(c *ProviderCatalog) { c.Models[0].Capabilities = json.RawMessage(`{`) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			invalid := catalogFixture()
			mutate(&invalid)
			if err := s.SaveProviderCatalog(invalid); err == nil {
				t.Fatal("invalid catalog saved")
			}
			got, err := s.LoadProviderCatalog(want.ProviderID)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("failed save changed cached data: %#v, %v", got, err)
			}
		})
	}
}

func TestProviderCatalogAtomicForExternalReaders(t *testing.T) {
	s := newCatalogStore(t)
	catalog := catalogFixture()
	catalog.Login = catalog.Models[0].Name
	path := s.providerCatalogPath(catalog.ProviderID)
	if err := s.SaveProviderCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop, errs := make(chan struct{}), make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			f, err := openSnapshot(path)
			var data []byte
			if err == nil {
				data, err = io.ReadAll(f)
				_ = f.Close()
			}
			var got ProviderCatalog
			if err == nil {
				err = json.Unmarshal(data, &got)
			}
			if err == nil && (len(got.Models) != 1 || got.Models[0].Name != got.Login) {
				err = fmt.Errorf("inconsistent catalog snapshot: %#v", got)
			}
			if err != nil {
				errs <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	for i := 0; i < 20; i++ {
		catalog.Login = fmt.Sprintf("revision-%d", i)
		catalog.Models[0].Name = catalog.Login
		if err := s.SaveProviderCatalog(catalog); err != nil {
			close(stop)
			wg.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatalf("external reader saw partial or missing catalog: %v", err)
	default:
	}
}
