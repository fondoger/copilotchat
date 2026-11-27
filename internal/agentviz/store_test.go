package agentviz

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var epoch = time.Unix(1_800_000_000, 0)

func user(text string) object         { return object{"role": "user", "content": text} }
func assistant(content ...any) object { return object{"role": "assistant", "content": content} }
func textBlock(text string) object    { return object{"type": "text", "text": text} }
func toolBlock(id string) object {
	return object{"type": "tool_use", "id": id, "name": "Read", "input": object{"path": "file.go", "large": json.Number("9007199254740993")}}
}
func toolResult(id, text string) object {
	return object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": id, "content": text}}}
}
func begin(t *testing.T, s *Store, id string, messages []any, at time.Time) *Capture {
	t.Helper()
	h := http.Header{}
	if id != "" {
		h.Set("X-Claude-Code-Session-Id", id)
	}
	c := s.Begin("profile", "/v1/messages", h, raw(object{"model": "alias", "system": "system prompt", "tools": []any{object{"name": "Read"}}, "messages": messages}), at)
	if c == nil {
		t.Fatal("capture not created")
	}
	return c
}
func complete(c *Capture, at time.Time, reason string, content ...any) {
	c.SetModel("actual-model", "Actual model")
	w := c.Wrap(httptest.NewRecorder())
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw(object{"id": fmt.Sprintf("msg_%d", at.UnixNano()), "content": content, "stop_reason": reason, "usage": object{"input_tokens": 10, "cache_creation_input_tokens": 20, "cache_read_input_tokens": 70, "output_tokens": 5}}))
	c.Finish(at, nil)
}
func onlySession(t *testing.T, s *Store) SessionSummary {
	t.Helper()
	snap := s.Snapshot(epoch.Add(time.Hour))
	if len(snap.Sessions) != 1 {
		t.Fatalf("sessions=%+v", snap.Sessions)
	}
	return snap.Sessions[0]
}

func TestAgentTurnToolsInjectionsAndHookContinuation(t *testing.T) {
	s := New()
	history := []any{user("Inspect the file")}
	c := begin(t, s, "main", history, epoch)
	complete(c, epoch.Add(time.Second), "end_turn", toolBlock("call1")) // Actual tools override a misleading stop reason.
	history = append(history, assistant(toolBlock("call1")), toolResult("call1", "result"), user("<system-reminder>Environment changed</system-reminder>"))
	c = begin(t, s, "main", history, epoch.Add(2*time.Second))
	complete(c, epoch.Add(3*time.Second), "end_turn", textBlock("Done"))
	history = append(history, assistant(textBlock("Done")), user("Stop hook feedback:\nCheck the result again"))
	c = begin(t, s, "main", history, epoch.Add(9*time.Second))
	complete(c, epoch.Add(10*time.Second), "end_turn", textBlock("Verified"))
	sess := onlySession(t, s)
	if len(sess.Turns) != 1 {
		t.Fatalf("synthetic inputs created turns: %+v", sess.Turns)
	}
	turn := sess.Turns[0]
	if turn.Status != "completed" || turn.RequestCount != 3 || turn.ToolCount != 1 || turn.DurationMS != 10000 {
		t.Fatalf("turn=%+v", turn)
	}
	if turn.InputTokens != 300 || turn.CachedTokens != 210 || turn.UncachedTokens != 90 || turn.TotalTokens != 315 || turn.CacheHitRate != .7 {
		t.Fatalf("usage=%+v", turn.Usage)
	}
	d, ok := s.Detail(turn.ID, epoch.Add(time.Hour))
	if !ok || len(d.Calls) != 1 || string(d.Calls[0].Result) != `"result"` || d.Calls[0].EndedAt == 0 {
		t.Fatalf("tool record=%+v", d.Calls)
	}
	if strings.Contains(string(d.Instruction), "feedback") || !strings.Contains(string(d.Reply), "Verified") || len(d.Injections) < 2 {
		t.Fatalf("detail=%+v", d)
	}
	history = append(history, assistant(textBlock("Verified")), user("Now fix it"))
	complete(begin(t, s, "main", history, epoch.Add(20*time.Second)), epoch.Add(21*time.Second), "end_turn", textBlock("Fixed"))
	if sess := onlySession(t, s); len(sess.Turns) != 2 || sess.Turns[1].InstructionPreview != "Now fix it" || sess.Label != "Inspect the file" {
		t.Fatal("new turn must retain its own instruction beside the original session title")
	}
}

