package agentviz

import "strings"

// A user-role batch can carry tool results, loaded Skill instructions, harness
// notices, or queued user input. Matching an outstanding tool ID proves the
// agent is resuming its open tool loop; text beside that result is not evidence
// that the preceding turn was interrupted. Closed turns and old/unknown result
// IDs cannot establish this continuation.
func toolContinuation(t *turn, delta []atom) []atom {
	if t == nil || t.Status != "waiting" && t.Status != "running" {
		return nil
	}
	pending := map[string]bool{}
	for _, call := range t.calls {
		if call.ID != "" && call.EndedAt == 0 {
			pending[call.ID] = true
		}
	}
	if len(pending) == 0 {
		return nil
	}
	for i, a := range delta {
		if a.role != "user" || a.kind != "tool_result" {
			continue
		}
		var result object
		if decode(a.data, &result) != nil || !pending[str(result["tool_use_id"])] {
			continue
		}
		// Results and attachments can be split across adjacent user messages or
		// reordered within a message. Bound the supplemental input by assistant
		// messages so a history-rewrite fallback does not copy earlier prompts.
		start, end := i, i+1
		for start > 0 && delta[start-1].role != "assistant" {
			start--
		}
		for end < len(delta) && delta[end].role != "assistant" {
			end++
		}
		for _, input := range delta[start:end] {
			if input.role == "user" && input.kind == "text" && strings.HasPrefix(strings.TrimSpace(atomText(input)), "[Request interrupted by user") {
				return nil
			}
		}
		return delta[start:end]
	}
	return nil
}
