package localproxy

import (
	"context"
	"net/http"
	"strings"

	"github.com/doganarif/govisual/v2"
)

const inspectionPath = "/__viz"

type requestInspection struct {
	handler http.Handler
	ctx     context.Context
	cancel  context.CancelFunc
}

func isInspectionPath(path string) bool {
	return path == inspectionPath || strings.HasPrefix(path, inspectionPath+"/")
}

// SetInspection changes capture for new requests without restarting the HTTP
// listener. Disabled mode bypasses GoVisual entirely. Each enable starts a fresh
// bounded in-memory session; no bodies or upstream credentials are written to disk.
func (s *Server) SetInspection(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (s.inspection.Load() != nil) == enabled {
		return
	}
	if !enabled {
		if old := s.inspection.Swap(nil); old != nil {
			old.cancel()
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	handler := govisual.Wrap(http.HandlerFunc(s.serveAPI),
		govisual.WithDashboardPath(inspectionPath),
		govisual.WithMaxRequests(100),
		govisual.WithProfiling(true),
		// Collect outbound HTTP timings without CPU, heap or goroutine sampling.
		govisual.WithProfileType(0),
		govisual.WithProfileThreshold(0),
		govisual.WithMaxProfileMetrics(100),
		govisual.WithMaxBodyBytes(1<<20),
		govisual.WithRequestBodyLogging(true),
		govisual.WithResponseBodyLogging(true),
	)
	// Default GoVisual guards keep the dashboard local, redact credential
	// headers and disable request replay and environment inspection.
	s.inspection.Store(&requestInspection{handler: handler, ctx: ctx, cancel: cancel})
}

func (s *Server) InspectionURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.inspection.Load() == nil || s.address == "" {
		return ""
	}
	return "http://" + s.address + inspectionPath + "/"
}