func TestEnvironmentDetailsKeepCompleteSystemText(t *testing.T) {
	const environment = "# Environment\r\n\r\nYou have been invoked in the following environment:\r\n  Platform: Windows\r\n\r\n# MCP Server Instructions\r\n## Example\r\nKeep <tag> as text.\r\n"
	for _, tt := range []struct {
		name    string
		content any
	}{
		{"string", environment},
		{"text blocks", []any{textBlock(environment), textBlock("Separate system note")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := New()
			history := []any{user("Inspect the workspace"), object{"role": "system", "content": tt.content}}
			first := begin(t, s, "main", history, epoch)
			complete(first, epoch.Add(time.Second), "tool_use", toolBlock("read"))
			history = append(history, assistant(toolBlock("read")), toolResult("read", "contents"))
			next := begin(t, s, "main", history, epoch.Add(2*time.Second))
			complete(next, epoch.Add(3*time.Second), "end_turn", textBlock("Done"))

			detail, ok := s.Detail(first.turn.ID, epoch.Add(4*time.Second))
			if !ok || detail.RequestCount != 2 || detail.InstructionPreview != "Inspect the workspace" {
				t.Fatalf("environment changed the observed turn: %+v", detail.TurnSummary)
			}
			var wire struct {
				Environment []string `json:"environment"`
				System      string   `json:"system"`
				Injections  []string `json:"injections"`
			}
			if err := json.Unmarshal(raw(detail), &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Environment) != 1 || wire.Environment[0] != environment {
				t.Fatalf("environment does not preserve the complete text: %q", wire.Environment)
			}
			if wire.System != "system prompt" || strings.Contains(strings.Join(wire.Injections, "\n"), "# Environment") {
				t.Fatal("environment was duplicated into another section")
			}
			if tt.name == "text blocks" && !strings.Contains(strings.Join(wire.Injections, "\n"), "Separate system note") {
				t.Fatal("unrelated system text was dropped")
			}
		})
	}
}

func TestEnvironmentDetailsRequireTheSystemMessageSignature(t *testing.T) {
	const environment = "# Environment\nYou have been invoked in the following environment:\nExample"
	s := New()
	c := begin(t, s, "main", []any{
		object{"role": "system", "content": "# Environment\nOrdinary project notes"},
		object{"role": "developer", "content": environment},
		user(environment),
	}, epoch)
	complete(c, epoch.Add(time.Second), "end_turn", textBlock("Done"))
	detail, ok := s.Detail(c.turn.ID, epoch.Add(2*time.Second))
	if !ok || len(detail.Environment) != 0 || !strings.Contains(detail.InstructionPreview, "# Environment") {
		t.Fatal("non-environment text was removed from its original section")
	}
}

