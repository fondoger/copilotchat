package localproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fondoger/copilotchat/internal/agentviz"
)

const visualizerPath = "/__agentviz"

var visualizerGeneration atomic.Uint64

type VisualizerUpdate struct {
	Status   string             `json:"status"`
	Version  string             `json:"version"`
	Snapshot *agentviz.Snapshot `json:"snapshot"`
}

type requestVisualizer struct {
	generation uint64
	store      *agentviz.Store
}

// SetVisualizer starts a fresh, memory-only observation session when enabled.
// Turning it off releases captures without canceling any inference request.
func (s *Server) SetVisualizer(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (s.visualizer.Load() != nil) == enabled {
		return
	}
	if !enabled {
		if old := s.visualizer.Swap(nil); old != nil {
			old.store.Close()
		}
		return
	}
	s.visualizer.Store(&requestVisualizer{generation: visualizerGeneration.Add(1), store: agentviz.New()})
}

// VisualizerPoll is shared by the owned native page and the loopback HTTP view.
// A generation-qualified version prevents old data surviving disable/re-enable
// or a Gateway restart whose request counter happens to have the same value.
func (s *Server) VisualizerPoll(version string) VisualizerUpdate {
	if s == nil {
		return VisualizerUpdate{Status: "disabled"}
	}
	viz := s.visualizer.Load()
	if viz == nil {
		return VisualizerUpdate{Status: "disabled"}
	}
	current := fmt.Sprintf("%d-%d", viz.generation, viz.store.Revision())
	if current == version {
		return VisualizerUpdate{Status: "enabled", Version: current}
	}
	snapshot := viz.store.Snapshot(time.Now())
	current = fmt.Sprintf("%d-%d", viz.generation, snapshot.Revision)
	return VisualizerUpdate{Status: "enabled", Version: current, Snapshot: &snapshot}
}

func (s *Server) VisualizerDetail(id string) (agentviz.TurnDetail, error) {
	if s != nil {
		if viz := s.visualizer.Load(); viz != nil {
			if detail, ok := viz.store.Detail(id, time.Now()); ok {
				return detail, nil
			}
			return agentviz.TurnDetail{}, errors.New("This turn is no longer retained.")
		}
	}
	return agentviz.TurnDetail{}, errors.New("LLM Gateway Visualizer is disabled.")
}

func (s *Server) VisualizerURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.visualizer.Load() == nil || s.address == "" {
		return ""
	}
	return "http://" + s.address + visualizerPath + "/"
}
func isVisualizerPath(path string) bool {
	return path == visualizerPath || strings.HasPrefix(path, visualizerPath+"/")
}

func (s *Server) serveVisualizer(w http.ResponseWriter, r *http.Request) {
	// Host checking in ServeHTTP blocks DNS rebinding. Also reject cross-origin
	// navigations/fetches and non-loopback callers; the local key never enters HTML.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	origin := r.Header.Get("Origin")
	if err != nil || ip == nil || !ip.IsLoopback() || (origin != "" && origin != "http://"+r.Host) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		apiError(w, http.StatusForbidden, "The visualizer is available to this local browser only.")
		return
	}
	viz := s.visualizer.Load()
	if viz == nil {
		apiError(w, http.StatusNotFound, "LLM Gateway Visualizer is disabled.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		apiError(w, http.StatusMethodNotAllowed, "Use GET for the visualizer.")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, visualizerPath)
	switch {
	case path == "":
		http.Redirect(w, r, visualizerPath+"/", http.StatusTemporaryRedirect)
	case path == "/api/snapshot":
		update := s.VisualizerPoll(strings.Trim(r.Header.Get("If-None-Match"), `"`))
		if update.Status == "disabled" {
			apiError(w, http.StatusNotFound, "LLM Gateway Visualizer is disabled.")
			return
		}
		w.Header().Set("ETag", `"`+update.Version+`"`)
		if update.Snapshot == nil {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(update.Snapshot)
	case strings.HasPrefix(path, "/api/turns/"):
		detail, ok := viz.store.Detail(strings.TrimPrefix(path, "/api/turns/"), time.Now())
		if !ok {
			apiError(w, http.StatusNotFound, "This turn is no longer retained.")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(detail)
	default:
		agentviz.ServeAsset(w, r, path)
	}
}
