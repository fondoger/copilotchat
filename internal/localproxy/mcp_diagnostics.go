package localproxy

import (
	"log"
	"net/http"
	"strings"
)

const mcpErrorBodyLimit = 1024

func withMCPErrorLog(next http.Handler, logger *log.Logger) http.Handler {
	if logger == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := &mcpErrorWriter{ResponseWriter: w}
		next.ServeHTTP(response, r)
		if response.status < http.StatusBadRequest {
			return
		}
		logger.Printf("gateway-mcp method=%.32q path=%.256q status=%d accept=%.256q content_type=%.256q protocol_version=%.64q has_session=%t user_agent=%.256q error=%q",
			r.Method, r.URL.Path, response.status, r.Header.Get("Accept"), r.Header.Get("Content-Type"),
			r.Header.Get("MCP-Protocol-Version"), r.Header.Get("Mcp-Session-Id") != "", r.UserAgent(), strings.TrimSpace(string(response.body)))
	})
}

type mcpErrorWriter struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (w *mcpErrorWriter) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *mcpErrorWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	// Protocol rejections use plain text; never retain JSON tool results or SSE.
	if w.status >= http.StatusBadRequest && strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		w.body = append(w.body, data[:min(n, mcpErrorBodyLimit-len(w.body))]...)
	}
	return n, err
}

func (w *mcpErrorWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *mcpErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
