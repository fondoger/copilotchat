package microsoft

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestRawForwardingOwnsCredentialsAndPreservesPayload(t *testing.T) {
	client := NewClient(func(context.Context) (string, error) { return "fixture-upstream", nil }, provider.AccountInfo{})
	body := []byte(`{"model":"claude-fixture","thinking":{"type":"adaptive"},"system":[{"text":"prefix","cache_control":{"type":"ephemeral"}}],"unknown":{"preserved":true}}`)
	previousCall, previousCorrelation := "", ""
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != BaseURL+"/v1/messages" {
			t.Fatal(r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer fixture-upstream" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("local credentials escaped")
		}
		for key, value := range map[string]string{"X-ConversationId": "persistent-chat-id", "X-ClientType": "GitHub", "X-ClientFeature": "LlmApiProxy", "X-ClientScenario": "Code", "X-Llm-Api-Version": "2026-09-28", "Anthropic-Beta": "fixture-beta", "X-Initiator": "agent"} {
			if r.Header.Get(key) != value {
				t.Fatalf("%s = %s", key, r.Header.Get(key))
			}
		}
		call, correlation := r.Header.Get("X-ClientCallId"), r.Header.Get("X-ClientCorrelationId")
		if len(call) != 36 || len(correlation) != 36 || call == previousCall || correlation == previousCorrelation {
			t.Fatal("request IDs reused")
		}
		previousCall, previousCorrelation = call, correlation
		got, _ := io.ReadAll(r.Body)
		if string(got) != string(body) {
			t.Fatal("payload modified")
		}
		return mockResponse(r, "application/json", `{"type":"message"}`), nil
	})}
	for i := 0; i < 2; i++ {
		res, err := client.Forward(context.Background(), provider.RawRequest{Endpoint: "/v1/messages", Body: body, ConversationID: "persistent-chat-id", AgentInitiated: true, Header: http.Header{"Authorization": {"Bearer local-secret"}, "X-Api-Key": {"local-secret"}, "Cookie": {"private"}, "Anthropic-Beta": {"fixture-beta"}}})
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if _, err := client.Forward(context.Background(), provider.RawRequest{Endpoint: "https://example.com"}); err == nil {
		t.Fatal("arbitrary destination allowed")
	}
}

func TestRawCompactionUsesServiceCredentialsAndReturnsErrors(t *testing.T) {
	client := NewClient(func(context.Context) (string, error) { return "fixture-upstream", nil }, provider.AccountInfo{})
	body := []byte("{\"model\":\"upstream-model\",\"input\":[{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}]}")
	for _, status := range []int{200, 422} {
		calls := 0
		client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			data, _ := io.ReadAll(r.Body)
			if r.URL.String() != BaseURL+"/responses/compact" || string(data) != string(body) {
				t.Fatal("compaction path or body changed")
			}
			for key, want := range map[string]string{"Authorization": "Bearer fixture-upstream", "X-ConversationId": "thread", "X-Initiator": "agent", "Accept": "application/json"} {
				if r.Header.Get(key) != want {
					t.Fatal(key, r.Header.Get(key))
				}
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("upstream-body"))}, nil
		})}
		res, err := client.Forward(context.Background(), provider.RawRequest{Endpoint: "/responses/compact", Body: body, ConversationID: "thread", AgentInitiated: true})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != status || string(data) != "upstream-body" || calls != 1 {
			t.Fatal("compaction response altered or retried")
		}
	}
}

func TestRawTokenCountingUsesProviderCredentials(t *testing.T) {
	client := NewClient(func(context.Context) (string, error) { return "provider-token", nil }, provider.AccountInfo{})
	for _, path := range []string{"/v1/messages/count_tokens", "/responses/input_tokens"} {
		client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != BaseURL+path || r.Header.Get("Accept") != "application/json" || r.Header.Get("Authorization") != "Bearer provider-token" {
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
}
