package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestNativeSearchCatalogAndPolicy(t *testing.T) {
	for _, advertised := range []string{`true`, `false`, `null`, `"missing"`} {
		t.Run(advertised, func(t *testing.T) {
			capabilities := `{}`
			if advertised != `"missing"` {
				capabilities = `{"server_tools":{"web_search":{"supported":` + advertised + `}}}`
			}
			var entry CatalogEntry
			if err := json.Unmarshal([]byte(`{"id":"claude-fixture","capabilities":`+capabilities+`}`), &entry); err != nil {
				t.Fatal(err)
			}
			model := entry.Model("/v1/messages")
			if model.NativeSearchKnown != (advertised == `true` || advertised == `false`) || model.NativeSearch != (advertised == `true`) || SupportsNativeSearch(model) != (advertised == `true`) {
				t.Fatalf("capability not preserved: %+v", model)
			}
			model.ID, model.Endpoint = "GPT-fixture", "/responses"
			if SupportsNativeSearch(model) != (advertised != `false`) {
				t.Fatalf("explicit false must override GPT default: %+v", model)
			}
		})
	}
	for _, test := range []struct {
		id, name, endpoint string
		want               bool
	}{
		{"GPT-legacy", "", "/responses", true},
		{"alias", "My GPT Model", "/responses", true},
		{"gpt-something-new", "", "/responses", true},
		{"grok-4.7", "", "/responses", true},
		{"GROK-4.6-2026-10-01", "", "/responses", true},
		{"grok-4.7-20261001", "", "/responses", true},
		{"grok-4.70", "", "/responses", false},
		{"grok-4.7-unknown", "", "/responses", false},
		{"grok-4.6-fast", "", "/responses", false},
		{"alias", "Grok 4.7", "/responses", false},
		{"grok-4.5", "", "/responses", false},
		{"claude-search", "", "/responses", false},
		{"gpt-new", "", "/v1/messages", false},
		{"gpt-new", "", "/chat/completions", false},
	} {
		if got := SupportsNativeSearch(Model{ID: test.id, Name: test.name, Endpoint: test.endpoint}); got != test.want {
			t.Errorf("%+v: got %v", test, got)
		}
	}
}

func TestNativeSearchToolsAreHostedAndOptIn(t *testing.T) {
	for _, endpoint := range []string{"/v1/messages", "/responses"} {
		for _, mcp := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				request := Request{Model: "model", NativeSearch: enabled, Messages: []Message{{Role: "user", Content: "question"}}}
				if mcp {
					request.Tools = []Tool{{Name: "lookup", Description: "Local lookup"}}
				}
				before, _ := json.Marshal(request)
				body, _, err := RequestBody(request, Model{ID: "model", Endpoint: endpoint, NativeSearchKnown: true, NativeSearch: true})
				if err != nil {
					t.Fatal(err)
				}
				withoutMarkers(body)
				tools, _ := body["tools"].([]any)
				wantCount := 0
				if mcp {
					wantCount++
				}
				if enabled {
					wantCount++
				}
				if len(tools) != wantCount {
					t.Fatalf("wrong tools: %#v", tools)
				}
				if mcp {
					want := map[string]any{"type": "function", "name": "lookup", "description": "Local lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}, "strict": false}
					if endpoint == "/v1/messages" {
						want = map[string]any{"name": "lookup", "description": "Local lookup", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}}
					}
					if !reflect.DeepEqual(tools[0], want) {
						t.Fatalf("MCP tool changed: %#v", tools[0])
					}
				}
				if enabled {
					want := map[string]any{"type": "web_search"}
					var choice any = "auto"
					if endpoint == "/v1/messages" {
						want = map[string]any{"type": "web_search_20250305", "name": "web_search"}
						choice = map[string]any{"type": "auto"}
					}
					if !reflect.DeepEqual(tools[len(tools)-1], want) || !reflect.DeepEqual(body["tool_choice"], choice) {
						t.Fatalf("wrong hosted tool: %#v", body)
					}
				} else if body["tool_choice"] != nil {
					t.Fatal("disabled search altered tool selection")
				}
				after, _ := json.Marshal(request)
				if string(before) != string(after) {
					t.Fatal("request mutated")
				}
			}
		}
	}
	request := Request{Model: "unsupported", NativeSearch: true, Messages: []Message{{Role: "user", Content: "question"}}}
	if _, _, err := RequestBody(request, Model{Endpoint: "/v1/messages"}); err == nil {
		t.Fatal("unsupported search accepted")
	}
	request.Tools = []Tool{{Name: "web_search"}}
	if _, _, err := RequestBody(request, Model{Endpoint: "/v1/messages", NativeSearchKnown: true, NativeSearch: true}); err == nil {
		t.Fatal("colliding native and MCP tool names accepted")
	}
}

