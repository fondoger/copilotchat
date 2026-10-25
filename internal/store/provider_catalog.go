package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

const (
	providerCatalogVersion   = 1
	maxProviderCatalogBytes  = 8 << 20
	maxProviderCatalogModels = 4096
)

// ProviderCatalog is a disposable model snapshot, separate from application
// state. Binding is an opaque account fingerprint, never an upstream credential.
type ProviderCatalog struct {
	Version    int              `json:"version"`
	ProviderID string           `json:"providerId"`
	Kind       string           `json:"kind"`
	Host       string           `json:"host"`
	Binding    string           `json:"binding"`
	AccountID  string           `json:"accountId"`
	Login      string           `json:"login"`
	FetchedAt  time.Time        `json:"fetchedAt"`
	Models     []provider.Model `json:"models"`
}

func (s *Store) providerCatalogPath(providerID string) string {
	return filepath.Join(s.dir, "provider-catalogs", fmt.Sprintf("%x.json", sha256.Sum256([]byte(providerID))))
}

// LoadProviderCatalog reports missing or invalid caches without changing state.
// Callers should treat these errors as cache misses and fetch a fresh catalog.
func (s *Store) LoadProviderCatalog(providerID string) (ProviderCatalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var catalog ProviderCatalog
	f, err := openSnapshot(s.providerCatalogPath(providerID))
	if err != nil {
		return catalog, fmt.Errorf("open provider catalog: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxProviderCatalogBytes+1))
	if err != nil {
		return catalog, fmt.Errorf("read provider catalog: %w", err)
	}
	if len(data) > maxProviderCatalogBytes {
		return catalog, errors.New("provider catalog exceeds size limit")
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return ProviderCatalog{}, fmt.Errorf("decode provider catalog: %w", err)
	}
	if catalog.Version != providerCatalogVersion {
		return ProviderCatalog{}, fmt.Errorf("unsupported provider catalog version %d", catalog.Version)
	}
	if catalog.ProviderID != providerID {
		return ProviderCatalog{}, errors.New("provider catalog ID does not match requested provider")
	}
	if err := validateProviderCatalog(catalog); err != nil {
		return ProviderCatalog{}, err
	}
	return catalog, nil
}

// SaveProviderCatalog atomically replaces only this provider's cache. An empty
// model list records a successful fetch, rather than an absent catalog.
func (s *Store) SaveProviderCatalog(catalog ProviderCatalog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	catalog.Version = providerCatalogVersion
	if catalog.Models == nil {
		catalog.Models = []provider.Model{}
	}
	if err := validateProviderCatalog(catalog); err != nil {
		return err
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("encode provider catalog: %w", err)
	}
	// writeSnapshot appends one newline; include it in the on-disk bound.
	if len(data)+1 > maxProviderCatalogBytes {
		return errors.New("provider catalog exceeds size limit")
	}
	path := s.providerCatalogPath(catalog.ProviderID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create provider catalog directory: %w", err)
	}
	return writeSnapshot(filepath.Dir(path), path, data)
}

func (s *Store) DeleteProviderCatalog(providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.providerCatalogPath(providerID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete provider catalog: %w", err)
	}
	return nil
}

func validateProviderCatalog(catalog ProviderCatalog) error {
	if strings.TrimSpace(catalog.ProviderID) == "" || strings.TrimSpace(catalog.Kind) == "" ||
		strings.TrimSpace(catalog.Host) == "" || strings.TrimSpace(catalog.Binding) == "" || catalog.FetchedAt.IsZero() {
		return errors.New("provider catalog requires provider ID, kind, host, binding, and fetch time")
	}
	if catalog.Models == nil {
		return errors.New("provider catalog requires a model list")
	}
	if len(catalog.Models) > maxProviderCatalogModels {
		return errors.New("provider catalog exceeds model limit")
	}
	for _, model := range catalog.Models {
		if strings.TrimSpace(model.ID) == "" {
			return errors.New("provider catalog contains an empty model ID")
		}
	}
	return nil
}
