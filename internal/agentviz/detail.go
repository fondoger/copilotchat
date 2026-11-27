package agentviz

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Payloads travel to the browser as display strings. Parsing tool arguments into
// JavaScript numbers would round integers above 2^53, even though the observer and
// gateway preserve them exactly. Keep JSON structure in the in-memory model and
// pretty-print its original numeric spelling only at this presentation boundary.
func displayJSON(data json.RawMessage) any {
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil
	}
	var text string
	if json.Unmarshal(data, &text) == nil {
		return text
	}
	var out bytes.Buffer
	if json.Indent(&out, data, "", "  ") != nil {
		return string(data)
	}
	return out.String()
}

func environmentDisplay(data json.RawMessage) string {
	var text string
	if json.Unmarshal(data, &text) == nil {
		return text
	}
	var clipped struct {
		Preview string `json:"preview"`
	}
	if json.Unmarshal(data, &clipped) == nil && clipped.Preview != "" {
		return clipped.Preview + "\n…"
	}
	return ""
}

func previewInstruction(data json.RawMessage) string {
	var blocks []object
	if decode(data, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if text := str(b["text"]); text != "" {
			parts = append(parts, text)
		}
		if str(b["type"]) == "image" || str(b["type"]) == "input_image" {
			parts = append(parts, "[Image]")
		}
	}
	preview := []rune(strings.Join(parts, "\n"))
	if len(preview) > 800 {
		return string(preview[:800]) + "…"
	}
	return string(preview)
}

func (t ToolCall) MarshalJSON() ([]byte, error) {
	type alias ToolCall
	return json.Marshal(struct {
		alias
		Input  any `json:"input"`
		Result any `json:"result"`
	}{alias: alias(t), Input: displayJSON(t.Input), Result: displayJSON(t.Result)})
}

func (d TurnDetail) MarshalJSON() ([]byte, error) {
	type alias TurnDetail
	injections := make([]any, 0, len(d.Injections))
	for _, v := range d.Injections {
		injections = append(injections, displayJSON(v))
	}
	return json.Marshal(struct {
		alias
		System      any   `json:"system"`
		Tools       any   `json:"tools"`
		Instruction any   `json:"instruction"`
		Reply       any   `json:"reply"`
		Injections  []any `json:"injections"`
	}{alias: alias(d), System: displayJSON(d.System), Tools: displayJSON(d.Tools), Instruction: displayJSON(d.Instruction), Reply: displayJSON(d.Reply), Injections: injections})
}
