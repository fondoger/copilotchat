package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNativeCatalogMetadataAndCopilotLimits(t *testing.T) {
	var catalog Catalog
	err := json.Unmarshal([]byte(`{"data":[{
		"id":"claude-fixture","display_name":"Claude Fixture","created_at":"2026-10-01T00:00:00Z",
		"lifecycle":"deprecated","deprecated_at":"2026-10-20T00:00:00Z","retires_at":"2027-01-01T00:00:00Z","line":"sonnet",
		"max_input_tokens":1000000,"max_tokens":64000,"capabilities":{
			"family":"do-not-forward","image_input":{"supported":true},"pdf_input":{"supported":true},
			"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}},
			"effort":{"low":{"supported":true},"xhigh":{"supported":true}},"structured_outputs":{"supported":true}
		}}, {"id":"gpt-fixture","name":"GPT Fixture","capabilities":{"supports":{"vision":true,"reasoning_effort":["low","high"]},"limits":{"max_context_window_tokens":400000,"max_output_tokens":32000}}}]}`), &catalog)
	if err != nil {
		t.Fatal(err)
	}
	m := catalog.Data[0].Model("/v1/messages")
	if m.Name != "Claude Fixture" || m.ContextWindow != 1000000 || m.MaxOutputTokens != 64000 || m.CreatedAt != "2026-10-01T00:00:00Z" || m.Lifecycle != "deprecated" || m.Line != "sonnet" || m.DeprecatedAt == "" || m.RetiresAt == "" || !m.Vision || !m.PDF || !m.AdaptiveThinking || !reflect.DeepEqual(m.ThinkingEfforts, []string{"low", "xhigh"}) || strings.Contains(string(m.Capabilities), "family") {
		t.Fatalf("native metadata lost: %+v", m)
	}
	m = catalog.Data[1].Model("/responses")
	if m.ContextWindow != 400000 || m.MaxOutputTokens != 32000 || !m.Vision || !m.Reasoning || len(m.Capabilities) != 0 || !reflect.DeepEqual(m.ThinkingEfforts, []string{"low", "high"}) {
		t.Fatalf("Copilot metadata lost: %+v", m)
	}
}
