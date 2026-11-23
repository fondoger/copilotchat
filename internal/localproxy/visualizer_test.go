package localproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/agentviz"
	"github.com/fondoger/copilotchat/internal/provider"
)

func visualizerSnapshot(t *testing.T, s *Server) agentviz.Snapshot {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, inspectionRequest("GET", visualizerPath+"/api/snapshot", ""))
	var snapshot agentviz.Snapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body.String())
	}
	return snapshot
}

func TestVisualizerLifecycleAccessAndAssets(t *testing.T) {
	s := New("inspection-test-key")
	t.Cleanup(s.Close)
	request := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, inspectionRequest("GET", path, ""))
		return w
	}
	if request(visualizerPath+"/").Code != 404 {
		t.Fatal("visualizer must be opt-in")
	}
	s.SetVisualizer(true)
	active := s.visualizer.Load()
	s.SetVisualizer(true)
	if s.visualizer.Load() != active {
		t.Fatal("saving reset visualizer")
	}
	for _, path := range []string{"/", "/visualizer.css", "/visualizer.js", "/api/snapshot"} {
		w := request(visualizerPath + path)
		if w.Code != 200 || w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
	}
	if request(visualizerPath+"/../server.go").Code != 404 || request(visualizerPath+"/api/turns/missing").Code != 404 {
		t.Fatal("unexpected resource access")
	}
	for _, test := range []struct{ host, remote, origin, site string }{{"localhost:39321", "127.0.0.1:1234", "", ""}, {"127.0.0.1:39321", "192.0.2.1:1234", "", ""}, {"127.0.0.1:39321", "127.0.0.1:1234", "https://example.com", ""}, {"127.0.0.1:39321", "127.0.0.1:1234", "", "cross-site"}} {
		r := inspectionRequest("GET", visualizerPath+"/api/snapshot", "")
		r.Host = test.host
		r.RemoteAddr = test.remote
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Sec-Fetch-Site", test.site)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("accepted foreign browser request: %+v", test)
		}
	}
	s.SetVisualizer(false)
	if request(visualizerPath+"/").Code != 404 {
		t.Fatal("disable did not release visualizer")
	}
	s.SetVisualizer(true)
	if len(visualizerSnapshot(t, s).Sessions) != 0 {
		t.Fatal("capture survived disable")
	}
	s.Close()
	s.SetVisualizer(true)
	if s.VisualizerURL() != "" || s.visualizer.Load() != nil {
		t.Fatal("closed visualizer restarted")
	}
}

func TestVisualizerObservesClientFacingConversion(t *testing.T) {
	upstream := `{"id":"resp1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":80}}}`
	s := New("inspection-test-key")
	t.Cleanup(s.Close)
	client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
		if r.Endpoint == "/responses/input_tokens" {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1}`))}, nil
		}
		if r.Endpoint != "/responses" {
			t.Errorf("wrong upstream protocol: %s", r.Endpoint)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(upstream))}, nil
	}}
	s.Update(map[string]Target{"p": {Client: client}}, map[string]Profile{"test": {ID: "p", Models: []Model{{ProviderID: "p", ID: "alias", Model: provider.Model{ID: "actual", Name: "Actual model", Endpoint: "/responses"}}}}})
	s.SetVisualizer(true)
	s.SetInspection(true)
	w := httptest.NewRecorder()
	r := inspectionRequest("POST", "/test/v1/messages", `{"model":"alias","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`)
	r.Header.Set("X-Claude-Code-Session-Id", "one")
	s.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"stop_reason":"end_turn"`) {
		t.Fatalf("translated response: %s", w.Body.String())
	}
	snapshot := visualizerSnapshot(t, s)
	if len(snapshot.Sessions) != 1 || len(snapshot.Sessions[0].Turns) != 1 {
		t.Fatalf("capture=%+v", snapshot)
	}
	turn := snapshot.Sessions[0].Turns[0]
	if turn.Status != "completed" || turn.ModelID != "actual" || turn.ModelName != "Actual model" || turn.InputTokens != 100 || turn.CachedTokens != 80 || turn.TotalTokens != 107 {
		t.Fatalf("turn=%+v", turn)
	}
	detail := httptest.NewRecorder()
	s.ServeHTTP(detail, inspectionRequest("GET", visualizerPath+"/api/turns/"+turn.ID, ""))
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), "Hello") {
		t.Fatal("missing captured content")
	}
	// count_tokens is traffic for token estimation, not an agent turn.
	count := httptest.NewRecorder()
	s.ServeHTTP(count, inspectionRequest("POST", "/test/v1/messages/count_tokens", `{"model":"alias","messages":[{"role":"user","content":"Count"}]}`))
	if len(visualizerSnapshot(t, s).Sessions) != 1 {
		t.Fatal("token estimation created a session")
	}
}

func TestVisualizerDisableDoesNotInterruptStream(t *testing.T) {
	s := New("inspection-test-key")
	t.Cleanup(s.Close)
	reader, writer := io.Pipe()
	t.Cleanup(func() { reader.Close(); writer.Close() })
	client := &rawClient{fn: func(context.Context, provider.RawRequest) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	}}
	s.Update(map[string]Target{"p": {Client: client}}, map[string]Profile{"test": {ID: "p", Models: []Model{{ProviderID: "p", ID: "claude", Model: provider.Model{ID: "claude", Endpoint: "/v1/messages"}}}}})
	s.SetVisualizer(true)
	w := &inspectionStreamWriter{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan string, 16)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ServeHTTP(w, inspectionRequest("POST", "/test/v1/messages", `{"model":"claude","stream":true,"messages":[{"role":"user","content":"Stream"}]}`))
	}()
	first := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n"
	_, _ = io.WriteString(writer, first)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case data := <-w.flushed:
			if strings.Contains(data, "message_start") {
				goto streamed
			}
		case <-deadline:
			t.Fatal("response was buffered")
		}
	}
streamed:
	s.SetVisualizer(false)
	last := "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	_, _ = io.WriteString(writer, last)
	writer.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("disable interrupted inference")
	}
	if w.Body.String() != first+last {
		t.Fatal("observer changed client stream")
	}
}
