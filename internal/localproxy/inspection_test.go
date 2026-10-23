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

	visualstore "github.com/doganarif/govisual/v2/store"
	"github.com/fondoger/copilotchat/internal/provider"
)

func inspectionRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host, r.RemoteAddr = "127.0.0.1:39321", "127.0.0.1:12345"
	r.Header.Set("X-Api-Key", "inspection-test-key")
	return r
}

func inspectionLogs(t *testing.T, s *Server) []visualstore.RequestLog {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, inspectionRequest("GET", "/__viz/api/requests", ""))
	var logs []visualstore.RequestLog
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &logs) != nil {
		t.Fatal("cannot read GoVisual capture", w.Code, w.Body.String())
	}
	return logs
}

func TestInspectionLifecycleAndGuards(t *testing.T) {
	s := New("inspection-test-key")
	t.Cleanup(s.Close)
	request := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, inspectionRequest("GET", path, ""))
		return w
	}
	if s.inspection.Load() != nil || request("/__viz/").Code != 404 {
		t.Fatal("inspection is not opt-in")
	}
	s.SetInspection(true)
	active := s.inspection.Load()
	s.SetInspection(true)
	if s.inspection.Load() != active {
		t.Fatal("saving settings reset captured traffic")
	}
	// Also capture invalid profile/auth requests, useful when configuring agents.
	request("/missing/v1/models")
	logs := inspectionLogs(t, s)
	if len(logs) != 1 || logs[0].Path != "/missing/v1/models" || logs[0].StatusCode != 404 || logs[0].ResponseBody == "" {
		t.Fatal("request/response not captured", logs)
	}
	if logs[0].RequestHeaders.Get("X-Api-Key") == "inspection-test-key" {
		t.Fatal("API key leaked into capture")
	}
	for _, test := range []struct {
		path, origin, remote string
		code                 int
	}{
		{"/__viz/", "http://127.0.0.1:39321", "127.0.0.1:12345", 200},
		{"/__viz/api/requests", "https://example.com", "127.0.0.1:12345", 403},
		{"/__viz/api/requests", "", "192.0.2.1:12345", 403},
		{"/missing/v1/models", "http://127.0.0.1:39321", "127.0.0.1:12345", 403},
	} {
		r := inspectionRequest("GET", test.path, "")
		r.RemoteAddr = test.remote
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != test.code {
			t.Fatal(test, w.Code)
		}
	}
	s.SetInspection(false)
	if request("/__viz/api/requests").Code != 404 || active.ctx.Err() == nil {
		t.Fatal("disabling left the dashboard active")
	}
	s.SetInspection(true)
	if len(inspectionLogs(t, s)) != 0 {
		t.Fatal("old captures survived a new inspection session")
	}
	s.Close()
	s.SetInspection(true)
	if s.inspection.Load() != nil || s.InspectionURL() != "" {
		t.Fatal("closed server restarted inspection")
	}
}

type inspectionStreamWriter struct {
	*httptest.ResponseRecorder
	flushed chan string
}

func (w *inspectionStreamWriter) Flush() {
	w.ResponseRecorder.Flush()
	select {
	case w.flushed <- w.Body.String():
	default:
	}
}

func TestInspectionPreservesStreamingWhileToggled(t *testing.T) {
	s := New("inspection-test-key")
	t.Cleanup(s.Close)
	reader, writer := io.Pipe()
	t.Cleanup(func() { reader.Close(); writer.Close() })
	client := &rawClient{fn: func(_ context.Context, _ provider.RawRequest) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	}}
	s.Update(map[string]Target{"p": {Client: client}}, map[string]Profile{"test": {ID: "test", Models: []Model{{ProviderID: "p", ID: "claude-test", Model: provider.Model{ID: "claude-test", Endpoint: "/v1/messages"}}}}})
	s.SetInspection(true)
	w := &inspectionStreamWriter{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan string, 16)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ServeHTTP(w, inspectionRequest("POST", "/test/v1/messages", `{"model":"claude-test","stream":true,"messages":[]}`))
	}()
	first := "event: content_block_delta\ndata: {\"text\":\"first\"}\n\n"
	if _, err := io.WriteString(writer, first); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case data := <-w.flushed:
			if strings.Contains(data, "first") {
				goto streamed
			}
		case <-deadline:
			t.Fatal("GoVisual buffered the stream instead of flushing")
		}
	}
streamed:
	s.SetInspection(false)
	last := "event: message_stop\ndata: {}\n\n"
	if _, err := io.WriteString(writer, last); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("disabling inspection interrupted inference")
	}
	if w.Code != 200 || w.Body.String() != first+last {
		t.Fatal("inspection changed the streamed response", w.Code, w.Body.String())
	}
}

