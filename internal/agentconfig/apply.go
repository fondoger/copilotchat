package agentconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const modifiedBy = "copilot-chat"

type ApplyResult struct {
	Path       string
	BackupPath string
}

// Apply installs the generated configuration only when the user clicks Apply.
// Generating previews and copying configuration never changes client files.
func Apply(agent, config string) (ApplyResult, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ApplyResult{}, err
	}
	switch agent {
	case ClaudeCode:
		return applyClaudeCode(filepath.Join(home, ".claude", "settings.json"), []byte(config))
	case ClaudeDesktop:
		var root string
		if runtime.GOOS == "windows" {
			root = os.Getenv("LOCALAPPDATA")
			if root == "" {
				root = filepath.Join(home, "AppData", "Local")
			}
		} else {
			root, err = os.UserConfigDir()
			if err != nil {
				return ApplyResult{}, err
			}
		}
		return applyClaudeDesktop(filepath.Join(root, "Claude-3p", "configLibrary"), []byte(config))
	case Codex:
		return applyCodex(filepath.Join(home, ".codex", "config.toml"), []byte(config))
	default:
		return ApplyResult{}, errors.New("select a supported agent")
	}
}

type jsonObject map[string]json.RawMessage

func decodeJSON(data []byte) (jsonObject, error) {
	var doc jsonObject
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errors.New("configuration must be a JSON object")
	}
	return doc, nil
}

func readOptional(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func readJSON(path string) (jsonObject, []byte, bool, error) {
	data, exists, err := readOptional(path)
	if err != nil {
		return nil, nil, false, err
	}
	if !exists {
		return jsonObject{}, nil, false, nil
	}
	doc, err := decodeJSON(data)
	if err != nil {
		return nil, nil, true, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc, data, true, nil
}

func ownedConfiguration(doc jsonObject) bool {
	var owner string
	return json.Unmarshal(doc["lastModifiedBy"], &owner) == nil && owner == modifiedBy
}

func writeJSON(path string, doc jsonObject) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

func jsonValue(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// A failed write leaves the original intact. Keep both the temporary file and
// final configuration private: these files may also contain unrelated secrets.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".copilot-chat-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func backupConfiguration(path string, data []byte) (string, error) {
	ext := filepath.Ext(path)
	stamp := time.Now().Format("20060102-150405.000000000")
	backup := path[:len(path)-len(ext)] + ".copilot-chat-backup-" + stamp + ext
	f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(backup)
		return "", err
	}
	return backup, nil
}
