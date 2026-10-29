package localproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	gvmcp "github.com/doganarif/govisual/mcp"
	"github.com/doganarif/govisual/v2"
	"github.com/doganarif/govisual/v2/store"
)

const (
	inspectionPath    = "/__viz"
	inspectionMCPPath = inspectionPath + "/mcp"
)

var gatewayLogger = slog.New(govisual.SlogHandler(slog.NewTextHandler(io.Discard, nil)))

func logModelRoute(ctx context.Context, requested string, route modelRoute) {
	gatewayLogger.InfoContext(ctx, "LLM Gateway model routing",
		"requested_model", requested, "routed_model", route.model.Model.ID,
		"provider_id", route.model.ProviderID, "reason", route.reason, "available", route.ctx != nil)
}

func logUpstreamFailure(ctx context.Context, providerID, endpoint, kind string, err error) {
	// Keep the transport cause without logging a request URL's credentials or query.
	var requestError *url.Error
	for errors.As(err, &requestError) {
		err = requestError.Err
	}
	gatewayLogger.ErrorContext(ctx, "LLM Gateway upstream request failed",
		"provider_id", providerID, "endpoint", endpoint, "kind", kind, "error", err.Error())
}

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
	st := store.WithNotify(store.NewMemory(100))
	handler := govisual.Wrap(http.HandlerFunc(s.serveAPI),
		govisual.WithStore(st),
		govisual.WithDashboardPath(inspectionPath),
		govisual.WithProfiling(true),
		// Collect outbound HTTP headers, bodies and timings without CPU, heap or goroutine sampling.
		govisual.WithProfileType(0),
		govisual.WithProfileThreshold(0),
		govisual.WithMaxProfileMetrics(100),
		govisual.WithMaxBodyBytes(1<<20),
		govisual.WithRequestBodyLogging(true),
		govisual.WithResponseBodyLogging(true),
	)
	// Dashboard replay and environment inspection remain disabled.
	// Keep MCP traffic outside capture; replays target the actual Gateway listener.
	routes := http.NewServeMux()
	routes.Handle(inspectionMCPPath, withMCPErrorLog(gvmcp.Handler(st, gvmcp.WithBaseURL("http://"+s.address)), s.mcpErrorLog))
	routes.Handle("/", handler)
	s.inspection.Store(&requestInspection{handler: routes, ctx: ctx, cancel: cancel})
}

func (s *Server) InspectionURL() string { return s.inspectionURL(inspectionPath + "/") }

func (s *Server) InspectionMCPURL() string { return s.inspectionURL(inspectionMCPPath) }

func (s *Server) inspectionURL(path string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.inspection.Load() == nil || s.address == "" {
		return ""
	}
	return "http://" + s.address + path
}
