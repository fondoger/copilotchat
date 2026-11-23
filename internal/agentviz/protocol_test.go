package agentviz

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMidSessionCaptureSelectsCurrentInstruction(t *testing.T) {
	s := New()
	history := []any{user("An old task"), assistant(textBlock("Old answer")), user("The current task"), assistant(toolBlock("current")), toolResult("current", "ok")}
	c := begin(t, s, "main", history, epoch)
	complete(c, epoch.Add(time.Second), "end_turn", textBlock("Done"))
	d, _ := s.Detail(c.turn.ID, epoch)
	if strings.Contains(string(d.Instruction), "old task") || !strings.Contains(string(d.Instruction), "current task") || c.turn.session.label != "The current task" {
		t.Fatalf("wrong current instruction: %s", d.Instruction)
	}
}

func TestHistoryRewriteUsesLatestToolEvidence(t *testing.T) {
	s := New()
	h := []any{user("Work")}
	complete(begin(t, s, "main", h, epoch), epoch.Add(time.Second), "tool_use", toolBlock("first"))
	h = append(h, assistant(toolBlock("first")), toolResult("first", "original large output"))
	complete(begin(t, s, "main", h, epoch.Add(2*time.Second)), epoch.Add(3*time.Second), "tool_use", toolBlock("second"))
	h[2] = toolResult("first", "[budgeted earlier output]")
	h = append(h, assistant(toolBlock("second")), toolResult("second", "new output"))
	complete(begin(t, s, "main", h, epoch.Add(4*time.Second)), epoch.Add(5*time.Second), "end_turn", textBlock("Done"))
	if sess := onlySession(t, s); len(sess.Turns) != 1 || sess.Turns[0].RequestCount != 3 {
		t.Fatalf("rewritten tool result split activity: %+v", sess)
	}
	// Rewind to an earlier instruction after completion is still a branch.
	h = append(h, assistant(textBlock("Done")), user("Next"))
	complete(begin(t, s, "main", h, epoch.Add(6*time.Second)), epoch.Add(7*time.Second), "end_turn", textBlock("Next result"))
	h[len(h)-1] = user("Edited next")
	complete(begin(t, s, "main", h, epoch.Add(8*time.Second)), epoch.Add(9*time.Second), "end_turn", textBlock("Revised"))
	if len(s.Snapshot(epoch).Sessions) != 2 {
		t.Fatal("earlier shared tool hid a rewind")
	}
}

func TestCanonicalHistoryPreservesToolPayloadAndWhitespace(t *testing.T) {
	p, err := parseRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"  indented code\n"},{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"f","cache_control":{"type":"ephemeral"},"input":{"cache_control":"meaningful","n":9007199254740993}}]}]}`), http.Header{}, "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	if atomText(p.atoms[0]) != "  indented code\n" || !strings.Contains(string(p.atoms[1].data), `"cache_control":"meaningful"`) || strings.Contains(string(p.atoms[1].data), "ephemeral") || !strings.Contains(string(p.atoms[1].data), "9007199254740993") {
		t.Fatalf("lossy canonicalization: %+v", p.atoms)
	}
}

func TestResponsesIncrementalHistoryAndChatStreaming(t *testing.T) {
	s := New()
	h := http.Header{"Thread-Id": {"thread"}}
	c := s.Begin("profile", "/responses", h, []byte(`{"model":"m","input":"First"}`), epoch)
	w := c.Wrap(httptest.NewRecorder())
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"resp_first","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Answer"}]}],"usage":{"input_tokens":20,"output_tokens":2,"input_tokens_details":{"cached_tokens":10}}}`))
	c.Finish(epoch.Add(time.Second), nil)
	c = s.Begin("profile", "/responses", h, []byte(`{"model":"m","previous_response_id":"resp_first","input":"Second"}`), epoch.Add(2*time.Second))
	w = c.Wrap(httptest.NewRecorder())
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_second\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":30,\"output_tokens\":3}}}\n\n"))
	c.Finish(epoch.Add(3*time.Second), nil)
	if sess := onlySession(t, s); len(sess.Turns) != 2 || sess.Turns[0].CachedTokens != 10 || sess.Turns[1].TotalTokens != 33 {
		t.Fatalf("responses=%+v", sess)
	}
	p := newResponseParser("/chat/completions")
	for _, frame := range []string{`{"id":"chat1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"Read","arguments":"{\"path\":"}}]}}]}`, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}]}`, `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":8}}}`, `[DONE]`} {
		p.feed([]byte("data: "+frame+"\n\n"), true)
	}
	p.finish()
	if !p.terminal || p.stop != "tool_calls" || p.usage.CachedTokens != 8 || len(p.output) != 2 || p.output[1].kind != "tool_use" || !json.Valid(p.output[1].data) {
		t.Fatalf("chat parser=%+v", p)
	}
}

func TestConcurrentSnapshotsAndClose(t *testing.T) {
	s := New()
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Go(func() {
			for j := 0; j < 20; j++ {
				c := s.Begin("profile", "/v1/messages", http.Header{}, []byte(`{"model":"m","messages":[{"role":"user","content":"Hi"}]}`), time.Now())
				if c != nil {
					complete(c, time.Now(), "end_turn", textBlock("Hello"))
					_, _ = s.Detail(c.turn.ID, time.Now())
				}
				_ = s.Snapshot(time.Now())
			}
		})
	}
	workers.Wait()
	s.Close()
	if len(s.Snapshot(time.Now()).Sessions) != 0 || s.bytes != 0 {
		t.Fatal("close retained data")
	}
}
