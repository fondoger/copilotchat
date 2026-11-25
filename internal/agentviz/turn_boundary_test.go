package agentviz

import (
	"strings"
	"testing"
	"time"
)

func TestOutstandingResultProofDoesNotMaskReplacementOrCancellation(t *testing.T) {
	for _, interruptMarker := range []bool{false, true} {
		s := New()
		history := []any{user("Original task")}
		first := begin(t, s, "main", history, epoch)
		complete(first, epoch.Add(time.Second), "tool_use", toolBlock("pending"))
		history = append(history, assistant(toolBlock("pending")))
		if interruptMarker {
			history = append(history, toolResult("pending", "canceled"), user("[Request interrupted by user for tool use]"))
		} else {
			history = append(history, toolResult("unrelated", "an old result"))
		}
		history = append(history, user("A replacement task"))
		next := begin(t, s, "main", history, epoch.Add(2*time.Second))
		if next.turn == first.turn || first.turn.Status != "failed" {
			t.Fatal("unrelated result or explicit interruption was mistaken for normal continuation")
		}
		complete(next, epoch.Add(3*time.Second), "end_turn", textBlock("Replacement completed"))
	}
}

func TestResolvedToolResultsCannotProveAnOpenLoop(t *testing.T) {
	task := &turn{TurnSummary: TurnSummary{Status: "waiting"}, calls: []ToolCall{{ID: "resolved", EndedAt: 1}, {ID: "pending"}}}
	result := func(id string) atom {
		return makeAtom("user", object{"type": "tool_result", "tool_use_id": id, "content": "result"})
	}
	if toolContinuation(task, []atom{result("resolved"), makeAtom("user", textBlock("New input"))}) != nil {
		t.Fatal("an already consumed result proved continuation")
	}
	if toolContinuation(task, []atom{result("pending")}) == nil {
		t.Fatal("outstanding result did not prove continuation")
	}
	task.Status = "failed"
	if toolContinuation(task, []atom{result("pending")}) != nil {
		t.Fatal("a failed request lost its separate retry attempt")
	}
}

func TestToolResultWithSupplementalTextKeepsActiveTurn(t *testing.T) {
	for _, extra := range []string{
		"Base directory for this skill: /fixture/mygo-automation\n\nUse the native automation workflow.",
		"Additional environment context supplied while the tools were running.",
		"The user sent a new message while you were working: include the sidebar too.",
	} {
		for _, textFirst := range []bool{false, true} {
			s := New()
			instruction := "Centralize font fallback configuration."
			history := []any{user(instruction)}
			skill := object{"type": "tool_use", "id": "skill", "name": "Skill", "input": object{"skill": "mygo-automation"}}
			read := toolBlock("read")
			first := begin(t, s, "main", history, epoch)
			complete(first, epoch.Add(time.Second), "tool_use", skill, read)
			result := object{"type": "tool_result", "tool_use_id": "skill", "content": "Launching skill"}
			content := []any{result, textBlock(extra)}
			if textFirst {
				content = []any{textBlock(extra), result}
			}
			history = append(history, assistant(skill, read), object{"role": "user", "content": content}, toolResult("read", "source text"))
			next := begin(t, s, "main", history, epoch.Add(2*time.Second))
			if next.turn != first.turn {
				t.Fatalf("normal tool continuation split the turn: status=%s error=%q", first.turn.Status, first.turn.Error)
			}
			complete(next, epoch.Add(3*time.Second), "end_turn", textBlock("Completed"))
			detail, _ := s.Detail(first.turn.ID, epoch.Add(4*time.Second))
			if detail.Status != "completed" || detail.Error != "" || detail.RequestCount != 2 || detail.ToolCount != 2 || detail.InstructionPreview != instruction {
				t.Fatal("normal tool cycle lost its original turn or metrics")
			}
			found := false
			for _, injection := range detail.Injections {
				if strings.Contains(string(injection), "while") || strings.Contains(string(injection), "Base directory") {
					found = true
				}
			}
			if !found {
				t.Fatal("supplemental input was dropped from turn details")
			}
			history = append(history, assistant(textBlock("Completed")), user("Start the next task"))
			fresh := begin(t, s, "main", history, epoch.Add(5*time.Second))
			complete(fresh, epoch.Add(6*time.Second), "end_turn", textBlock("Next complete"))
			if fresh.turn == first.turn || fresh.turn.Number != 2 || first.turn.Status != "completed" {
				t.Fatal("a real instruction after end_turn did not start the next turn")
			}
		}
	}
}
