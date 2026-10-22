package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestRawTokenCountingUsesProviderCredentials(t *testing.T) {
	client := NewClient("provider-token")
	client.accountLoaded = true
	for _, path := range []string{"/v1/messages/count_tokens", "/responses/input_tokens"} {
		client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != client.baseURL+path || r.Header.Get("Accept") != "application/json" || r.Header.Get("Authorization") != "Bearer provider-token" {
				t.Fatal("count request changed destination or authentication", r.URL)
			}
			if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Cookie") != "" {
				t.Fatal("agent credentials escaped")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":42}`))}, nil
		})}
		res, err := client.Forward(t.Context(), provider.RawRequest{Endpoint: path, Body: []byte(`{"model":"test"}`), Header: http.Header{"X-Api-Key": {"local-secret"}, "Cookie": {"local-cookie"}}})
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if _, err := client.Forward(context.Background(), provider.RawRequest{Endpoint: "https://example.com/responses/input_tokens"}); err == nil {
		t.Fatal("counting allowed an arbitrary destination")
	}
}