func TestForkRewindAndIndependentIdenticalPrompts(t *testing.T) {
	s := New()
	h := []any{user("Start")}
	complete(begin(t, s, "main", h, epoch), epoch.Add(time.Second), "end_turn", textBlock("First"))
	h = append(h, assistant(textBlock("First")), user("Second"))
	complete(begin(t, s, "main", h, epoch.Add(2*time.Second)), epoch.Add(3*time.Second), "end_turn", textBlock("Second answer"))
	original := onlySession(t, s)
	anchor := original.Turns[0].ID
	fork := []any{user("Start"), assistant(textBlock("First")), user("Branch task")}
	complete(begin(t, s, "fork", fork, epoch.Add(4*time.Second)), epoch.Add(5*time.Second), "end_turn", textBlock("Fork answer"))
	rewind := []any{user("Start"), assistant(textBlock("First")), user("Edited second instruction")}
	complete(begin(t, s, "main", rewind, epoch.Add(6*time.Second)), epoch.Add(7*time.Second), "end_turn", textBlock("Edited answer"))
	complete(begin(t, s, "unrelated", []any{user("Start")}, epoch.Add(8*time.Second)), epoch.Add(9*time.Second), "end_turn", textBlock("Unrelated"))
	snap := s.Snapshot(epoch.Add(time.Hour))
	if len(snap.Sessions) != 4 {
		t.Fatalf("sessions=%+v", snap.Sessions)
	}
	branches := 0
	for _, sess := range snap.Sessions {
		if sess.ParentID != "" {
			branches++
			if sess.ParentID != original.ID || sess.ForkTurnID != anchor || sess.GroupID != original.GroupID || len(sess.Turns) != 1 {
				t.Fatalf("fork=%+v", sess)
			}
		}
	}
	if branches != 2 {
		t.Fatalf("branches=%d", branches)
	}
}

func TestFailedAttemptsRetainRetryIdentity(t *testing.T) {
	s := New()
	h := []any{user("Work")}
	for i := 0; i < 3; i++ {
		c := begin(t, s, "main", h, epoch.Add(time.Duration(i)*time.Second))
		w := c.Wrap(httptest.NewRecorder())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":7}}}\n\n"))
		c.Finish(epoch.Add(time.Duration(i+1)*time.Second), nil)
	}
	complete(begin(t, s, "main", h, epoch.Add(4*time.Second)), epoch.Add(5*time.Second), "end_turn", textBlock("OK"))
	sess := onlySession(t, s)
	if len(sess.Turns) != 4 {
		t.Fatalf("turns=%+v", sess.Turns)
	}
	for i, turn := range sess.Turns {
		want := "failed"
		if i == 3 {
			want = "completed"
		}
		if turn.Number != 1 || turn.Attempt != i+1 || turn.Status != want {
			t.Fatalf("attempt=%+v", turn)
		}
	}
}

func TestSchedulerMetadataSubagentAndCompaction(t *testing.T) {
	s := New()
	h := []any{user("Root task")}
	complete(begin(t, s, "root", h, epoch), epoch.Add(time.Second), "end_turn", textBlock("Done"))
	root := onlySession(t, s)
	headers := http.Header{"X-Claude-Code-Session-Id": {"root"}, "X-Claude-Code-Agent-Id": {"child"}}
	c := s.Begin("profile", "/v1/messages", headers, raw(object{"model": "m", "messages": []any{user("Independent subagent task")}}), epoch.Add(2*time.Second))
	complete(c, epoch.Add(3*time.Second), "end_turn", textBlock("Child result"))
	snap := s.Snapshot(epoch.Add(time.Hour))
	if len(snap.Sessions) != 2 || snap.Sessions[0].ParentID != root.ID {
		t.Fatalf("explicit child=%+v", snap.Sessions)
	}
	headers = http.Header{"User-Agent": {"claude-cli workload/cron"}}
	h = append(h, assistant(textBlock("Done")), user("Scheduled check"))
	c = s.Begin("profile", "/v1/messages", headers, raw(object{"model": "m", "metadata": object{"user_id": `{"session_id":"root"}`}, "messages": h}), epoch.Add(4*time.Second))
	complete(c, epoch.Add(5*time.Second), "end_turn", textBlock("Checked"))
	if c.turn.Trigger != "scheduler" || c.turn.session.id != root.ID {
		t.Fatalf("scheduler=%+v", c.turn.TurnSummary)
	}
	compacted := []any{user("This session is being continued from a previous conversation that ran out of context.\nSummary"), user("A new instruction")}
	c = begin(t, s, "root", compacted, epoch.Add(6*time.Second))
	complete(c, epoch.Add(7*time.Second), "end_turn", textBlock("Continued"))
	if c.turn.session.id != root.ID || len(s.Snapshot(epoch.Add(time.Hour)).Sessions) != 2 {
		t.Fatal("compaction invented a fork")
	}
}