type searchBackend struct {
	model                 Model
	contentType, response string
}

func (b searchBackend) LookupModel(context.Context, string) (Model, error) { return b.model, nil }
func (b searchBackend) PrepareInference(ctx context.Context, _ Request, _ Model, body []byte, _ bool) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, "POST", "https://fixture.invalid", strings.NewReader(string(body)))
}
func (b searchBackend) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{b.contentType}}, Body: io.NopCloser(strings.NewReader(b.response))}, nil
}
func runSearchFixture(endpoint, contentType, response string) (Completion, []StreamEvent, error) {
	backend := searchBackend{model: Model{ID: "fixture", Endpoint: endpoint, NativeSearchKnown: true, NativeSearch: true}, contentType: contentType, response: response}
	var events []StreamEvent
	completion, err := NewEngine(backend).StreamWithEvents(context.Background(), Request{Model: "fixture", NativeSearch: true, Messages: []Message{{Role: "user", Content: "question"}}}, func(event StreamEvent) { events = append(events, event) })
	return completion, events, err
}
func eventJSON(value any) string {
	data, _ := json.Marshal(value)
	return "data: " + string(data) + "\n\n"
}
func rawBlocks(t *testing.T, text string) []json.RawMessage {
	t.Helper()
	var blocks []json.RawMessage
	if err := json.Unmarshal([]byte(text), &blocks); err != nil {
		t.Fatal(err)
	}
	return blocks
}

const messagesSearchBlocks = `[
 {"type":"text","text":"Before. "},
 {"type":"thinking","thinking":"Considering","signature":"signed-thinking"},
 {"type":"server_tool_use","id":"srv1","name":"web_search","input":{"query":"latest news"}},
 {"type":"web_search_tool_result","tool_use_id":"srv1","content":[{"type":"web_search_result","url":"https://EXAMPLE.com/news","title":"News","encrypted_content":"encrypted-search-result"}]},
 {"type":"text","text":"After.","citations":[{"type":"web_search_result_location","url":"https://example.com/news","title":"News","encrypted_index":"opaque-citation","cited_text":"Result"}]},
 {"type":"tool_use","id":"mcp1","name":"lookup","input":{"key":"value"}}
]`

