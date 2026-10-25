package localproxy

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func discoveryTool() object {
	return object{"type": "tool_search", "execution": "client", "description": "Find tools in available sources", "parameters": object{"type": "object", "properties": object{"query": object{"type": "string"}}, "required": []any{"query"}}}
}

func discoveredTools() []any {
	return []any{object{"type": "namespace", "name": "crm", "tools": []any{
		object{"type": "function", "name": "lookup", "defer_loading": true, "description": "Find a customer", "parameters": object{"type": "object", "properties": object{"customer": object{"type": "string"}}}},
		object{"type": "custom", "name": "patch", "defer_loading": true, "format": object{"type": "text"}},
	}}}
}

func searchHistory() []any {
	return []any{object{"role": "user", "content": "Find a customer"},
		object{"type": "tool_search_call", "execution": "client", "call_id": "search1", "arguments": object{"query": "customer"}},
		object{"type": "tool_search_output", "execution": "client", "call_id": "search1", "status": "completed", "tools": discoveredTools()},
	}
}

func encodedDiscovery(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCodexDiscoveryLoadingAndNamespace(t *testing.T) {
	src := object{"input": "hello", "tools": []any{discoveryTool(), object{"type": "function", "name": "lookup", "parameters": object{"type": "object"}}, object{"type": "namespace", "name": "crm", "tools": []any{object{"type": "function", "name": "hidden", "defer_loading": true, "parameters": object{"type": "object"}}}}}}
	c, err := normalizeResponsesTools(src, toolSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(array(src["tools"])) != 2 {
		t.Fatalf("initial schemas leaked: %v", src["tools"])
	}
	search := obj(array(src["tools"])[0])
	if search["type"] != "function" || search["description"] != discoveryTool()["description"] || search["execution"] != nil {
		t.Fatal(search)
	}
	call := object{"type": "function_call", "id": "fc_search", "call_id": "search1", "name": search["name"], "arguments": `{"query":"customer"}`, "status": "completed"}
	if err := c.restoreOutputItem(call); err != nil {
		t.Fatal(err)
	}
	if call["type"] != "tool_search_call" || call["id"] != "tsc_search" || call["call_id"] != "search1" || obj(call["arguments"])["query"] != "customer" {
		t.Fatal(call)
	}

	src = object{"input": searchHistory(), "tools": []any{discoveryTool(), object{"type": "function", "name": "lookup", "parameters": object{"type": "object"}}}}
	c, err = normalizeResponsesTools(src, toolSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tools := array(src["tools"])
	if len(tools) != 4 {
		t.Fatalf("search did not load schemas: %v", tools)
	}
	alias := text(obj(tools[2])["name"])
	if alias == "lookup" || obj(tools[2])["defer_loading"] != nil {
		t.Fatal(tools)
	}
	result := obj(array(src["input"])[2])
	if result["type"] != "function_call_output" || result["call_id"] != "search1" {
		t.Fatal(result)
	}
	namespaced := object{"type": "function_call", "name": alias, "arguments": "{}"}
	if err := c.restoreOutputItem(namespaced); err != nil {
		t.Fatal(err)
	}
	if namespaced["namespace"] != "crm" || namespaced["name"] != "lookup" {
		t.Fatal(namespaced)
	}
}

func TestCodexDiscoveryHistoryWithoutDeclaration(t *testing.T) {
	for _, withTools := range []bool{false, true} {
		src := object{"input": searchHistory()}
		if withTools {
			src["tools"] = []any{}
		}
		c, err := normalizeResponsesTools(src, toolSearchOptions{})
		if err != nil || c == nil {
			t.Fatal(c, err)
		}
		if len(array(src["tools"])) != 0 {
			t.Fatal("resurrected historical schemas", src)
		}
		if obj(array(src["input"])[1])["type"] != "function_call" || obj(array(src["input"])[2])["type"] != "function_call_output" {
			t.Fatal(src)
		}
	}
}

func TestCodexDiscoveryRejectsInvalidHistory(t *testing.T) {
	cases := []string{
		`{"input":[],"tools":[{"type":"tool_search","parameters":{}}]}`,
		`{"input":[{"type":"tool_search_output","execution":"client","call_id":"missing","tools":[]}]}`,
		`{"input":[{"type":"tool_search_call","execution":"client","call_id":"s","arguments":{}}]}`,
		`{"input":[{"type":"tool_search_call","execution":"client","call_id":"s","arguments":"{}"}]}`,
		`{"input":[],"tools":[{"type":"namespace","name":"n","tools":[{"type":"namespace","name":"n2","tools":[]}]}]}`,
		`{"input":[],"tools":[{"type":"tool_search","execution":"client","parameters":{}}],"previous_response_id":"r"}`,
	}
	for _, input := range cases {
		src := parsed(t, input)
		if _, err := normalizeResponsesTools(src, toolSearchOptions{}); err == nil {
			t.Fatal("accepted invalid discovery", input)
		}
	}
}

func TestCodexDiscoveryMessagesReplay(t *testing.T) {
	model := provider.Model{ID: "claude-test", Tools: true}
	input := []any{object{"role": "user", "content": "Find a customer"}}
	request := object{"input": input, "tools": []any{discoveryTool()}}
	body, _, bridge, err := responsesToMessages(encodedDiscovery(t, request), model)
	if err != nil {
		t.Fatal(err)
	}
	searchAlias := obj(array(parsed(t, string(body))["tools"])[0])["name"]
	firstMessage := object{"id": "s", "type": "message", "role": "assistant", "stop_reason": "tool_use", "content": []any{
		object{"type": "thinking", "thinking": "search", "signature": "untouched"},
		object{"type": "tool_use", "id": "search1", "name": searchAlias, "input": object{"query": "customer"}},
	}}
	first, err := messagesToResponse(firstMessage, "model", bridge)
	if err != nil {
		t.Fatal(err)
	}
	if obj(array(first["output"])[0])["type"] != "tool_search_call" {
		t.Fatal(first)
	}
	input = append(input, array(first["output"])...)
	input = append(input, object{"type": "tool_search_output", "execution": "client", "call_id": "search1", "tools": discoveredTools()})
	request["input"] = input
	body, _, bridge, err = responsesToMessages(encodedDiscovery(t, request), model)
	if err != nil {
		t.Fatal(err)
	}
	sent := parsed(t, string(body))
	messages := array(sent["messages"])
	if !reflect.DeepEqual(obj(messages[1])["content"], firstMessage["content"]) {
		t.Fatal("signed search turn changed", messages)
	}
	alias := obj(array(sent["tools"])[1])["name"]
	secondMessage := object{"id": "f", "type": "message", "role": "assistant", "stop_reason": "tool_use", "content": []any{object{"type": "tool_use", "id": "lookup1", "name": alias, "input": object{"customer": "42"}}}}
	second, err := messagesToResponse(secondMessage, "model", bridge)
	if err != nil {
		t.Fatal(err)
	}
	item := obj(array(second["output"])[0])
	if item["namespace"] != "crm" || item["name"] != "lookup" {
		t.Fatal(second)
	}
	input = append(input, array(second["output"])...)
	input = append(input, object{"type": "function_call_output", "call_id": "lookup1", "output": "found"})
	request["input"] = input
	if _, _, _, err := responsesToMessages(encodedDiscovery(t, request), model); err != nil {
		t.Fatal("replay failed", err)
	}
	// The namespace is part of the match key, not merely presentation metadata.
	item["namespace"] = "other"
	if _, _, _, err := responsesToMessages(encodedDiscovery(t, request), model); err == nil {
		t.Fatal("accepted tampered namespace")
	}
}

func TestCodexDiscoveryChoiceAndLatestSchema(t *testing.T) {
	history := searchHistory()
	updated := discoveredTools()
	function := obj(array(obj(updated[0])["tools"])[0])
	function["parameters"] = object{"type": "object", "required": []any{"customer_id"}}
	history = append(history,
		object{"type": "tool_search_call", "execution": "client", "call_id": "search2", "arguments": object{"query": "customer"}},
		object{"type": "tool_search_output", "execution": "client", "call_id": "search2", "tools": updated})
	for _, allowed := range []bool{false, true} {
		choice := object{"type": "function", "namespace": "crm", "name": "lookup"}
		if allowed {
			choice = object{"type": "allowed_tools", "mode": "auto", "tools": []any{choice}}
		}
		src := object{"input": history, "tools": []any{discoveryTool()}, "tool_choice": choice}
		_, err := normalizeResponsesTools(src, toolSearchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		loaded := obj(array(src["tools"])[1])
		if !reflect.DeepEqual(loaded["parameters"], function["parameters"]) {
			t.Fatal("stale schema survived", src)
		}
		mapped := obj(src["tool_choice"])
		if allowed {
			mapped = obj(array(mapped["tools"])[0])
		}
		if mapped["name"] != loaded["name"] || mapped["namespace"] != nil {
			t.Fatal("choice alias mismatch", src)
		}
	}
	src := object{"input": "hi", "tools": []any{discoveryTool()}, "conversation": "conv1"}
	if _, err := normalizeResponsesTools(src, toolSearchOptions{}); err == nil {
		t.Fatal("accepted stateful conversation")
	}
	src = object{"input": "hi", "tools": []any{discoveryTool(), discoveredTools()[0]}, "tool_choice": object{"type": "function", "namespace": "crm", "name": "lookup"}}
	if _, err := normalizeResponsesTools(src, toolSearchOptions{}); err == nil {
		t.Fatal("selected undiscovered deferred tool")
	}
}

func TestCodexDiscoveryNamespacedCustomAndSearchCollision(t *testing.T) {
	longNamespace := strings.Repeat("source", 20)
	namespace := func(name string) object {
		return object{"type": "namespace", "name": name, "tools": []any{
			object{"type": "function", "name": "lookup", "parameters": object{"type": "object"}},
			object{"type": "custom", "name": "patch", "format": object{"type": "text"}},
		}}
	}
	src := object{"input": "hi", "tools": []any{discoveryTool(), namespace("a"), namespace(longNamespace), object{"type": "function", "name": "tool_search", "parameters": object{"type": "object"}}},
		"tool_choice": object{"type": "custom", "namespace": longNamespace, "name": "patch"}}
	c, err := normalizeResponsesTools(src, toolSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aliases := map[string]bool{}
	for _, v := range array(src["tools"]) {
		name := text(obj(v)["name"])
		if aliases[name] || len(name) > 64 {
			t.Fatal("invalid flattened identity", src)
		}
		aliases[name] = true
	}
	choice := obj(src["tool_choice"])
	item := object{"type": "custom_tool_call", "name": choice["name"], "input": "patch text"}
	if err := c.restoreOutputItem(item); err != nil {
		t.Fatal(err)
	}
	if item["name"] != "patch" || item["namespace"] != longNamespace || item["input"] != "patch text" {
		t.Fatal(item)
	}
	ordinary := object{"type": "function_call", "name": "tool_search", "arguments": "{}"}
	if err := c.restoreOutputItem(ordinary); err != nil {
		t.Fatal(err)
	}
	if ordinary["type"] != "function_call" {
		t.Fatal("user function became discovery", ordinary)
	}
}

func TestCodexDiscoveryMessagesSSE(t *testing.T) {
	for _, upstreamStream := range []bool{false, true} {
		t.Run(fmt.Sprint(upstreamStream), func(t *testing.T) {
			_, _, bridge, err := responsesToMessages(encodedDiscovery(t, object{"input": "hi", "tools": []any{discoveryTool()}}), provider.Model{ID: "claude-test"})
			if err != nil {
				t.Fatal(err)
			}
			alias := bridge.tools.names[searchIdentity()]
			message := object{"id": "s", "type": "message", "role": "assistant", "stop_reason": "tool_use", "content": []any{object{"type": "tool_use", "id": "search1", "name": alias, "input": object{"query": "customer"}}}}
			w := httptest.NewRecorder()
			if upstreamStream {
				s := newResponsesStream(w, "model", bridge, true)
				frames := []object{
					{"type": "message_start", "message": object{"id": "s", "type": "message", "role": "assistant", "content": []any{}, "usage": object{}}},
					{"type": "content_block_start", "index": json.Number("0"), "content_block": object{"type": "tool_use", "id": "search1", "name": alias, "input": object{}}},
					{"type": "content_block_delta", "index": json.Number("0"), "delta": object{"type": "input_json_delta", "partial_json": `{"query":"customer"}`}},
					{"type": "content_block_stop", "index": json.Number("0")},
					{"type": "message_delta", "delta": object{"stop_reason": "tool_use"}, "usage": object{}},
					{"type": "message_stop"},
				}
				for _, frame := range frames {
					if err := s.event(frame); err != nil && err != errResponseFinished {
						t.Fatal(err)
					}
				}
			} else {
				response, err := messagesToResponse(message, "model", bridge)
				if err != nil {
					t.Fatal(err)
				}
				s := newResponsesStream(w, "model", bridge, true)
				if err := s.complete(response); err != nil {
					t.Fatal(err)
				}
			}
			var call object
			if err := readResponseEvents(strings.NewReader(w.Body.String()), func(e object) error {
				if strings.HasPrefix(text(e["type"]), "response.function_call_arguments") {
					t.Fatal("search leaked ordinary arguments event", e)
				}
				if e["type"] == "response.output_item.done" && obj(e["item"])["type"] == "tool_search_call" {
					call = obj(e["item"])
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if call == nil || obj(call["arguments"])["query"] != "customer" || call["call_id"] != "search1" {
				t.Fatal(w.Body.String())
			}
		})
	}
}
