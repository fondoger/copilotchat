package provider

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNumericUpstreamErrorCodesKeepMessages(t *testing.T) {
	for _, raw := range []string{`{"message":"Slow down","code":429}`, `{"error":{"message":"Slow down","code":429}}`} {
		response := &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(raw))}
		var detail *HTTPError
		if err := ResponseError(response); !errors.As(err, &detail) || detail.Message != "Slow down" {
			t.Fatalf("numeric code hid the upstream message: %v", err)
		}
	}
	if err := streamError([]byte(`{"message":"Slow down","code":429}`)); err == nil || !strings.Contains(err.Error(), "Slow down") {
		t.Fatalf("numeric code hid the stream message: %v", err)
	}
}

func TestRecoveryLinkSurvivesHTTPAndStreamErrors(t *testing.T) {
	const message = "Sign in to continue. [Open Doubao](copilotchat://providers/doubao/open)"
	detail := map[string]string{"message": message}
	raw, _ := json.Marshal(map[string]any{"error": detail})
	stream, _ := json.Marshal(detail)
	response := &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(string(raw)))}
	for _, err := range []error{ResponseError(response), streamError(stream)} {
		if err == nil || !strings.HasSuffix(err.Error(), message) {
			t.Fatalf("recovery link lost from message: %v", err)
		}
	}
}