func TestHistorySharingBinaryOmissionAndPrecision(t *testing.T) {
	s := New()
	binary := strings.Repeat("ABCD", 250000)
	image := object{"type": "image", "source": object{"type": "base64", "media_type": "image/png", "data": binary}}
	h := []any{object{"role": "user", "content": []any{textBlock("Inspect image"), image}}}
	complete(begin(t, s, "root", h, epoch), epoch.Add(time.Second), "tool_use", toolBlock("read"))
	h = append(h, assistant(toolBlock("read")), toolResult("read", "content"))
	complete(begin(t, s, "root", h, epoch.Add(2*time.Second)), epoch.Add(3*time.Second), "end_turn", textBlock("Done"))
	sess := onlySession(t, s)
	d, _ := s.Detail(sess.Turns[0].ID, epoch)
	if strings.Contains(string(d.Instruction), binary[:100]) || !strings.Contains(string(d.Instruction), "sha256") {
		t.Fatal("retained binary data")
	}
	if !strings.Contains(string(d.Calls[0].Input), "9007199254740993") {
		t.Fatal("tool number lost precision")
	}
	before := s.bytes
	fork := append(append([]any{}, h...), assistant(textBlock("Done")), user("New branch"))
	complete(begin(t, s, "fork", fork, epoch.Add(4*time.Second)), epoch.Add(5*time.Second), "end_turn", textBlock("Fork done"))
	if s.bytes-before > 10000 {
		t.Fatalf("duplicated shared prefix: %d bytes", s.bytes-before)
	}
	s.maxTurns = 1
	s.mu.Lock()
	s.prune()
	s.mu.Unlock()
	if len(s.sessions) != 0 || s.bytes != 0 || len(s.nodes) != 0 || len(s.responses) != 0 {
		t.Fatal("family eviction leaked retained data")
	}
}

func TestSSETerminalFramingAndCumulativeUsage(t *testing.T) {
	frames := "event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":70,\"cache_creation_input_tokens\":20,\"output_tokens\":1}}}\r\n\r\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\r\n\r\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"你好\"}}\r\n\r\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\r\n\r\n" +
		"data: {\"type\":\"message_stop\"}\r\n\r\n"
	for _, size := range []int{1, 7, 8192} {
		s := New()
		c := begin(t, s, "s", []any{user("Hello")}, epoch)
		rec := httptest.NewRecorder()
		w := c.Wrap(rec)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < len(frames); i += size {
			_, _ = w.Write([]byte(frames[i:min(i+size, len(frames))]))
		}
		c.Finish(epoch.Add(time.Second), nil)
		turn := onlySession(t, s).Turns[0]
		if rec.Body.String() != frames || turn.Status != "completed" || turn.InputTokens != 100 || turn.OutputTokens != 5 || turn.FirstChunkMS == nil {
			t.Fatalf("size %d: %+v", size, turn)
		}
	}
	p := newResponseParser("/v1/messages")
	p.feed([]byte(strings.TrimSuffix(frames, "\r\n")), true)
	p.finish()
	if p.terminal {
		t.Fatal("unterminated SSE event counted as success")
	}
}

func TestClientDisconnectAndEmptyEndTurn(t *testing.T) {
	s := New()
	c := begin(t, s, "one", []any{user("Hello")}, epoch)
	complete(c, epoch.Add(time.Second), "end_turn")
	if onlySession(t, s).Turns[0].Status != "completed" {
		t.Fatal("valid empty answer marked failed")
	}
	c = begin(t, s, "two", []any{user("Other")}, epoch)
	c.status = 200
	c.writeError = fmt.Errorf("client closed")
	c.parser.feed([]byte(`{"content":[],"stop_reason":"end_turn"}`), false)
	c.Finish(epoch.Add(time.Second), nil)
	if c.turn.Status != "failed" {
		t.Fatal("client disconnect not failed")
	}
}
