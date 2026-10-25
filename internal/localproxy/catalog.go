package localproxy

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"slices"
)

// Codex-native catalogs require a base instruction template. Use Codex's own
// generic fallback prompt; no extra instructions are injected into inference.
// Attribution and Apache-2.0 license accompany the embedded file.
//
//go:embed codex/prompt.md
var codexPrompt string

// Codex uses a native catalog rather than the OpenAI data[] envelope. Keep its
// slug and display name identical to the public ID and advertise only the
// native or bridged Responses models that this endpoint can actually route.
func CodexCatalog(models []Model) []byte {
	data := []map[string]any{}
	for _, model := range models {
		m := model.Model
		if !supportsResponses(m) {
			continue
		}
		efforts := []map[string]string{}
		for _, effort := range m.ThinkingEfforts {
			if slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, effort) {
				efforts = append(efforts, map[string]string{"effort": effort, "description": effort})
			}
		}
		modalities := []string{"text"}
		if m.Vision {
			modalities = append(modalities, "image")
		}
		row := map[string]any{
			"id": model.ID, "name": model.ID, "slug": model.ID, "display_name": model.ID,
			"description": "", "supported_reasoning_levels": efforts,
			"shell_type": "shell_command", "visibility": "list", "supported_in_api": true,
			"priority": len(data), "support_verbosity": false,
			"supports_parallel_tool_calls":         m.Tools,
			"supports_search_tool":                 m.Tools,
			"base_instructions":                    codexPrompt,
			"supports_reasoning_summary_parameter": m.Reasoning,
			"truncation_policy":                    map[string]any{"mode": "bytes", "limit": 10000},
			"experimental_supported_tools":         []string{}, "input_modalities": modalities,
		}
		if len(efforts) > 0 {
			row["default_reasoning_level"] = efforts[0]["effort"]
			if slices.Contains(m.ThinkingEfforts, "medium") {
				row["default_reasoning_level"] = "medium"
			}
		}
		data = append(data, row)
	}
	encoded, _ := json.Marshal(map[string]any{"models": data})
	return encoded
}

func writeCodexModels(w http.ResponseWriter, models []Model) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(CodexCatalog(models))
}
