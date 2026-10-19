package microsoft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// This synthetic fixture retains the sparse structure observed in M365's model
// catalog. It intentionally includes capabilities omitted by that service.
const m365CatalogFixture = `{"data":[
	{"id":"claude-opus-4.8","name":"Claude Opus 4.8","supported_endpoints":["/v1/messages"],"capabilities":{"limits":{"max_context_window_tokens":1000000},"supports":{"reasoning_effort":["low","medium","high","xhigh","max"]}}},
	{"id":"claude-haiku-4.5","name":"Claude Haiku 4.5","supported_endpoints":["/v1/messages"],"capabilities":{"limits":{"max_context_window_tokens":200000}}},
	{"id":"gpt-5.6-sol","supported_endpoints":["/responses"],"capabilities":{"supports":{"reasoning_effort":["none","low","high"]}}},
	{"id":"claude-sonnet-4.6","supported_endpoints":["/v1/messages"],"capabilities":{"supports":{"vision":false,"tool_calls":false,"adaptive_thinking":false,"reasoning_effort":["high"]}}},
	{"id":"future-model","supported_endpoints":["/v1/messages"]},
	{"id":"restricted-model","supported_endpoints":["/v1/messages"],"policy":{"state":"disabled"}},
	{"id":"hidden-model","supported_endpoints":["/chat/completions"],"model_picker_enabled":false},
	{"id":"unsafe-model","supported_endpoints":["https://untrusted.invalid/v1/messages"]},
	{"id":"unknown-route","supported_endpoints":["/future-protocol"]}
]}`

func TestM365CatalogUsesRenewableTokenAndPinnedService(t *testing.T) {
	sourceCalls, requests := 0, 0
	client := NewClient(func(context.Context) (string, error) {
		sourceCalls++
		return fmt.Sprintf("entra-fixture-%d", sourceCalls), nil
	}, AccountInfo{Login: "member@example.test", Host: "untrusted.invalid"})
	// Microsoft has no GitHub credential or configurable endpoint fields.
	// Only the pinned source can authorize these requests.
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method != http.MethodGet || r.URL.String() != BaseURL+"/models" {
			t.Fatalf("catalog crossed the fixed M365 service boundary: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer entra-fixture-%d", requests) {
			t.Fatal("catalog did not use the current Entra token source")
		}
		if r.Header.Get("X-ClientType") != "GitHub" || r.Header.Get("X-GitHub-Api-Version") != "" {
			t.Fatalf("incorrect M365 catalog headers: %#v", r.Header)
		}
		return mockResponse(r, "application/json", m365CatalogFixture), nil
	})
	account, err := client.Account(context.Background())
	if err != nil || account != (AccountInfo{Login: "member@example.test", Host: Host}) || client.Host() != Host {
		t.Fatalf("pinned account was lost: %#v, %v", account, err)
	}
	if requests != 0 || sourceCalls != 0 {
		t.Fatal("M365 identity lookup called a GitHub entitlement/account endpoint")
	}
	for range 2 {
		models, err := client.Models(context.Background())
		if err != nil || len(models) != 5 {
			t.Fatalf("incorrect M365 catalog: %#v, %v", models, err)
		}
		opus := models[0]
		if !opus.AdaptiveThinking || !opus.Reasoning || !opus.Vision || !opus.Tools || opus.PDF || opus.MaxOutputTokens != 0 || !reflect.DeepEqual(opus.ThinkingEfforts, []string{"low", "medium", "high", "xhigh", "max"}) {
			t.Fatalf("sparse Claude catalog lost supported capabilities: %#v", opus)
		}
		haiku := models[1]
		if !haiku.Reasoning || haiku.AdaptiveThinking || haiku.MinThinkingBudget != 1024 || haiku.MaxThinkingBudget != 8191 {
			t.Fatalf("Haiku budget-based thinking was lost: %#v", haiku)
		}
		if models[2].Endpoint != "/responses" || !reflect.DeepEqual(models[2].ThinkingEfforts, []string{"none", "low", "high"}) {
			t.Fatalf("non-Anthropic route/efforts were changed: %#v", models[2])
		}
		restricted := models[3]
		if restricted.Vision || restricted.Tools || restricted.AdaptiveThinking || len(restricted.ThinkingEfforts) != 0 {
			t.Fatalf("M365 defaults overrode explicit restrictions: %#v", restricted)
		}
		if models[4].VisionKnown || models[4].ToolsKnown || models[4].ReasoningKnown {
			t.Fatalf("unknown model capabilities were guessed: %#v", models[4])
		}
		if _, exists := client.models["hidden-model"]; !exists {
			t.Fatal("a picker-hidden model was removed from the inference catalog")
		}
		if _, exists := client.models["unsafe-model"]; exists {
			t.Fatal("a catalog-supplied endpoint URL was accepted")
		}
	}
	if requests != 2 || sourceCalls != 2 {
		t.Fatal("M365 refresh reused or replaced a GitHub token")
	}
}

