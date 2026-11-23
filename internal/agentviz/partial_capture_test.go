package agentviz

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Diagnostic extraction only. Stop at the first incomplete message and never
// treat the missing tail of an exported capture as an actual complete request.
func completeCapturedMessages(body string) []json.RawMessage {
	d := json.NewDecoder(strings.NewReader(body))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil
		}
		if key == "messages" {
			if token, err := d.Token(); err != nil || token != json.Delim('[') {
				return nil
			}
			var messages []json.RawMessage
			for d.More() {
				var message json.RawMessage
				if d.Decode(&message) != nil {
					break
				}
				messages = append(messages, message)
			}
			return messages
		}
		var skip json.RawMessage
		if d.Decode(&skip) != nil {
			return nil
		}
	}
	return nil
}

func TestLocalInterruptedCapturePrefixWithConstructedTail(t *testing.T) {
	paths := filepath.SplitList(os.Getenv("AGENTVIZ_INTERRUPTED_CAPTURES"))
	if len(paths) == 0 {
		t.Skip("set AGENTVIZ_INTERRUPTED_CAPTURES to local interrupted/rewind GoVisual exports")
	}
	type capture struct {
		Timestamp                       time.Time
		StatusCode                      int
		RequestHeaders, ResponseHeaders http.Header
		RequestBody, ResponseBody       string
	}
	var logs []capture
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var log capture
		if json.Unmarshal(b, &log) != nil {
			t.Fatal("invalid outer capture JSON")
		}
		if strings.Contains(log.RequestBody, "[truncated by govisual]") {
			logs = append(logs, log)
		}
	}
	slices.SortFunc(logs, func(a, b capture) int { return a.Timestamp.Compare(b.Timestamp) })
	if len(logs) < 3 {
		t.Fatal("expected truncated main, interruption and rewind records")
	}
	failed := -1
	for i, log := range logs {
		if log.RequestBody != logs[0].RequestBody {
			t.Fatal("preserved prefixes differ; inspect before constructing a continuation case")
		}
		if log.StatusCode == 502 && strings.Contains(log.ResponseBody, "canceled") {
			failed = i
		}
	}
	if failed < 1 || failed+1 >= len(logs) {
		t.Fatal("missing captured cancellation or following request")
	}
	base := completeCapturedMessages(logs[0].RequestBody)
	if len(base) == 0 {
		t.Fatal("no complete messages survived capture truncation")
	}
	for _, log := range []capture{logs[failed], logs[failed+1]} {
		if log.RequestHeaders.Get("X-Claude-Code-Session-Id") == logs[0].RequestHeaders.Get("X-Claude-Code-Session-Id") {
			t.Fatal("expected observed wire-ID rotation")
		}
	}
	// The seven exports omit the edited instruction. Use the real preserved
	// prefix and real headers, then explicitly constructed tails for this test.
	s := New()
	body := func(messages []json.RawMessage) []byte { return raw(object{"model": "fixture", "messages": messages}) }
	mainInput := append(slices.Clone(base), raw(user("constructed main checkpoint")))
	main := s.Begin("capture-profile", "/v1/messages", logs[0].RequestHeaders, body(mainInput), logs[0].Timestamp)
	if main == nil {
		t.Fatal("could not observe complete captured prefix")
	}
	complete(main, logs[0].Timestamp.Add(time.Second), "end_turn", textBlock("checkpoint"))
	prefix := append(slices.Clone(mainInput), raw(assistant(textBlock("checkpoint"))))
	common := "1. Fork connectors should have one elbow. " + strings.Repeat("same prefix ", 100)
	interruptedInput := append(slices.Clone(prefix), raw(user(common+"original tail")))
	interrupted := s.Begin("capture-profile", "/v1/messages", logs[failed].RequestHeaders, body(interruptedInput), logs[failed].Timestamp)
	w := interrupted.Wrap(httptest.NewRecorder())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(502)
	_, _ = w.Write([]byte(logs[failed].ResponseBody))
	interrupted.Finish(logs[failed].Timestamp.Add(time.Second), context.Canceled)
	editedInput := append(slices.Clone(prefix), raw(user(common+"edited tail")))
	edited := s.Begin("capture-profile", "/v1/messages", logs[failed+1].RequestHeaders, body(editedInput), logs[failed+1].Timestamp)
	complete(edited, logs[failed+1].Timestamp.Add(time.Second), "end_turn", textBlock("edited answer"))
	if interrupted.turn.session != main.turn.session || edited.turn.session.parent != main.turn.session.id || len(s.Snapshot(time.Now()).Sessions) != 2 {
		t.Fatal("captured header rotation or shared prefix split the wrong lane")
	}
	if bytes.Equal(interruptedInput[len(interruptedInput)-1], editedInput[len(editedInput)-1]) {
		t.Fatal("constructed edited tails must differ")
	}
	t.Logf("verified %d complete captured prefix messages and rotating headers with explicitly constructed interrupted/edited tails; original truncated tails were not replayable", len(base))
}
