package store

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func DataDir(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Copilot Chat"), nil
}

// ReadProvider is read-only and skips conversation bodies while decoding. A
// helper must never create default providers or save/migrate application state.
func ReadProvider(dir, id string) (Provider, error) {
	f, err := openSnapshot(filepath.Join(dir, "state.json"))
	if err != nil {
		return Provider{}, errors.New("open Copilot Chat and configure the provider first")
	}
	defer f.Close()
	var snapshot struct {
		Settings struct {
			Providers []Provider `json:"providers"`
		} `json:"settings"`
	}
	decoder := json.NewDecoder(f)
	if decoder.Decode(&snapshot) != nil {
		return Provider{}, errors.New("could not read Copilot Chat provider settings")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Provider{}, errors.New("invalid Copilot Chat provider settings")
	}
	var found *Provider
	for _, p := range snapshot.Settings.Providers {
		if p.ID == id {
			if found != nil {
				return Provider{}, errors.New("duplicate provider ID in settings")
			}
			copy := p
			found = &copy
		}
	}
	if found == nil {
		return Provider{}, errors.New("provider no longer exists; regenerate the agent configuration")
	}
	if found.Disabled {
		return Provider{}, errors.New("provider is disabled; enable it in Copilot Chat")
	}
	if found.Login == "" {
		return Provider{}, errors.New("provider is signed out; reconnect it in Copilot Chat")
	}
	return *found, nil
}
