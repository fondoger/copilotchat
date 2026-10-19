package provider

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCacheKeysDoNotDependOnCurrentPrompt(t *testing.T) {
	model := Model{Endpoint: "/responses"}
	request := Request{Model: "model", ConversationID: "conversation", Messages: []Message{{Role: "user", Content: "hello"}}}
	first, _, err := RequestBody(request, model)
	if err != nil {
		t.Fatal(err)
	}
	request.Messages = append(request.Messages, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "call", Name: "lookup", Arguments: `{}`}}}, Message{Role: "tool", ToolCallID: "call", Content: "result"})
	second, _, err := RequestBody(request, model)
	if err != nil || first["prompt_cache_key"] != second["prompt_cache_key"] {
		t.Fatalf("tool continuation changed cache affinity: %v", err)
	}
	request.ConversationID = "other"
	other, _, _ := RequestBody(request, model)
	if first["prompt_cache_key"] == other["prompt_cache_key"] {
		t.Fatal("unrelated conversations share a cache routing key")
	}
	request.ConversationID = ""
	missing, _, _ := RequestBody(request, model)
	if _, exists := missing["prompt_cache_key"]; exists {
		t.Fatal("missing conversation identity produced an arbitrary cache key")
	}
}

func TestAnthropicCacheBreakpointsAndHistoryNormalization(t *testing.T) {
	signature := json.RawMessage(`{"type":"thinking","thinking":"Visible reasoning","signature":"opaque-do-not-change"}`)
	request := Request{Model: "model", Messages: []Message{
		{Role: "system", Content: "Base instructions"},
		{Role: "system", Content: "Middle instructions"},
		{Role: "system", Content: "Project instructions"},
		{Role: "user", Content: "Question"},
		{Role: "assistant"}, // A stopped, empty reply must not introduce a new turn.
		{Role: "assistant", ProviderData: []json.RawMessage{signature}, ToolCalls: []ToolCall{{ID: "a", Name: "lookup", Arguments: `{}`}, {ID: "b", Name: "lookup", Arguments: `{}`}}},
		{Role: "tool", ToolCallID: "a", Content: "First result"},
		{Role: "tool", ToolCallID: "b", Content: "Second result"},
	}, Tools: []Tool{{Name: "z_tool"}, {Name: "a_tool"}}}
	before, _ := json.Marshal(request)
	body, _, err := RequestBody(request, Model{Endpoint: "/v1/messages"})
	if err != nil {
		t.Fatal(err)
	}
	if countCacheMarkers(body) != 4 {
		t.Fatalf("wrong cache breakpoint count: %#v", body)
	}
	systems := body["system"].([]any)
	if systems[0].(map[string]any)["cache_control"] == nil || systems[1].(map[string]any)["cache_control"] != nil || systems[2].(map[string]any)["cache_control"] == nil {
		t.Fatal("cache did not preserve first/last system boundaries")
	}
	tools := body["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "a_tool" || tools[0].(map[string]any)["cache_control"] != nil || tools[1].(map[string]any)["cache_control"] == nil {
		t.Fatal("tool prefix was not canonicalized and marked once")
	}
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("empty reply or separate tool results changed normalized turns: %#v", messages)
	}
	parts := messages[1].(map[string]any)["content"].([]any)
	if parts[0].(map[string]any)["signature"] != "opaque-do-not-change" || parts[0].(map[string]any)["cache_control"] != nil {
		t.Fatal("cache normalization modified signed thinking")
	}
	results := messages[2].(map[string]any)["content"].([]any)
	if len(results) != 2 || results[0].(map[string]any)["cache_control"] != nil || results[1].(map[string]any)["cache_control"] == nil {
		t.Fatal("tool result tail cache boundary is not on the final result")
	}
	after, _ := json.Marshal(request)
	if string(before) != string(after) {
		t.Fatal("cache normalization mutated stored history or tool definitions")
	}
}

func TestCacheDoesNotMarkOpaqueThinkingOrInventUsage(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"thinking","thinking":"Visible","signature":"opaque"}`),
		json.RawMessage(`{"type":"redacted_thinking","data":"encrypted"}`),
	} {
		request := Request{Model: "model", Messages: []Message{{Role: "user", Content: "question"}, {Role: "assistant", ProviderData: []json.RawMessage{raw}}}}
		body, _, err := RequestBody(request, Model{Endpoint: "/v1/messages"})
		if err != nil {
			t.Fatal(err)
		}
		messages := body["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)["content"].([]any)[0].(map[string]any)
		if last["cache_control"] != nil || countCacheMarkers(body) != 1 {
			t.Fatal("invalid cache marker was attached to opaque reasoning")
		}
	}
	parser := streamParser{}
	if err := parser.chat([]byte(`{"choices":[],"usage":{"prompt_tokens":1500,"completion_tokens":2}}`)); err != nil {
		t.Fatal(err)
	}
	if parser.completion.Usage.CachedInputAvailable || parser.completion.Usage.CachedInputTokens != 0 {
		t.Fatal("cache setup invented cache hits when the service omitted them")
	}
}

func TestToolOrderDoesNotChangeReusablePrefix(t *testing.T) {
	for _, endpoint := range []string{"/responses", "/chat/completions", "/v1/messages"} {
		request := Request{Model: "model", Messages: []Message{{Role: "user", Content: "question"}}, Tools: []Tool{{Name: "z", Description: "Last"}, {Name: "a", Description: "First"}}}
		first, _, _ := RequestBody(request, Model{Endpoint: endpoint})
		request.Tools[0], request.Tools[1] = request.Tools[1], request.Tools[0]
		second, _, _ := RequestBody(request, Model{Endpoint: endpoint})
		if !reflect.DeepEqual(first["tools"], second["tools"]) {
			t.Errorf("tool enumeration order changed %s prefix", endpoint)
		}
	}
}

func countCacheMarkers(value any) int {
	count := 0
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "cache_control" {
				count++
			} else {
				count += countCacheMarkers(child)
			}
		}
	case []any:
		for _, child := range value {
			count += countCacheMarkers(child)
		}
	}
	return count
}

func withoutMarkers(value any) {
	switch value := value.(type) {
	case map[string]any:
		delete(value, "cache_control")
		for _, child := range value {
			withoutMarkers(child)
		}
	case []any:
		for _, child := range value {
			withoutMarkers(child)
		}
	}
}
