package agentviz

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Replay captured response bytes, reconstructing only the missing request tails.
// Tool results and Skill text below are explicitly synthetic; the truncated
// exports cannot establish what those original request tails contained.
func TestLocalToolLoopResponsesWithConstructedResults(t *testing.T) {
	paths := filepath.SplitList(os.Getenv("AGENTVIZ_TOOL_LOOP_CAPTURES"))
	if len(paths) == 0 {
		t.Skip("set AGENTVIZ_TOOL_LOOP_CAPTURES to local tool-loop GoVisual exports")
	}
	var logs []localCapture
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var log localCapture
		if err = json.Unmarshal(b, &log); err != nil {
			t.Fatal(err)
		}
		logs = append(logs, log)
	}
	slices.SortFunc(logs, func(a, b localCapture) int { return a.Timestamp.Compare(b.Timestamp) })
	if len(logs) < 2 {
		t.Fatal("need multiple captured responses")
	}
	for _, log := range logs {
		if !strings.Contains(log.RequestBody, "[truncated by govisual]") || log.RequestBody != logs[0].RequestBody {
			t.Fatal("expected the same preserved, truncated request prefix")
		}
		if log.StatusCode != 200 || strings.Contains(log.ResponseBody, "[truncated by govisual]") {
			t.Fatal("response is not a complete successful capture")
		}
	}
	history := completeCapturedMessages(logs[0].RequestBody)
	if len(history) == 0 {
		t.Fatal("no complete prefix messages survived")
	}
	instruction := "Constructed replay: centralize font fallback configuration."
	history = append(history, raw(user(instruction)))
	s := New()
	var first *turn
	requests, skillInjections := 0, 0
	for _, log := range logs {
		body := raw(object{"model": "capture-fixture", "messages": history})
		c := s.Begin("capture-profile", "/v1/messages", log.RequestHeaders, body, log.Timestamp)
		if c == nil {
			t.Fatal("constructed request could not be observed")
		}
		if first == nil {
			first = c.turn
		} else if c.turn != first {
			t.Fatalf("normal captured tool cycle split: previous state=%s error=%q", first.Status, first.Error)
		}
		recorder := httptest.NewRecorder()
		w := c.Wrap(recorder)
		w.Header().Set("Content-Type", log.ResponseHeaders.Get("Content-Type"))
		w.WriteHeader(log.StatusCode)
		_, _ = w.Write([]byte(log.ResponseBody))
		c.Finish(log.Timestamp.Add(time.Duration(log.Duration)*time.Millisecond), nil)
		if recorder.Body.String() != log.ResponseBody {
			t.Fatal("observer changed the captured response")
		}
		requests++
		if c.turn.Status == "completed" {
			break
		} // Later requests have unknown, missing user inputs.
		if c.turn.Status != "waiting" {
			t.Fatalf("captured tool response became %s", c.turn.Status)
		}
		var content []json.RawMessage
		for _, a := range c.parser.output {
			content = append(content, a.data)
		}
		history = append(history, raw(object{"role": "assistant", "content": content}))
		var results []any
		for _, call := range c.turn.calls {
			if call.EndedAt != 0 {
				continue
			}
			results = append(results, object{"type": "tool_result", "tool_use_id": call.ID, "content": "Constructed tool result for local replay"})
			if call.Name == "Skill" {
				results = append(results, textBlock("Base directory for this skill: /fixture/mygo-automation\n\nConstructed skill instructions for replay."))
				skillInjections++
			}
		}
		history = append(history, raw(object{"role": "user", "content": results}))
	}
	if first == nil || first.Status != "completed" || first.Error != "" || first.InstructionPreview != instruction || len(s.Snapshot(time.Now()).Sessions) != 1 || len(first.session.turns) != 1 || skillInjections == 0 {
		t.Fatal("captured tool workflow did not finish as one intact turn")
	}
	t.Logf("%d original response bodies, %d tool calls and %d constructed Skill injections stayed in one completed turn; missing request tails were not replayed verbatim", requests, first.ToolCount, skillInjections)
}
