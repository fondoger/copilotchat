package agentconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/localproxy"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/pelletier/go-toml/v2"
)

func generatedApplyConfig(t *testing.T, agent string) []byte {
	t.Helper()
	model := provider.Model{ID: "github-copilot/gpt-fixture", Endpoint: "/responses"}
	config, err := Generate(Options{
		Agent: agent, BaseURL: "http://127.0.0.1:39321/test-profile", Key: "copilotchat",
		Model: model, CatalogPath: `C:\Users\test\catalog.json`,
		Models: []localproxy.Model{{ID: model.ID, Model: provider.Model{ID: "gpt-fixture", Endpoint: "/responses"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return []byte(config)
}

func fixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureJSON(t *testing.T, path string) jsonObject {
	t.Helper()
	doc, err := decodeJSON(readFixture(t, path))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestApplyClaudeCodeReplacesOwnedFieldsAndBacksUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	original := `{"env":{"CUSTOM_SECRET":"old","ANTHROPIC_API_KEY":"old"},"apiKeyHelper":"old-helper","model":"unavailable","permissions":{"allow":["Read"]},"largeNumber":9007199254740993}`
	fixtureFile(t, path, original)
	generated := generatedApplyConfig(t, ClaudeCode)
	result, err := applyClaudeCode(path, generated)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupPath == "" || string(readFixture(t, result.BackupPath)) != original || !strings.HasPrefix(filepath.Base(result.BackupPath), "settings.copilot-chat-backup-") {
		t.Fatal("original configuration was not backed up exactly", result)
	}
	doc, want := fixtureJSON(t, path), jsonObject{}
	_ = json.Unmarshal(generated, &want)
	for _, key := range []string{"env", "modelPicker", "modelSettings"} {
		var got, expected any
		_ = json.Unmarshal(doc[key], &got)
		_ = json.Unmarshal(want[key], &expected)
		if !reflect.DeepEqual(got, expected) {
			t.Fatal("managed field was merged instead of replaced", key)
		}
	}
	if doc["apiKeyHelper"] != nil || doc["model"] != nil || !ownedConfiguration(doc) || doc["permissions"] == nil || string(doc["largeNumber"]) != "9007199254740993" {
		t.Fatal("unrelated settings lost or stale overrides retained", doc)
	}
	second, err := applyClaudeCode(path, generated)
	if err != nil || second.BackupPath != "" {
		t.Fatal("managed settings should not create another backup", second, err)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "settings.copilot-chat-backup-*.json"))
	if len(backups) != 1 {
		t.Fatal("unexpected backups", backups)
	}
}

func TestApplyClaudeCodePreservesOnlyMatchingDefault(t *testing.T) {
	for _, model := range []string{"github-copilot/gpt-fixture[1m]", "github-copilot/gpt-fixture", "sonnet"} {
		t.Run(model, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			fixtureFile(t, path, `{"model":`+string(jsonValue(model))+`,"lastModifiedBy":"copilot-chat"}`)
			if _, err := applyClaudeCode(path, generatedApplyConfig(t, ClaudeCode)); err != nil {
				t.Fatal(err)
			}
			doc := fixtureJSON(t, path)
			if (doc["model"] != nil) != (model == "github-copilot/gpt-fixture[1m]") {
				t.Fatal("default model must exactly match a picker option", doc)
			}
		})
	}
}

func TestApplyClaudeCodeCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	result, err := applyClaudeCode(path, generatedApplyConfig(t, ClaudeCode))
	if err != nil || result.BackupPath != "" || !ownedConfiguration(fixtureJSON(t, path)) {
		t.Fatal(result, err)
	}
}

func TestApplyClaudeDesktopPreservesExternalFilesAndReusesOwned(t *testing.T) {
	dir := t.TempDir()
	externalID := "00000000-0000-4000-8000-000000000001"
	external := filepath.Join(dir, externalID+".json")
	fixtureFile(t, external, `{"inferenceGatewayBaseUrl":"https://other.example","custom":true}`)
	metaPath := filepath.Join(dir, "_meta.json")
	fixtureFile(t, metaPath, `{"appliedId":"`+externalID+`","entries":[{"id":"`+externalID+`","name":"Work","color":"blue"}],"customMeta":42}`)
	before := readFixture(t, external)
	generated := generatedApplyConfig(t, ClaudeDesktop)
	first, err := applyClaudeDesktop(dir, generated)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == external || fixtureJSON(t, first.Path)["lastModifiedBy"] != nil || !bytes.Equal(readFixture(t, external), before) {
		t.Fatal("external configuration was modified", first)
	}
	meta := fixtureJSON(t, metaPath)
	if string(meta["appliedId"]) != string(jsonValue(strings.TrimSuffix(filepath.Base(first.Path), ".json"))) || string(meta["customMeta"]) != "42" {
		t.Fatal("configuration not activated or metadata lost", meta)
	}
	var entries []jsonObject
	_ = json.Unmarshal(meta["entries"], &entries)
	if len(entries) != 2 || string(entries[0]["color"]) != `"blue"` || string(entries[1]["name"]) != `"Copilot Chat"` {
		t.Fatal("external library entry lost", entries)
	}
	// Reapplying also removes the unsupported field written by older builds.
	legacy := fixtureJSON(t, first.Path)
	legacy["lastModifiedBy"] = jsonValue(modifiedBy)
	if err := writeJSON(first.Path, legacy); err != nil {
		t.Fatal(err)
	}
	second, err := applyClaudeDesktop(dir, bytes.ReplaceAll(generated, []byte("test-profile"), []byte("another-profile")))
	if err != nil || second.Path != first.Path || !bytes.Contains(readFixture(t, second.Path), []byte("another-profile")) || fixtureJSON(t, second.Path)["lastModifiedBy"] != nil {
		t.Fatal("repeated Apply must update the managed file", second, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 3 {
		t.Fatal("repeated Apply created extra configuration files", files)
	}
	// A different active entry must not cause another managed file to be made.
	meta["appliedId"] = jsonValue(externalID)
	if err := writeJSON(metaPath, meta); err != nil {
		t.Fatal(err)
	}
	third, err := applyClaudeDesktop(dir, generated)
	if err != nil || third.Path != first.Path {
		t.Fatal("inactive owned file was not reused", third, err)
	}
}

func TestApplyClaudeDesktopCreatesLibrary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "configLibrary")
	result, err := applyClaudeDesktop(dir, generatedApplyConfig(t, ClaudeDesktop))
	if err != nil || !desktopConfigID.MatchString(strings.TrimSuffix(filepath.Base(result.Path), ".json")) {
		t.Fatal(result, err)
	}
	expected, _ := decodeJSON(generatedApplyConfig(t, ClaudeDesktop))
	if !reflect.DeepEqual(fixtureJSON(t, result.Path), expected) || fixtureJSON(t, filepath.Join(dir, "_meta.json"))["appliedId"] == nil {
		t.Fatal("new library not initialized")
	}
}

func TestApplyCodexPreservesSettingsCommentsAndOtherProviders(t *testing.T) {
	fixtures := []string{
		`# My settings
model = "old-model"
model_provider = "other"
model_catalog_json = "old-catalog"
model_reasoning_effort = "high" # keep this comment
[features]
shell_tool = true
[model_providers]
[model_providers.other]
name = "Other"
base_url = "https://other.example"
[model_providers."copilot_chat"] # replace this provider
name = "Old Copilot"
base_url = "http://old.example"
[model_providers.copilot_chat.http_headers]
Legacy = "remove"
[profiles.work]
model_provider = "other"
model = "work-model"
[mcp_servers.example]
command = "test"
args = ["a", "b"]
`,
		`# Inline providers
model_providers = { other = {name = "Other", base_url = "https://other.example"}, copilot_chat = { name = "Old" } }
model = "old"
[features]
shell_tool = true
`,
		`model_providers.other.name = "Other"
model_providers.copilot_chat.name = "Old"
model = "old"
`,
		`[model_providers]
other = { name = "Other" }
copilot_chat = { name = "Old" }
`,
		"",
	}
	for i, original := range fixtures {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".codex", "config.toml")
			if original != "" {
				fixtureFile(t, path, original)
			}
			generated := generatedApplyConfig(t, Codex)
			if _, err := applyCodex(path, generated); err != nil {
				t.Fatal(err)
			}
			data := readFixture(t, path)
			var got, want map[string]any
			_ = toml.Unmarshal(data, &got)
			_ = toml.Unmarshal(generated, &want)
			for _, key := range []string{"model_provider", "model", "model_catalog_json"} {
				if !reflect.DeepEqual(got[key], want[key]) {
					t.Fatal("managed root setting not applied", key, got)
				}
			}
			gotProviders := got["model_providers"].(map[string]any)
			if !reflect.DeepEqual(gotProviders["copilot_chat"], want["model_providers"].(map[string]any)["copilot_chat"]) {
				t.Fatal("provider was not replaced", gotProviders)
			}
			if original != "" && gotProviders["other"].(map[string]any)["name"] != "Other" {
				t.Fatal("other provider changed", gotProviders)
			}
			if i == 0 && (!bytes.Contains(data, []byte(`# keep this comment`)) || got["profiles"].(map[string]any)["work"].(map[string]any)["model"] != "work-model" || got["mcp_servers"] == nil) {
				t.Fatal("unrelated settings or comments lost", string(data))
			}
			if _, err := applyCodex(path, generated); err != nil {
				t.Fatal("repeated Apply failed", err)
			}
			if !bytes.Equal(data, readFixture(t, path)) {
				t.Fatalf("repeated Apply changed the same configuration:\nfirst:\n%s\nsecond:\n%s", data, readFixture(t, path))
			}
		})
	}
}

func TestApplyRejectsMalformedExistingFiles(t *testing.T) {
	for _, agent := range []string{ClaudeCode, ClaudeDesktop, Codex} {
		t.Run(agent, func(t *testing.T) {
			dir := t.TempDir()
			path, original := filepath.Join(dir, "settings.json"), "{invalid"
			if agent == ClaudeDesktop {
				path = filepath.Join(dir, "_meta.json")
			} else if agent == Codex {
				path, original = filepath.Join(dir, "config.toml"), "model = [broken"
			}
			fixtureFile(t, path, original)
			var err error
			switch agent {
			case ClaudeCode:
				_, err = applyClaudeCode(path, generatedApplyConfig(t, agent))
			case ClaudeDesktop:
				_, err = applyClaudeDesktop(dir, generatedApplyConfig(t, agent))
			case Codex:
				_, err = applyCodex(path, generatedApplyConfig(t, agent))
			}
			if err == nil || string(readFixture(t, path)) != original {
				t.Fatal("malformed configuration was overwritten", err)
			}
		})
	}
}