func TestInspectionCaptureLimitDoesNotTruncateTraffic(t *testing.T) {
	s := New("inspection-test-key")
	t.Cleanup(s.Close)
	payload := strings.Repeat("x", (1<<20)+100)
	body := `{"model":"claude-test","messages":[{"role":"user","content":"` + payload + `"}]}`
	var forwarded string
	client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
		forwarded = string(r.Body)
		return response(payload), nil
	}}
	s.Update(map[string]Target{"p": {Client: client}}, map[string]Profile{"test": {ID: "test", Models: []Model{{ProviderID: "p", ID: "claude-test", Model: provider.Model{ID: "claude-test", Endpoint: "/v1/messages"}}}}})
	s.SetInspection(true)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, inspectionRequest("POST", "/test/v1/messages", body))
	if w.Code != 200 || forwarded != body || w.Body.String() != payload {
		t.Fatal("capture limit truncated actual HTTP traffic")
	}
	logs := inspectionLogs(t, s)
	if len(logs) != 1 || !strings.HasSuffix(logs[0].RequestBody, "[truncated by govisual]") || !strings.HasSuffix(logs[0].ResponseBody, "[truncated by govisual]") {
		t.Fatal("large captures are not bounded")
	}
}

func TestInspectionRecordsOutboundHTTP(t *testing.T) {
	const limit = 1 << 20
	for _, test := range []struct {
		name, content, response string
	}{
		{"complete", "foo", `{"error":{"message":"not found"}}`},
		{"empty response", "foo", ""},
		{"truncated", strings.Repeat("x", limit+100), strings.Repeat("y", limit+100)},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"model":"claude-test","messages":[{"role":"user","content":"` + test.content + `"}]}`
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(r.Body)
				if err != nil || string(data) != body {
					t.Error("capture changed the upstream request body")
				}
				if r.Header.Get("Authorization") != "Bearer upstream-test-secret" {
					t.Error("capture changed the upstream credential")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Add("X-Test-Response", "first")
				w.Header().Add("X-Test-Response", "second")
				w.Header().Set("Set-Cookie", "session=upstream-test-cookie")
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, test.response)
			}))
			defer upstream.Close()
			httpClient := provider.NewHTTPClient()
			defer httpClient.CloseIdleConnections()
			client := &rawClient{fn: func(ctx context.Context, raw provider.RawRequest) (*http.Response, error) {
				r, err := http.NewRequestWithContext(ctx, "POST", upstream.URL+raw.Endpoint, strings.NewReader(string(raw.Body)))
				if err != nil {
					return nil, err
				}
				r.Header.Set("Authorization", "Bearer upstream-test-secret")
				r.Header.Set("Content-Type", "application/json")
				r.Header.Add("X-Test-Request", "first")
				r.Header.Add("X-Test-Request", "second")
				return httpClient.Do(r)
			}}
			s := New("inspection-test-key")
			t.Cleanup(s.Close)
			s.Update(map[string]Target{"p": {Client: client}}, map[string]Profile{"test": {ID: "test", Models: []Model{{ProviderID: "p", ID: "claude-test", Model: provider.Model{ID: "claude-test", Endpoint: "/v1/messages"}}}}})
			s.SetInspection(true)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, inspectionRequest("POST", "/test/v1/messages", body))
			logs := inspectionLogs(t, s)
			if w.Code != 404 || w.Body.String() != test.response || len(logs) != 1 || logs[0].PerformanceMetrics == nil {
				t.Fatal("missing upstream observation or changed response", w.Code)
			}
			calls := logs[0].PerformanceMetrics.HTTPCalls
			if len(calls) != 1 || calls[0].Method != "POST" || calls[0].URL != upstream.URL+"/v1/messages" || calls[0].Status != 404 || calls[0].Duration <= 0 {
				t.Fatal("incorrect outbound capture")
			}
			call := calls[0]
			if call.RequestHeaders.Get("Content-Type") != "application/json" || call.ResponseHeaders.Get("Content-Type") != "application/json" ||
				strings.Join(call.RequestHeaders.Values("X-Test-Request"), ",") != "first,second" || strings.Join(call.ResponseHeaders.Values("X-Test-Response"), ",") != "first,second" {
				t.Fatal("outbound headers not captured")
			}
			if call.RequestHeaders.Get("Authorization") == "" || call.ResponseHeaders.Get("Set-Cookie") == "" {
				t.Fatal("credential headers should be redacted, not omitted")
			}
			if call.RequestBody == nil || *call.RequestBody != body[:min(len(body), limit)] ||
				call.ResponseBody == nil || *call.ResponseBody != test.response[:min(len(test.response), limit)] {
				t.Fatal("outbound bodies not captured within the limit")
			}
			if call.RequestBodyTruncated != (len(body) > limit) || call.ResponseBodyTruncated != (len(test.response) > limit) {
				t.Fatal("incorrect outbound body truncation flags")
			}
			data, _ := json.Marshal(logs)
			if strings.Contains(string(data), "upstream-test-secret") || strings.Contains(string(data), "upstream-test-cookie") {
				t.Fatal("upstream credential captured")
			}
			s.SetInspection(false)
			w = httptest.NewRecorder()
			s.ServeHTTP(w, inspectionRequest("POST", "/test/v1/messages", body))
			if w.Code != 404 || w.Body.String() != test.response {
				t.Fatal("disabling inspection changed upstream forwarding", w.Code)
			}
			s.SetInspection(true)
			if len(inspectionLogs(t, s)) != 0 {
				t.Fatal("disabled requests were captured")
			}
		})
	}
}