func messagesSearchSSE(t *testing.T, blocks []json.RawMessage, stop string) string {
	t.Helper()
	var stream strings.Builder
	for index, raw := range blocks {
		var block map[string]any
		_ = json.Unmarshal(raw, &block)
		var deltas []map[string]any
		switch block["type"] {
		case "text":
			text := block["text"].(string)
			block["text"] = ""
			deltas = append(deltas, map[string]any{"type": "text_delta", "text": text})
			if citations, ok := block["citations"].([]any); ok {
				delete(block, "citations")
				for _, citation := range citations {
					deltas = append(deltas, map[string]any{"type": "citations_delta", "citation": citation})
				}
			}
		case "thinking":
			deltas = append(deltas, map[string]any{"type": "thinking_delta", "thinking": block["thinking"]}, map[string]any{"type": "signature_delta", "signature": block["signature"]})
			block["thinking"], block["signature"] = "", ""
		case "server_tool_use", "tool_use":
			input, _ := json.Marshal(block["input"])
			block["input"] = map[string]any{}
			// Deliberately split within JSON string tokens, not just boundaries.
			for _, fragment := range []string{string(input[:3]), string(input[3:8]), string(input[8:])} {
				deltas = append(deltas, map[string]any{"type": "input_json_delta", "partial_json": fragment})
			}
		}
		stream.WriteString(eventJSON(map[string]any{"type": "content_block_start", "index": index, "content_block": block}))
		for _, delta := range deltas {
			stream.WriteString(eventJSON(map[string]any{"type": "content_block_delta", "index": index, "delta": delta}))
		}
		stream.WriteString(eventJSON(map[string]any{"type": "content_block_stop", "index": index}))
	}
	stream.WriteString(eventJSON(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}}))
	stream.WriteString(eventJSON(map[string]any{"type": "message_stop"}))
	return stream.String()
}

func TestMessagesSearchReplayStreamAndJSON(t *testing.T) {
	blocks := rawBlocks(t, messagesSearchBlocks)
	for _, contentType := range []string{"text/event-stream", "application/json"} {
		response := messagesSearchSSE(t, blocks, "pause_turn")
		if contentType == "application/json" {
			response = `{"stop_reason":"pause_turn","content":` + messagesSearchBlocks + `}`
		}
		completion, events, err := runSearchFixture("/v1/messages", contentType, response)
		if err != nil {
			t.Fatal(err)
		}
		if completion.Content != "Before. After." || completion.Reasoning != "Considering" || completion.StopReason != "pause_turn" {
			t.Fatalf("visible response lost: %+v", completion)
		}
		if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "lookup" || completion.ToolCalls[0].Arguments != `{"key":"value"}` {
			t.Fatalf("hosted search became an executable tool call: %+v", completion.ToolCalls)
		}
		if !reflect.DeepEqual(completion.Citations, []Citation{{URL: "https://example.com/news", Title: "News"}}) {
			t.Fatalf("citations lost: %+v", completion.Citations)
		}
		if len(completion.ProviderData) != 1 || !strings.Contains(string(completion.ProviderData[0]), "signed-thinking") {
			t.Fatal("legacy thinking lost")
		}
		for _, event := range events {
			if strings.Contains(event.Delta, "encrypted") || strings.Contains(event.Delta, "signed-thinking") || strings.Contains(event.Delta, "latest news") {
				t.Fatalf("opaque/tool state leaked into text: %+v", event)
			}
		}
		if completion.Replay == nil || completion.Replay.Protocol != "/v1/messages" || len(completion.Replay.Blocks) != len(blocks) {
			t.Fatalf("ordered replay missing: %+v", completion.Replay)
		}
		message := completion.AssistantMessage()
		request := Request{Model: "fixture", Messages: []Message{{Role: "user", Content: "question"}, message, {Role: "tool", ToolCallID: "mcp1", Content: "result"}}}
		body, _, err := RequestBody(request, Model{Endpoint: "/v1/messages"})
		if err != nil {
			t.Fatal(err)
		}
		withoutMarkers(body)
		messages := body["messages"].([]any)
		got, _ := json.Marshal(messages[1].(map[string]any)["content"])
		var expected, actual any
		_ = json.Unmarshal([]byte(messagesSearchBlocks), &expected)
		_ = json.Unmarshal(got, &actual)
		if !reflect.DeepEqual(expected, actual) {
			t.Fatalf("replay changed or duplicated text/MCP: %s", got)
		}
		if len(messages) != 3 {
			t.Fatalf("tool result misplaced: %#v", messages)
		}
	}
}

