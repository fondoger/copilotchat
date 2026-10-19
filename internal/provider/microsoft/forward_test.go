package microsoft

import (
	"context"
	"io"
	"net/http"
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
