package localproxy

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPDiagnosticsLogsProtocolRejectionWithoutSecrets(t *testing.T) {
	var logs bytes.Buffer
	s := New("inspection-test-key", WithMCPErrorLog(log.New(&logs, "", 0)))
	t.Cleanup(s.Close)
	s.SetInspection(true)
	r := inspectionRequest("GET", inspectionMCPPath+"?token=private-query", "private-request-body")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("MCP-Protocol-Version", "2025-06-18")
	r.Header.Set("User-Agent", "diagnostic-test")
	r.Header.Set("Authorization", "Bearer private-bearer")
	r.Header.Set("Cookie", "session=private-cookie")
	r.Header.Set("Mcp-Session-Id", "private-session")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, want := range []string{
		`gateway-mcp`, `method="GET"`, `path="/__viz/mcp"`, `status=400`,
		`accept="application/json"`, `content_type="application/json"`,
		`protocol_version="2025-06-18"`, `has_session=true`, `user_agent="diagnostic-test"`,
		strings.TrimSpace(w.Body.String()),
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing diagnostic %q: %s", want, logs.String())
		}
	}
	for _, secret := range []string{"inspection-test-key", "private-query", "private-request-body", "private-bearer", "private-cookie", "private-session"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("secret %q leaked into diagnostics", secret)
		}
	}
	if len(inspectionLogs(t, s)) != 0 {
		t.Fatal("MCP diagnostics entered GoVisual captures")
	}
}

func TestMCPDiagnosticsPreservesInitializeStreaming(t *testing.T) {
	var logs bytes.Buffer
	s := New("inspection-test-key", WithMCPErrorLog(log.New(&logs, "", 0)))
	t.Cleanup(s.Close)
	s.SetInspection(true)
	r := inspectionRequest("POST", inspectionMCPPath, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"diagnostic-test","version":"1"}}}`)
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !w.Flushed || !strings.Contains(w.Body.String(), `"serverInfo"`) {
		t.Fatal("MCP initialize did not stream", w.Code, w.Flushed, w.Body.String())
	}
	session := w.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("MCP session was not established")
	}
	r = inspectionRequest("DELETE", inspectionMCPPath, "")
	r.Header.Set("Mcp-Session-Id", session)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatal("MCP session cleanup failed", w.Code, w.Body.String())
	}
	if logs.Len() != 0 || len(inspectionLogs(t, s)) != 0 {
		t.Fatal("successful MCP traffic was captured", logs.String())
	}
}

func TestMCPDiagnosticsBoundsAndFiltersResponseBodies(t *testing.T) {
	for _, contentType := range []string{"text/plain; charset=utf-8", "application/json", "text/event-stream"} {
		t.Run(contentType, func(t *testing.T) {
			var logs bytes.Buffer
			body := strings.Repeat("x", mcpErrorBodyLimit) + "private-tail"
			handler := withMCPErrorLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(body[:512]))
				_, _ = w.Write([]byte(body[512:]))
			}), log.New(&logs, "", 0))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("POST", inspectionMCPPath, nil))
			if w.Code != http.StatusBadRequest || w.Body.String() != body {
				t.Fatal("diagnostics changed the HTTP response")
			}
			if strings.Contains(logs.String(), "private-tail") {
				t.Fatal("oversized response retained in diagnostics")
			}
			if strings.HasPrefix(contentType, "text/plain") {
				if !strings.Contains(logs.String(), `error="`+body[:mcpErrorBodyLimit]+`"`) {
					t.Fatal("bounded protocol error missing", logs.String())
				}
			} else if !strings.Contains(logs.String(), `error=""`) {
				t.Fatal("non-plain-text response body was logged", logs.String())
			}
		})
	}
}