const responsesSearchOutput = `[
 {"type":"reasoning","id":"reason1","summary":[{"type":"summary_text","text":"Considering"}],"encrypted_content":"encrypted-reasoning"},
 {"type":"web_search_call","id":"search1","status":"completed","action":{"type":"search","query":"news"}},
 {"type":"message","id":"msg1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Answer","annotations":[{"type":"url_citation","url":"https://example.com/news","title":"News","start_index":0,"end_index":6},{"type":"url_citation","url":"javascript:alert(1)","title":"Unsafe","start_index":0,"end_index":6}],"logprobs":[]}],"output_only_extension":"not-input"},
 {"type":"function_call","id":"fc1","call_id":"mcp1","name":"lookup","arguments":"{}","status":"completed"}
]`

func TestResponsesSearchReplayStreamAndJSON(t *testing.T) {
	blocks := rawBlocks(t, responsesSearchOutput)
	for _, contentType := range []string{"text/event-stream", "application/json"} {
		var stream strings.Builder
		stream.WriteString(eventJSON(map[string]any{"type": "response.output_text.delta", "delta": "Answer"}))
		stream.WriteString(eventJSON(map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "Considering"}))
		for index, raw := range blocks {
			stream.WriteString(eventJSON(map[string]any{"type": "response.output_item.done", "output_index": index, "item": raw}))
		}
		// Both item.done and response.completed carry snapshots: no duplicate state.
		stream.WriteString(eventJSON(map[string]any{"type": "response.completed", "response": map[string]any{"output": blocks}}))
		response := stream.String()
		if contentType == "application/json" {
			response = `{"status":"completed","output":` + responsesSearchOutput + `}`
		}
		completion, events, err := runSearchFixture("/responses", contentType, response)
		if err != nil {
			t.Fatal(err)
		}
		if completion.Content != "Answer" || completion.Reasoning != "Considering" || len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "lookup" || len(completion.ProviderData) != 1 {
			t.Fatalf("incorrect output: %+v", completion)
		}
		if !reflect.DeepEqual(completion.Citations, []Citation{{URL: "https://example.com/news", Title: "News"}}) {
			t.Fatalf("unsafe or missing citations: %+v", completion.Citations)
		}
		for _, event := range events {
			if strings.Contains(event.Delta, "encrypted") {
				t.Fatal("encrypted reasoning leaked")
			}
		}
		if completion.Replay == nil || !reflect.DeepEqual(completion.Replay.Blocks, blocks) {
			t.Fatalf("complete output not retained: %+v", completion.Replay)
		}
		body, _, err := RequestBody(Request{Model: "fixture", Messages: []Message{completion.AssistantMessage(), {Role: "tool", ToolCallID: "mcp1", Content: "result"}}}, Model{Endpoint: "/responses"})
		if err != nil {
			t.Fatal(err)
		}
		input := body["input"].([]any)
		if len(input) != 5 {
			t.Fatalf("replay duplicated text or MCP: %#v", input)
		}
		encoded, _ := json.Marshal(input)
		if strings.Contains(string(encoded), "logprobs") || strings.Contains(string(encoded), "output_only_extension") || !strings.Contains(string(encoded), "encrypted-reasoning") || !strings.Contains(string(encoded), "web_search_call") {
			t.Fatalf("invalid replay fields: %s", encoded)
		}
	}
}

