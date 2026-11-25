package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyProviderKindRemainsGitHub(t *testing.T) {
	dir := t.TempDir()
	disk, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"schemaVersion":2,"settings":{"providers":[{"id":"existing","name":"Company","host":"company.ghe.com"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := disk.Load()
	if err != nil {
		t.Fatal(err)
	}
	provider := state.Settings.Providers[0]
	if provider.Kind != ProviderKindGitHub || provider.Host != "company.ghe.com" || provider.ID != "existing" {
		t.Fatalf("existing provider identity changed: %+v", provider)
	}
}

func TestProviderKindsKeepAuthenticationHostsSeparate(t *testing.T) {
	for _, test := range []struct {
		name, kind, host string
		valid            bool
	}{
		{"github-public", ProviderKindGitHub, "github.com", true},
		{"github-enterprise", ProviderKindGitHub, "EXAMPLE.GHE.COM", true},
		{"github-rejects-custom-host", ProviderKindGitHub, "models.example.test", false},
		{"legacy-rejects-custom-host", "", "models.example.test", false},
		{"plugin", "plugin:example", "", true},
		{"unavailable-kind", "retired-provider", "github.com", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			disk, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			state := DefaultState()
			state.Settings.Providers = []Provider{{ID: "provider", Kind: test.kind, Host: test.host}}
			err = disk.Save(state)
			if !test.valid {
				if err == nil {
					t.Fatal("accepted a host for the wrong authentication provider")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := disk.Load()
			if err != nil {
				t.Fatal(err)
			}
			p := loaded.Settings.Providers[0]
			if p.Kind != test.kind {
				t.Fatalf("provider kind did not round trip: %q", p.Kind)
			}

		})
	}
}
