package desktop

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/agentviz"
	"github.com/fondoger/copilotchat/internal/localproxy"
)

func TestVisualizerBridgeRejectsUnownedCallers(t *testing.T) {
	s := &gatewayVisualizerAPI{}
	server := localproxy.New("test-key")
	defer server.Close()
	server.SetVisualizer(true)
	s.attach(nil, server)
	if _, err := s.Poll(context.Background(), ""); err == nil {
		t.Fatal("unowned caller read snapshot")
	}
	if _, err := s.Detail(context.Background(), "t1"); err == nil {
		t.Fatal("unowned caller read detail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Poll(ctx, ""); err != context.Canceled {
		t.Fatalf("canceled caller: %v", err)
	}
}

func TestVisualizerNavigationStaysOnEmbeddedDocument(t *testing.T) {
	for _, target := range []string{"mygo://localhost/web/index.html", "http://mygo.localhost/web/index.html", "mygo://localhost/web/index.html#activity"} {
		if !visualizerNavigation(target) {
			t.Fatalf("blocked owned document %s", target)
		}
	}
	for _, target := range []string{"https://example.com/web/index.html", "http://127.0.0.1:39322/__agentviz/", "mygo://localhost/web/other.html", "file:///web/index.html", "javascript:alert(1)", "http://mygo.localhost.evil/web/index.html", "mygo://localhost/web/index.html?redirect=1"} {
		if visualizerNavigation(target) {
			t.Fatalf("accepted navigation outside owned document %s", target)
		}
	}
}

func TestVisualizerEmbeddedDocumentRedirect(t *testing.T) {
	files := mygo.FileServer(agentviz.Frontend())
	r := httptest.NewRequest("GET", "http://mygo.localhost"+visualizerDocument, nil)
	w := httptest.NewRecorder()
	files.ServeHTTP(w, r)
	if w.Code != 301 {
		t.Fatalf("embedded index redirect: %d", w.Code)
	}
	ref, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	canonical := r.URL.ResolveReference(ref)
	if !visualizerNavigation(canonical.String()) {
		t.Fatal("navigation guard blocked embedded index redirect", canonical)
	}
	w = httptest.NewRecorder()
	files.ServeHTTP(w, httptest.NewRequest("GET", canonical.String(), nil))
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatal("embedded page unavailable after redirect")
	}
}

func TestVisualizerBridgeDropsStoppedForwarder(t *testing.T) {
	a := newSettingsTestApp(t)
	a.visualizerAPI = &gatewayVisualizerAPI{}
	startVisualizerTestForwarder(t, a)
	if a.visualizerAPI.server != a.forwarder {
		t.Fatal("window bridge did not receive running server")
	}
	a.stopForwarding()
	if a.visualizerAPI.server != nil {
		t.Fatal("window bridge retained stopped server")
	}
}