func TestSearchErrorsAndPartialOutputReplaySafety(t *testing.T) {
	blocks := rawBlocks(t, `[{"type":"server_tool_use","id":"srv1","name":"web_search","input":{"query":"news"}},{"type":"web_search_tool_result","tool_use_id":"srv1","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}]`)
	for _, contentType := range []string{"text/event-stream", "application/json"} {
		response := messagesSearchSSE(t, blocks, "end_turn")
		if contentType == "application/json" {
			raw, _ := json.Marshal(blocks)
			response = `{"stop_reason":"end_turn","content":` + string(raw) + `}`
		}
		completion, _, err := runSearchFixture("/v1/messages", contentType, response)
		if err == nil || !strings.Contains(err.Error(), "max_uses_exceeded") || len(completion.ToolCalls) != 0 || len(completion.Citations) != 0 || completion.Replay == nil {
			t.Fatalf("search error became successful or lost safe replay: %+v, %v", completion, err)
		}
	}
	for _, test := range []struct{ endpoint, response string }{
		{"/v1/messages", `data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s","name":"web_search","input":{}}}` + "\n\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}` + "\n\n"},
		{"/v1/messages", `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c","name":"lookup","input":{}}}` + "\n\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{"}}` + "\n\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n"},
		{"/responses", `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c","name":"lookup","arguments":""}}` + "\n\n" + `data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{"}` + "\n\n"},
	} {
		completion, _, err := runSearchFixture(test.endpoint, "text/event-stream", test.response)
		if err == nil || completion.Replay != nil {
			t.Fatalf("partial output became authoritative: %+v %v", completion, err)
		}
	}
}

func TestResponsesHostedActionSources(t *testing.T) {
	output := rawBlocks(t, `[{"type":"web_search_call","id":"s","status":"completed","action":{"type":"search","query":"news","sources":["https://EXAMPLE.com/news",{"type":"url","url":"https://example.com/news","title":"News","vendor_extension":"not-input"},{"type":"url","url":"https://example.com/second","title":"Second"},{"type":"url","url":"javascript:alert(1)"},{"type":"url","url":"https://user:password@example.com/private"},{"type":"unknown","url":"https://example.com/ignore"}]}},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Answer","annotations":[]}]}]`)
	for _, contentType := range []string{"text/event-stream", "application/json"} {
		response := eventJSON(map[string]any{"type": "response.completed", "response": map[string]any{"output": output}})
		if contentType == "application/json" {
			raw, _ := json.Marshal(output)
			response = `{"status":"completed","output":` + string(raw) + `}`
		}
		completion, _, err := runSearchFixture("/responses", contentType, response)
		if err != nil {
			t.Fatal(err)
		}
		want := []Citation{{URL: "https://example.com/news", Title: "News"}, {URL: "https://example.com/second", Title: "Second"}}
		if !reflect.DeepEqual(completion.Citations, want) || len(completion.ToolCalls) != 0 {
			t.Fatalf("unsafe, missing, or duplicate tool sources: %+v", completion)
		}
		body, _, err := RequestBody(Request{Model: "fixture", Messages: []Message{completion.AssistantMessage()}}, Model{Endpoint: "/responses"})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(body)
		if strings.Contains(string(encoded), "vendor_extension") || strings.Contains(string(encoded), `"title"`) {
			t.Fatalf("output-only source fields leaked into input: %s", encoded)
		}
	}
}

func TestReplayProtocolGuardAndLegacyThinking(t *testing.T) {
	message := Message{Role: "assistant", Content: "normalized", ToolCalls: []ToolCall{{ID: "c", Name: "lookup", Arguments: `{}`}}, ProviderData: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"old","signature":"legacy-signature"}`)}, Replay: &Replay{Protocol: "/responses", Blocks: rawBlocks(t, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"foreign"}]}]`)}}
	body, _, err := RequestBody(Request{Model: "fixture", Messages: []Message{message}}, Model{Endpoint: "/v1/messages"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "foreign") || !strings.Contains(string(encoded), "legacy-signature") || !strings.Contains(string(encoded), "normalized") {
		t.Fatalf("protocol guard discarded legacy history: %s", encoded)
	}
	message.Replay = &Replay{Protocol: "/v1/messages", Blocks: []json.RawMessage{json.RawMessage(`{"type":"unknown"}`)}}
	if _, _, err := RequestBody(Request{Model: "fixture", Messages: []Message{message}}, Model{Endpoint: "/v1/messages"}); err == nil {
		t.Fatal("unknown authoritative replay silently accepted")
	}
}