func TestM365StreamPreservesThinkingToolsAndCachedUsage(t *testing.T) {
	sourceCalls, requests := 0, 0
	client := NewClient(func(context.Context) (string, error) {
		sourceCalls++
		return fmt.Sprintf("entra-fixture-%d", sourceCalls), nil
	}, AccountInfo{Login: "member@example.test"})
	client.models["claude-opus-4.8"] = Model{ID: "claude-opus-4.8", Name: "Claude Opus 4.8", Endpoint: "/v1/messages", Vision: true, Tools: true, Reasoning: true, AdaptiveThinking: true, ThinkingEfforts: []string{"high"}}
	var bodies []map[string]any
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.String() != BaseURL+"/v1/messages" || r.Method != http.MethodPost {
			t.Fatalf("wrong inference destination: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer entra-fixture-%d", requests) {
			t.Fatal("inference used a stale token")
		}
		if r.Header.Get("X-ClientType") != "GitHub" || r.Header.Get("X-ConversationId") != "stable-conversation" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("missing M365 inference headers: %#v", r.Header)
		}
		for _, key := range []string{"anthropic-beta", "X-GitHub-Api-Version", "X-Session-Id", "Copilot-Vision-Request"} {
			if r.Header.Get(key) != "" {
				t.Errorf("GitHub-only header %s reached the M365 service", key)
			}
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		bodies = append(bodies, body)
		if body["thinking"].(map[string]any)["type"] != "adaptive" || body["output_config"].(map[string]any)["effort"] != "high" {
			t.Fatal("advertised Claude effort was not encoded")
		}
		if countCacheMarkers(body) != 3 {
			t.Fatal("M365 request lost system, tools, or conversation cache breakpoint")
		}
		stream := strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":22,"cache_read_input_tokens":70,"cache_creation_input_tokens":9}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Visible reasoning"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-fixture"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":"Answer"}}`,
			`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":{}}}`,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"example\"}"}}`,
			`data: {"type":"message_delta","usage":{"output_tokens":11}}`,
			`data: {"type":"message_stop"}`,
		}, "\n\n") + "\n\n"
		return mockResponse(r, "text/event-stream", stream), nil
	})
	request := Request{Model: "claude-opus-4.8", ConversationID: "stable-conversation", ReasoningEffort: "high", Tools: []Tool{{Name: "lookup"}}, Messages: []Message{
		{Role: "system", Content: "Stable instructions"},
		{Role: "user", Content: "Read the image", Attachments: []Attachment{{Name: "image.png", MIME: "image/png", Data: "aW1hZ2U="}}},
	}}
	var events []StreamEvent
	first, err := client.StreamWithEvents(context.Background(), request, func(event StreamEvent) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if first.Content != "Answer" || first.Reasoning != "Visible reasoning" || len(first.ToolCalls) != 1 || first.ToolCalls[0].Arguments != `{"query":"example"}` || len(first.ProviderData) != 1 {
		t.Fatalf("M365 stream lost reasoning/tool replay state: %#v", first)
	}
	if first.Usage.InputTokens != 101 || first.Usage.CachedInputTokens != 70 || first.Usage.OutputTokens != 11 || !first.Usage.CachedInputAvailable {
		t.Fatalf("M365 usage was misreported: %#v", first.Usage)
	}
	if !reflect.DeepEqual(events, []StreamEvent{{Kind: EventReasoning, Delta: "Visible reasoning"}, {Kind: EventText, Delta: "Answer"}}) {
		t.Fatalf("opaque data leaked into visible events: %#v", events)
	}
	request.Messages = append(request.Messages, first.AssistantMessage(), Message{Role: "tool", ToolCallID: "call-1", Content: "lookup result"})
	if _, err := client.StreamWithEvents(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || sourceCalls != 2 {
		t.Fatal("a follow-up did not obtain a fresh access token")
	}
	secondBody, _ := json.Marshal(bodies[1])
	if !strings.Contains(string(secondBody), `"signature":"opaque-fixture"`) || !strings.Contains(string(secondBody), `"tool_use_id":"call-1"`) {
		t.Fatal("the follow-up lost Claude's signed thinking or tool results")
	}
}

func TestM365DoesNotFallBackAfterCredentialFailure(t *testing.T) {
	wantErr := errors.New("Entra sign-in expired")
	for _, source := range []TokenSource{
		nil,
		func(context.Context) (string, error) { return "", wantErr },
		func(context.Context) (string, error) { return " ", nil },
	} {
		client := NewClient(source, AccountInfo{})
		client.models["model"] = Model{ID: "model", Endpoint: "/v1/messages"}
		client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Fatalf("credential failure unexpectedly issued %s", r.URL)
			return nil, nil
		})
		if _, err := client.Models(context.Background()); err == nil {
			t.Fatal("catalog accepted an unavailable Entra credential")
		}
		if _, err := client.StreamWithEvents(context.Background(), Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}}, nil); err == nil {
			t.Fatal("inference accepted an unavailable Entra credential")
		}
		for _, method := range []string{"BeginLogin", "PollLogin"} {
			if _, exists := reflect.TypeOf(client).MethodByName(method); exists {
				t.Fatal("Microsoft inference exposes GitHub device authentication")
			}
		}
	}
}

func TestM365RejectsMalformedCatalogAndRedirects(t *testing.T) {
	for _, fixture := range []string{`{}`, `{"data":null}`, `{"data":"invalid"}`} {
		client := NewClient(func(context.Context) (string, error) { return "entra-fixture", nil }, AccountInfo{})
		client.models["existing"] = Model{ID: "existing"}
		client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return mockResponse(r, "application/json", fixture), nil
		})
		if _, err := client.Models(context.Background()); err == nil || len(client.models) != 1 {
			t.Fatal("malformed catalog replaced the existing model cache")
		}
	}
	for _, destination := range []string{"https://api.github.com/user", BaseURL + "/v1/messages?redirect=1"} {
		client := NewClient(func(context.Context) (string, error) { return "entra-fixture", nil }, AccountInfo{})
		requests := 0
		client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests++
			if requests > 1 {
				t.Fatalf("Entra credential followed a redirect to %s", r.URL)
			}
			return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {destination}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: r}, nil
		})
		if _, err := client.Models(context.Background()); err == nil || requests != 1 {
			t.Fatalf("M365 redirect policy failed: %v, requests=%d", err, requests)
		}
		if _, err := client.do(context.Background(), http.MethodGet, destination, nil, "entra-fixture"); err == nil || requests != 1 {
			t.Fatal("credential-bearing request escaped the explicit M365 route allowlist")
		}
	}
}
