package agentviz

import (
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

type localCapture struct {
	Timestamp                       time.Time
	Duration                        int64
	StatusCode                      int
	RequestHeaders, ResponseHeaders http.Header
	RequestBody, ResponseBody       string
}

// Captures remain outside the repository and never leave the process. Skip
// incomplete exported bodies explicitly; normal CI uses synthetic regressions.
func suggestionCaptures(t *testing.T) []localCapture {
	t.Helper()
	paths := filepath.SplitList(os.Getenv("AGENTVIZ_CAPTURE_FILES"))
	if len(paths) == 0 {
		t.Skip("set AGENTVIZ_CAPTURE_FILES to local GoVisual JSON exports")
	}
	var logs []localCapture
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var log localCapture
		if err = json.Unmarshal(data, &log); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(log.RequestBody, "[truncated by govisual]") {
			continue
		}
		logs = append(logs, log)
	}
	slices.SortFunc(logs, func(a, b localCapture) int { return a.Timestamp.Compare(b.Timestamp) })
	return logs
}

func replayLocalCapture(t *testing.T, s *Store, log localCapture) *Capture {
	t.Helper()
	c := s.Begin("capture-profile", "/v1/messages", log.RequestHeaders, []byte(log.RequestBody), log.Timestamp)
	if c == nil {
		t.Fatal("capture could not be observed")
	}
	w := c.Wrap(httptest.NewRecorder())
	w.Header().Set("Content-Type", log.ResponseHeaders.Get("Content-Type"))
	w.WriteHeader(log.StatusCode)
	_, _ = w.Write([]byte(log.ResponseBody))
	c.Finish(log.Timestamp.Add(time.Duration(log.Duration)*time.Millisecond), nil)
	return c
}

func TestLocalSuggestionCaptures(t *testing.T) {
	var mainLog, helperLog *localCapture
	for _, log := range suggestionCaptures(t) {
		p, err := parseRequest([]byte(log.RequestBody), log.RequestHeaders, "/v1/messages")
		if err != nil {
			t.Fatal(err)
		}
		if p.purpose == "suggestion" {
			helperLog = &log
		} else {
			mainLog = &log
		}
	}
	if mainLog == nil || helperLog == nil {
		t.Fatal("provide one main request and its following suggestion request")
	}
	for _, helperFirst := range []bool{false, true} {
		name := "main-first"
		if helperFirst {
			name = "helper-first"
		}
		t.Run(name, func(t *testing.T) {
			s := New()
			var main, helper *Capture
			if helperFirst {
				helper = replayLocalCapture(t, s, *helperLog)
				main = replayLocalCapture(t, s, *mainLog)
			} else {
				main = replayLocalCapture(t, s, *mainLog)
				helper = replayLocalCapture(t, s, *helperLog)
			}
			if main.turn.Status != "completed" || helper.turn.Status != "completed" {
				t.Fatal("captured response was not completed")
			}
			if main.turn.session.parent != "" || helper.turn.session.parent != main.turn.session.id || helper.turn.session.forkTurn != main.turn.ID {
				t.Fatal("captured main/helper ancestry was reversed or unanchored")
			}
			var envelope map[string]json.RawMessage
			_ = json.Unmarshal([]byte(helperLog.RequestBody), &envelope)
			var messages []json.RawMessage
			_ = json.Unmarshal(envelope["messages"], &messages)
			messages = append(messages[:len(messages)-1], raw(user("capture continuation check")))
			envelope["messages"], _ = json.Marshal(messages)
			body, _ := json.Marshal(envelope)
			at := helperLog.Timestamp.Add(time.Duration(helperLog.Duration)*time.Millisecond + time.Second)
			next := s.Begin("capture-profile", "/v1/messages", mainLog.RequestHeaders, body, at)
			if next == nil {
				t.Fatal("next main turn was not observed")
			}
			complete(next, at.Add(time.Second), "end_turn", textBlock("Checked"))
			if next.turn.session != main.turn.session || len(main.turn.session.turns) != 2 || len(helper.turn.session.turns) != 1 || len(s.Snapshot(at.Add(2*time.Second)).Sessions) != 2 {
				t.Fatal("suggestion diverted subsequent main input")
			}
			t.Log("captured requests and following main input preserved primary/helper ownership")
		})
	}
}

func TestLocalEarlierSuggestionTimeline(t *testing.T) {
	logs := suggestionCaptures(t)
	if len(logs) < 3 {
		t.Skip("requires an earlier suggestion, main request, and following suggestion")
	}
	s := New()
	var main *Capture
	var helpers []*Capture
	for _, log := range logs {
		c := replayLocalCapture(t, s, log)
		if c.turn.session.purpose == "suggestion" {
			helpers = append(helpers, c)
		} else {
			main = c
		}
	}
	if main == nil || len(helpers) < 2 || main.turn.session.parent != "" {
		t.Fatal("earlier suggestion became primary")
	}
	for _, helper := range helpers {
		if helper.turn.session.parent != main.turn.session.id {
			t.Fatal("suggestion did not retain the reserved main identity")
		}
	}
	if helpers[len(helpers)-1].turn.session.forkTurn != main.turn.ID {
		t.Fatal("following suggestion lost its captured shared turn")
	}
	if len(s.Snapshot(time.Now()).Sessions) != len(helpers)+1 {
		t.Fatal("timeline invented uncaptured main turns or additional lanes")
	}
	t.Log("earlier and later captured suggestions both remained forks of the real main lane")
}
