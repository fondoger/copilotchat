package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestForwardProfileClassifierMigration(t *testing.T) {
	for _, test := range []struct {
		name, fields string
		want         ForwardModelRef
	}{
		{"unset", ``, ForwardModelRef{}},
		{"auto default", `"claudeDefaults":{"CLAUDE_CODE_AUTO_MODE_MODEL":{"providerId":"auto","modelId":"auto-model"}}`, ForwardModelRef{"auto", "auto-model"}},
		{"legacy", `"claudeDesktopClassifierModel":{"providerId":"legacy","modelId":"legacy-model"},"claudeDefaults":{"CLAUDE_CODE_AUTO_MODE_MODEL":{"providerId":"auto","modelId":"auto-model"}}`, ForwardModelRef{"legacy", "legacy-model"}},
		{"empty legacy falls back", `"claudeDesktopClassifierModel":{},"claudeDefaults":{"CLAUDE_CODE_AUTO_MODE_MODEL":{"providerId":"auto","modelId":"auto-model"}}`, ForwardModelRef{"auto", "auto-model"}},
		{"canonical wins", `"claudeClassifierModel":{"providerId":"new","modelId":"new-model"},"claudeDesktopClassifierModel":{"providerId":"legacy","modelId":"legacy-model"},"claudeDefaults":{"CLAUDE_CODE_AUTO_MODE_MODEL":{"providerId":"auto","modelId":"auto-model"}}`, ForwardModelRef{"new", "new-model"}},
		{"empty canonical clears", `"claudeClassifierModel":{},"claudeDesktopClassifierModel":{"providerId":"legacy","modelId":"legacy-model"},"claudeDefaults":{"CLAUDE_CODE_AUTO_MODE_MODEL":{"providerId":"auto","modelId":"auto-model"}}`, ForwardModelRef{}},
		{"null canonical clears", `"claudeClassifierModel":null,"claudeDesktopClassifierModel":{"providerId":"legacy","modelId":"legacy-model"},"claudeDefaults":{"CLAUDE_CODE_AUTO_MODE_MODEL":{"providerId":"auto","modelId":"auto-model"}}`, ForwardModelRef{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got ForwardProfile
			if err := json.Unmarshal([]byte("{"+test.fields+"}"), &got); err != nil {
				t.Fatal(err)
			}
			if got.ClaudeClassifierModel != test.want {
				t.Fatalf("classifier = %+v, want %+v", got.ClaudeClassifierModel, test.want)
			}
			if _, exists := got.ClaudeDefaults["CLAUDE_CODE_AUTO_MODE_MODEL"]; exists {
				t.Fatal("legacy auto-mode default survived migration")
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields["claudeDesktopClassifierModel"]; exists {
				t.Fatal("legacy classifier field written", string(encoded))
			}
			if _, exists := fields["claudeClassifierModel"]; !exists {
				t.Fatal("canonical classifier field missing", string(encoded))
			}
			var roundTrip ForwardProfile
			if err := json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatal(err)
			}
			if roundTrip.ClaudeClassifierModel != test.want || len(roundTrip.ClaudeDefaults) != 0 {
				t.Fatalf("round trip changed classifier: %+v", roundTrip)
			}
		})
	}
}

func TestForwardProfileClassifierMigrationPreservesOtherFields(t *testing.T) {
	data := []byte(`{"id":"work-id","name":"work","models":[{"providerId":"p","modelId":"chat","enabled":true}],"agent":"Claude Code","claudeDefaults":{"ANTHROPIC_DEFAULT_SONNET_MODEL":{"providerId":"p","modelId":"chat"},"CLAUDE_CODE_AUTO_MODE_MODEL":{"providerId":"p","modelId":"classifier"}},"claudePermissionMode":"auto","codexModel":{"providerId":"p","modelId":"codex"},"codexRemoteCompaction":true}`)
	want := ForwardProfile{
		ID: "work-id", Name: "work", Models: []ForwardModel{{ProviderID: "p", ModelID: "chat", Enabled: true}}, Agent: "Claude Code",
		ClaudeDefaults:       map[string]ForwardModelRef{"ANTHROPIC_DEFAULT_SONNET_MODEL": {ProviderID: "p", ModelID: "chat"}},
		ClaudePermissionMode: "auto", ClaudeClassifierModel: ForwardModelRef{ProviderID: "p", ModelID: "classifier"},
		CodexModel: ForwardModelRef{ProviderID: "p", ModelID: "codex"}, CodexRemoteCompaction: true,
	}
	var got ForwardProfile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip ForwardProfile
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, want) {
		t.Fatalf("round trip = %+v, want %+v", roundTrip, want)
	}
}
