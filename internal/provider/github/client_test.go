package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(server *httptest.Server) *Client {
	client := NewClient("test-token")
	client.http = server.Client()
	client.githubURL, client.githubAPIURL, client.baseURL = server.URL, server.URL, server.URL
	return client
}

func TestDeviceLoginRespectsPendingAndSlowDown(t *testing.T) {
	var polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Accept") != "application/json" {
			t.Error("missing OAuth headers")
		}
		if r.URL.Path == "/copilot_internal/user" {
			if r.Header.Get("Authorization") != "Bearer authorized-token" {
				t.Error("entitlement lookup did not use the GitHub OAuth token")
			}
			io.WriteString(w, `{"chat_enabled":true}`)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["client_id"] != clientID {
			t.Errorf("unexpected OAuth client: %q", body["client_id"])
		}
		switch r.URL.Path {
		case "/login/device/code":
			if body["scope"] != "read:user" {
				t.Error("incorrect scope")
			}
			io.WriteString(w, `{"device_code":"device-secret","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":2}`)
		case "/login/oauth/access_token":
			if body["device_code"] != "device-secret" || body["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Error("incorrect device token request")
			}
			polls++
			switch polls {
			case 1:
				io.WriteString(w, `{"error":"authorization_pending"}`)
			case 2:
				io.WriteString(w, `{"error":"slow_down"}`)
			default:
				io.WriteString(w, `{"access_token":"authorized-token"}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := testClient(server)
	var delays []time.Duration
	client.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	code, err := client.BeginLogin(context.Background())
	if err != nil || code.UserCode != "ABCD-1234" {
		t.Fatalf("BeginLogin: %#v, %v", code, err)
	}
	token, err := client.PollLogin(context.Background(), code)
	if err != nil || token != "authorized-token" {
		t.Fatalf("PollLogin: %q, %v", token, err)
	}
	if !reflect.DeepEqual(delays, []time.Duration{5 * time.Second, 10 * time.Second}) {
		t.Fatalf("incorrect poll delays: %v", delays)
	}
}

func TestDeviceLoginCancellationAndExpiration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"error":"authorization_pending"}`)
	}))
	defer server.Close()
	client := testClient(server)
	ctx, cancel := context.WithCancel(context.Background())
	client.sleep = func(ctx context.Context, delay time.Duration) error {
		cancel()
		return sleepContext(ctx, delay)
	}
	_, err := client.PollLogin(ctx, DeviceCode{DeviceCode: "code", ExpiresIn: 900, Interval: 5})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
	_, err = client.PollLogin(context.Background(), DeviceCode{DeviceCode: "code", ExpiresIn: 1, issuedAt: time.Now().Add(-2 * time.Second)})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired device code was accepted: %v", err)
	}
}

func TestModelsUsesLiveEndpointMetadataAndEntitlement(t *testing.T) {
	var denied atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing OAuth bearer token")
		}
		if r.URL.Path == "/copilot_internal/user" {
			fmt.Fprintf(w, `{"chat_enabled":%t,"can_signup_for_limited":true}`, !denied.Load())
			return
		}
		if r.URL.Path != "/models" || r.Header.Get("X-GitHub-Api-Version") != apiVersion {
			t.Errorf("incorrect model discovery request: %s", r.URL.Path)
		}
		io.WriteString(w, `{"data":[
		 {"id":"gpt-5","name":"GPT Five","model_picker_enabled":true,"supported_endpoints":["/chat/completions"],"capabilities":{"supports":{"vision":true},"limits":{"max_output_tokens":16000}}},
		 {"id":"claude","name":"Claude","supported_endpoints":["/responses","/v1/messages"],"capabilities":{"limits":{"vision":{"supported_media_types":["image/png","application/pdf"]}}}},
		 {"id":"gpt-4o-mini","model_picker_enabled":false},
		 {"id":"disabled","policy":{"state":"disabled"}},
		 {"id":"gpt-5-chat-latest"},
		 {"name":"invalid"}
		]}`)
	}))
	defer server.Close()
	client := testClient(server)
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Endpoint != "/chat/completions" || models[1].Endpoint != "/v1/messages" || !models[1].Vision || !models[1].PDF {
		t.Fatalf("incorrect model metadata: %#v", models)
	}
	if _, present := client.models["gpt-4o-mini"]; !present {
		t.Error("utility model should be retained outside the picker")
	}
	denied.Store(true)
	client = testClient(server)
	if _, err := client.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "Copilot Free") {
		t.Fatalf("missing entitlement should be actionable: %v", err)
	}
}

