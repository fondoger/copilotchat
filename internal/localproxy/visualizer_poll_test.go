package localproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVisualizerPollVersionsAndNotModified(t *testing.T) {
	s := New("inspection-test-key")
	defer s.Close()
	if got := s.VisualizerPoll(""); got.Status != "disabled" || got.Snapshot != nil {
		t.Fatalf("disabled=%+v", got)
	}
	s.SetVisualizer(true)
	first := s.VisualizerPoll("")
	if first.Status != "enabled" || first.Snapshot == nil || first.Version == "" {
		t.Fatalf("first=%+v", first)
	}
	if same := s.VisualizerPoll(first.Version); same.Snapshot != nil || same.Version != first.Version {
		t.Fatal("unchanged native poll returned data")
	}
	r := inspectionRequest("GET", visualizerPath+"/api/snapshot", "")
	r.Header.Set("If-None-Match", `"`+first.Version+`"`)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("ETag") != `"`+first.Version+`"` {
		t.Fatalf("conditional HTTP request: %d %q", w.Code, w.Body.String())
	}
	capture := s.visualizer.Load().store.Begin("profile", "/v1/messages", http.Header{}, []byte(`{"model":"m","messages":[{"role":"user","content":"first task"}]}`), time.Now())
	next := s.VisualizerPoll(first.Version)
	if next.Snapshot == nil || next.Version == first.Version || next.Snapshot.Sessions[0].Turns[0].InstructionPreview != "first task" {
		t.Fatal("new turn or instruction was not published")
	}
	capture.Finish(time.Now(), nil)
	s.SetVisualizer(false)
	s.SetVisualizer(true)
	reset := s.VisualizerPoll(first.Version)
	if reset.Snapshot == nil || reset.Version == first.Version || len(reset.Snapshot.Sessions) != 0 {
		t.Fatal("capture reset reused an old version")
	}
}
