package localproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestInspectionRecordsUpstreamFailures(t *testing.T) {
	const requestURL = "https://upstream-test-user:password-test-secret@api.example.com/responses?key=query-test-secret"
	for _, test := range []struct {
		name, kind, cause string
		err               error
		status            int
	}{
		{"header timeout", "timeout", "i/o timeout", &url.Error{Op: "Post", URL: requestURL, Err: os.ErrDeadlineExceeded}, http.StatusGatewayTimeout},
		{"request deadline", "timeout", "context deadline exceeded", fmt.Errorf("forward: %w", context.DeadlineExceeded), http.StatusGatewayTimeout},
		{"cancellation", "canceled", "context canceled", fmt.Errorf("forward: %w", context.Canceled), http.StatusBadGateway},
		{"connection failure", "transport", "unexpected EOF", &url.Error{Op: "Post", URL: requestURL, Err: io.ErrUnexpectedEOF}, http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &rawClient{fn: func(_ context.Context, raw provider.RawRequest) (*http.Response, error) {
				calls++
				if raw.Endpoint != "/responses" {
					t.Fatal("wrong upstream endpoint", raw.Endpoint)
				}
				return nil, test.err
			}}
			s := New("inspection-test-key")
			t.Cleanup(s.Close)
			s.Update(map[string]Target{"selected": {Client: client}}, map[string]Profile{"test": {ID: "test", Models: []Model{{ProviderID: "selected", ID: "public", Model: provider.Model{ID: "gpt-test", Endpoint: "/responses"}}}}})
			s.SetInspection(true)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, inspectionRequest("POST", "/test/v1/messages", `{"model":"public","max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`))
			if w.Code != test.status || calls != 1 || obj(parsed(t, w.Body.String())["error"])["type"] != "api_error" {
				t.Fatal("incorrect transport error or unexpected retry", w.Code, calls, w.Body.String())
			}
			logs := inspectionLogs(t, s)
			if len(logs) != 1 {
				t.Fatal("upstream failure was not captured", logs)
			}
			found := false
			for _, log := range logs[0].Logs {
				if log.Attrs["kind"] == test.kind && log.Attrs["provider_id"] == "selected" && log.Attrs["endpoint"] == "/responses" && strings.Contains(fmt.Sprint(log.Attrs["error"]), test.cause) {
					found = true
				}
			}
			if !found {
				t.Fatal("underlying transport cause was not recorded", logs[0].Logs)
			}
			data, _ := json.Marshal(logs)
			for _, secret := range []string{"upstream-test-user", "password-test-secret", "query-test-secret"} {
				if strings.Contains(string(data)+w.Body.String(), secret) {
					t.Fatal("request URL credentials leaked into diagnostics or API response")
				}
			}
		})
	}
}