func TestStreamProtocolsAndToolCalls(t *testing.T) {
	tests := []struct {
		name, endpoint, stream string
	}{
		{"chat", "/chat/completions", `data: {"choices":[{"delta":{"content":"Hello "}}]}

data: {"choices":[{"delta":{"content":"world","reasoning_text":"think","reasoning_opaque":"signature","tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"test\"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`},
		{"responses", "/responses", `event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"changing_1","delta":"Hello "}

data: {"type":"response.output_text.delta","item_id":"changing_2","delta":"world"}

data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"q\":\"test\"}"}

data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"test\"}"}}

data: {"type":"response.output_item.done","output_index":2,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"opaque","summary":[]}}

data: {"type":"response.completed","response":{}}

`},
		{"messages", "/v1/messages", `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hello "}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"test\"}"}}

data: {"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":""}}

data: {"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"think"}}

data: {"type":"content_block_delta","index":2,"delta":{"type":"signature_delta","signature":"signed"}}

data: {"type":"content_block_stop","index":2}

data: {"type":"message_stop"}

`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.endpoint || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("incorrect endpoint or token: %s", r.URL.Path)
				}
				if r.Header.Get("Copilot-Vision-Request") != "true" || r.Header.Get("x-initiator") != "user" || r.Header.Get("X-Interaction-Id") != "conversation-1" {
					t.Error("missing request classification headers")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "model" || body["stream"] != true || len(body["tools"].([]any)) != 1 {
					t.Errorf("incorrect request: %#v", body)
				}
				if test.endpoint == "/responses" && (body["input"] == nil || body["store"] != false) {
					t.Error("Responses request must be stateless and contain input")
				}
				if test.endpoint == "/v1/messages" && (body["max_tokens"] == nil || r.Header.Get("anthropic-version") == "") {
					t.Error("Messages request missing required fields")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, test.stream)
			}))
			defer server.Close()
			client := testClient(server)
			client.accountLoaded = true
			client.models["model"] = Model{ID: "model", Name: "Model", Endpoint: test.endpoint, Vision: true}
			request := Request{Model: "model", ConversationID: "conversation-1", Tools: []Tool{{Name: "lookup"}}, Messages: []Message{
				{Role: "user", Content: "hello", Attachments: []Attachment{{Name: "image.png", MIME: "image/png", Data: "aGVsbG8="}}},
			}}
			var visible strings.Builder
			result, err := client.StreamWithTools(context.Background(), request, func(s string) { visible.WriteString(s) })
			if err != nil {
				t.Fatal(err)
			}
			if visible.String() != "Hello world" || result.Content != visible.String() || len(result.ToolCalls) != 1 || result.ToolCalls[0] != (ToolCall{ID: "call_1", Name: "lookup", Arguments: `{"q":"test"}`}) {
				t.Fatalf("incorrect completion: %#v", result)
			}
			if test.endpoint != "/chat/completions" && len(result.ProviderData) != 1 {
				t.Fatal("reasoning signature not preserved")
			}
			request.Messages = append(request.Messages, result.AssistantMessage(), Message{Role: "tool", ToolCallID: "call_1", Content: "found"})
			body, _, err := requestBody(request, client.models["model"])
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(body)
			if !strings.Contains(string(encoded), "call_1") || !strings.Contains(string(encoded), "found") {
				t.Fatal("tool result not replayed")
			}
		})
	}
}

func TestStreamFailureAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name, response, want string
		status               int
	}{
		{"incomplete", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "before completion", 200},
		{"invalid-json", "data: {bad}\n\n", "invalid Copilot stream JSON", 200},
		{"api-error", `{"error":{"message":"subscription required"}}`, "subscription required", 403},
		{"malformed-tool", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call\",\"function\":{\"name\":\"tool\",\"arguments\":\"{bad}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", "invalid tool call", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(test.status)
				io.WriteString(w, test.response)
			}))
			defer server.Close()
			client := testClient(server)
			client.accountLoaded = true
			client.models["model"] = Model{ID: "model", Endpoint: "/chat/completions"}
			_, err := client.StreamWithTools(context.Background(), Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}}, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wanted %q, got %v", test.want, err)
			}
		})
	}
	t.Run("cancel-active-stream", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		defer server.Close()
		client := testClient(server)
		client.accountLoaded = true
		client.models["model"] = Model{ID: "model", Endpoint: "/chat/completions"}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result, err := client.StreamWithTools(ctx, Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}}, func(string) { cancel() })
		if !errors.Is(err, context.Canceled) || result.Content != "partial" {
			t.Fatalf("cancellation lost partial result: %#v, %v", result, err)
		}
	})
}

func TestBackgroundRequestUsesAgentClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-initiator") != "agent" || r.Header.Get("X-Interaction-Type") != "conversation-background" {
			t.Error("background title request was classified as a user turn")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Title\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := testClient(server)
	client.accountLoaded = true
	client.models["model"] = Model{ID: "model", Endpoint: "/chat/completions"}
	result, err := client.StreamWithTools(context.Background(), Request{Model: "model", Background: true, Messages: []Message{{Role: "user", Content: "name this conversation"}}}, nil)
	if err != nil || result.Content != "Title" {
		t.Fatalf("background completion failed: %#v, %v", result, err)
	}
}

func TestAttachmentsRejectUnsupportedFilesAndPreserveText(t *testing.T) {
	model := Model{ID: "text", Name: "Text model", Endpoint: "/chat/completions"}
	request := Request{Model: model.ID, Messages: []Message{{Role: "user", Content: "read", Attachments: []Attachment{
		{Name: "notes.txt", MIME: "text/plain", Data: base64.StdEncoding.EncodeToString([]byte("你好, world"))},
	}}}}
	body, _, err := requestBody(request, model)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(body)
	if !strings.Contains(string(encoded), "你好, world") {
		t.Fatal("text attachment contents were dropped")
	}
	request.Messages[0].Attachments[0] = Attachment{Name: "photo.png", MIME: "image/png", Data: "aGVsbG8="}
	if _, _, err := requestBody(request, model); err == nil || !strings.Contains(err.Error(), "does not support image") {
		t.Fatalf("unsupported image was accepted: %v", err)
	}
	request.Messages[0].Attachments[0].Data = "invalid-base64"
	if _, _, err := requestBody(request, model); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("invalid base64 accepted: %v", err)
	}
}
