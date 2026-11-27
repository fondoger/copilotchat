package agentviz

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDetailWirePreservesLargeToolNumbers(t *testing.T) {
	d := TurnDetail{Instruction: json.RawMessage(`[{"type":"text","text":"<script>untrusted</script>"}]`), Calls: []ToolCall{{ID: "call", Input: json.RawMessage(`{"n":9007199254740993,"cache_control":"data"}`), Result: json.RawMessage(`"plain result"`)}}}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	// The browser uses ordinary JSON.parse: a payload string must retain all
	// digits without needing a custom JSON parser or an unsafe HTML renderer.
	var wire struct {
		Instruction string
		Calls       []struct{ Input, Result string }
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wire.Calls[0].Input, "9007199254740993") || wire.Calls[0].Result != "plain result" || !strings.Contains(wire.Instruction, "<script>untrusted</script>") {
		t.Fatalf("wire=%s", encoded)
	}
}

func TestDetailWirePreservesIndividualToolDefinitions(t *testing.T) {
	d := TurnDetail{Tools: json.RawMessage(`[
		{"name":"Read","description":"First line\nSecond line","input_schema":{"properties":{"n":{"const":9007199254740993}}}},
		{"type":"namespace","name":"files","tools":[{"type":"function","name":"write"}]},
		{"type":"function","function":{"name":"lookup"}},
		{"name":"Read","description":"Updated definition"},
		{"type":"web_search"}
	]`)}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Tools []toolDefinition
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	want := []string{"Read", "files.write", "lookup", "Read", "web_search"}
	if len(wire.Tools) != len(want) {
		t.Fatalf("tools = %+v", wire.Tools)
	}
	for i, name := range want {
		if wire.Tools[i].Name != name {
			t.Errorf("tool %d = %q, want %q", i, wire.Tools[i].Name, name)
		}
	}
	if !strings.Contains(wire.Tools[0].Definition, "9007199254740993") ||
		strings.Contains(wire.Tools[0].Definition, "Updated definition") ||
		!strings.Contains(wire.Tools[3].Definition, "Updated definition") ||
		strings.Contains(wire.Tools[1].Definition, "namespace") {
		t.Fatalf("definitions are not separate or lost original JSON: %+v", wire.Tools)
	}
}

func TestCacheLevelsRespectFixedBoundaries(t *testing.T) {
	for _, tc := range []struct {
		rate  float64
		level int
	}{{0, 1}, {.74999, 1}, {.75, 2}, {.89999, 2}, {.90, 3}, {.94999, 3}, {.95, 4}, {1, 4}} {
		sess := sessionAt("s", 0)
		sess.Turns[0].CacheHitRate = tc.rate
		sess.Turns[0].ToolCount = 1000
		s := Snapshot{Sessions: []SessionSummary{sess}}
		layout(&s)
		if got := s.Sessions[0].Turns[0].Level; got != tc.level {
			t.Fatalf("rate %v: level %d, want %d", tc.rate, got, tc.level)
		}
	}
}
