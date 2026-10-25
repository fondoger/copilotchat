package agentconfig

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/fondoger/copilotchat/internal/provider"
)

func applyClaudeCode(path string, data []byte) (ApplyResult, error) {
	result := ApplyResult{Path: path}
	generated, err := decodeJSON(data)
	if err != nil {
		return result, err
	}
	doc, original, exists, err := readJSON(path)
	if err != nil {
		return result, err
	}
	owned := ownedConfiguration(doc)
	permissions := jsonObject{}
	if raw, ok := doc["permissions"]; ok {
		permissions, err = decodeJSON(raw)
		if err != nil {
			return result, fmt.Errorf("parse Claude Code permissions: %w", err)
		}
	}
	var defaultMode json.RawMessage
	if raw, ok := generated["permissions"]; ok {
		generatedPermissions, err := decodeJSON(raw)
		if err != nil {
			return result, fmt.Errorf("parse generated Claude Code permissions: %w", err)
		}
		defaultMode = generatedPermissions["defaultMode"]
	}
	if defaultMode != nil {
		permissions["defaultMode"] = defaultMode
		doc["permissions"] = jsonValue(permissions)
	} else if _, ok := permissions["defaultMode"]; ok {
		delete(permissions, "defaultMode")
		if len(permissions) == 0 {
			delete(doc, "permissions")
		} else {
			doc["permissions"] = jsonValue(permissions)
		}
	}
	// These are complete replacements, including env. Retaining an old model
	// override or authentication variable can silently bypass the gateway.
	for _, key := range []string{"env", "modelPicker", "modelSettings"} {
		if _, ok := generated[key]; !ok {
			return result, fmt.Errorf("generated configuration is missing %s", key)
		}
		doc[key] = generated[key]
	}
	delete(doc, "apiKeyHelper")
	var picker struct {
		Options []struct{ Model string } `json:"options"`
	}
	if err := json.Unmarshal(doc["modelPicker"], &picker); err != nil {
		return result, err
	}
	var selected string
	_ = json.Unmarshal(doc["model"], &selected)
	valid := false
	for _, option := range picker.Options {
		valid = valid || (selected != "" && selected == option.Model)
	}
	if !valid {
		delete(doc, "model")
	}
	doc["lastModifiedBy"] = jsonValue(modifiedBy)
	if exists && !owned {
		result.BackupPath, err = backupConfiguration(path, original)
		if err != nil {
			return result, fmt.Errorf("back up existing Claude Code settings: %w", err)
		}
	}
	return result, writeJSON(path, doc)
}

var desktopConfigID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const desktopConfigName = "Copilot Chat"

func applyClaudeDesktop(dir string, data []byte) (ApplyResult, error) {
	generated, err := decodeJSON(data)
	if err != nil {
		return ApplyResult{}, err
	}
	metaPath := filepath.Join(dir, "_meta.json")
	meta, _, _, err := readJSON(metaPath)
	if err != nil {
		return ApplyResult{}, err
	}
	var entries []jsonObject
	if raw, ok := meta["entries"]; ok {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return ApplyResult{}, fmt.Errorf("parse Claude Desktop configuration entries: %w", err)
		}
	}
	var appliedID string
	_ = json.Unmarshal(meta["appliedId"], &appliedID)
	// Desktop validates configuration fields strictly. Identify our file by
	// its library entry name instead of adding a marker to the configuration.
	var id string
	for _, entry := range entries {
		var candidate, name string
		_ = json.Unmarshal(entry["id"], &candidate)
		_ = json.Unmarshal(entry["name"], &name)
		if name == desktopConfigName && desktopConfigID.MatchString(candidate) && (id == "" || candidate == appliedID) {
			id = candidate
		}
	}
	if id == "" {
		id = provider.RequestID()
	}
	doc, _, _, err := readJSON(filepath.Join(dir, id+".json"))
	if err != nil {
		return ApplyResult{}, err
	}
	for key, value := range generated {
		doc[key] = value
	}
	delete(doc, "lastModifiedBy")
	registered := false
	for _, entry := range entries {
		var entryID string
		_ = json.Unmarshal(entry["id"], &entryID)
		registered = registered || entryID == id
	}
	if !registered {
		entries = append(entries, jsonObject{"id": jsonValue(id), "name": jsonValue(desktopConfigName)})
	}
	meta["appliedId"], meta["entries"] = jsonValue(id), jsonValue(entries)
	result := ApplyResult{Path: filepath.Join(dir, id+".json")}
	if err := writeJSON(result.Path, doc); err != nil {
		return result, err
	}
	// Publish the complete configuration before making Desktop select it.
	if err := writeJSON(metaPath, meta); err != nil {
		return result, fmt.Errorf("activate Claude Desktop configuration: %w", err)
	}
	return result, nil
}
