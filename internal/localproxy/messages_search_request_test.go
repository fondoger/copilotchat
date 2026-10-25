package localproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func claudeSearchTool() object {
	return object{"name": "ToolSearch", "description": "Find deferred tools", "input_schema": object{"type": "object", "properties": object{"query": object{"type": "string"}, "max_results": object{"type": "number"}}, "required": []any{"query"}}}
}
func claudeSearchRequest() object {
	return object{"tools": []any{claudeSearchTool(), object{"name": "read", "defer_loading": true, "input_schema": object{"type": "object", "properties": object{"n": object{"const": json.Number("9007199254740993")}}}}}, "messages": []any{object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "s", "name": "ToolSearch", "input": object{"query": "read", "max_results": 5}}}}, object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "s", "content": []any{object{"type": "tool_reference", "tool_name": "read"}}}}}}}
}
func TestMessagesNativeSearchReplay(t *testing.T) {
	for _, id := range []string{"vendor/GPT-model", "grok-4.7"} {
		request := claudeSearchRequest()
		model := provider.Model{ID: id}
		convert := func() object {
			data, _, err := messagesToResponses(encodedDiscovery(t, request), model)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "additional_tools") {
				t.Fatal(string(data))
			}
			return parsed(t, string(data))
		}
		first := convert()
		input := array(first["input"])
		if id == "grok-4.7" {
			if len(array(first["tools"])) != 2 || obj(input[1])["type"] != "function_call_output" {
				t.Fatal(first)
			}
			continue
		}
		output := obj(input[1])
		tools := array(output["tools"])
		if len(array(first["tools"])) != 1 || obj(array(first["tools"])[0])["type"] != "tool_search" || obj(input[0])["type"] != "tool_search_call" || obj(input[0])["execution"] != "client" || output["type"] != "tool_search_output" || len(tools) != 1 {
			t.Fatal(first)
		}
		if !reflect.DeepEqual(obj(tools[0])["parameters"], obj(array(request["tools"])[1])["input_schema"]) {
			t.Fatal(first)
		}
		delete(obj(array(request["tools"])[1]), "defer_loading")
		request["messages"] = append(array(request["messages"]), object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "r", "name": "read", "input": object{}}}}, object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "r", "content": "ok"}}})
		next := convert()
		if !reflect.DeepEqual(first["tools"], next["tools"]) || !reflect.DeepEqual(input, array(next["input"])[:len(input)]) {
			t.Fatal("prefix changed", next)
		}
	}
}
func TestCodexNativeSearchPassThrough(t *testing.T) {
	src := object{"tools": []any{discoveryTool()}, "input": searchHistory(), "tool_choice": object{"type": "function", "namespace": "crm", "name": "lookup"}}
	before := copyMessagesObject(src)
	c, err := normalizeResponsesTools(src, toolSearchOptions{native: true})
	if err != nil || c != nil || !reflect.DeepEqual(src, before) {
		t.Fatal(src, c, err)
	}
}
func TestNativeSearchUsesUpstreamModelID(t *testing.T) {
	for _, test := range []struct {
		public, upstream string
		native           bool
	}{{"opaque-alias", "vendor/GPT-native", true}, {"gpt-alias", "grok-4.7", false}} {
		t.Run(test.public, func(t *testing.T) {
			requests := make(chan provider.RawRequest, 2)
			client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
				requests <- r
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"r","object":"response","status":"completed","output":[]}`))}, nil
			}}
			s, base := fixture(t, client)
			s.Update(map[string]Target{"p": {Client: client}}, map[string]Profile{"fixture": {ID: "fixture-id", Models: []Model{{ID: test.public, ProviderID: "p", Model: provider.Model{ID: test.upstream, Endpoint: "/responses"}}}}})
			for _, path := range []string{"/responses", "/v1/messages"} {
				body := object{"model": test.public, "tools": []any{discoveryTool()}, "input": searchHistory()}
				if path == "/v1/messages" {
					body = claudeSearchRequest()
					body["model"] = test.public
				}
				res := send(t, "POST", base+path, string(encodedDiscovery(t, body)), map[string]string{"Authorization": "Bearer fixture-key"})
				reply, _ := io.ReadAll(res.Body)
				if res.StatusCode != 200 {
					t.Fatal(res.Status, string(reply))
				}
				raw := <-requests
				src := parsed(t, string(raw.Body))
				native := obj(array(src["tools"])[0])["type"] == "tool_search"
				if native != test.native || src["model"] != test.upstream || strings.Contains(string(raw.Body), "additional_tools") {
					t.Fatal(string(raw.Body))
				}
			}
		})
	}
}
func TestMessagesHostedSearchCatalog(t *testing.T) {
	for _, kind := range []string{"tool_search_tool_regex_20251119", "tool_search_tool_bm25_20251119"} {
		name := strings.TrimSuffix(kind, "_20251119")
		request := object{"tools": []any{object{"type": kind, "name": name}, object{"name": "read", "defer_loading": true, "input_schema": object{"type": "object"}}}, "messages": []any{object{"role": "user", "content": "find read"}}}
		data, _, err := messagesToResponses(encodedDiscovery(t, request), provider.Model{ID: "gpt-test"})
		if err != nil {
			t.Fatal(err)
		}
		src := parsed(t, string(data))
		tools := array(src["tools"])
		if len(tools) != 2 || obj(tools[0])["type"] != "tool_search" || obj(tools[0])["execution"] != "server" || obj(tools[1])["defer_loading"] != true {
			t.Fatal(src)
		}
		if _, _, err := messagesToResponses(encodedDiscovery(t, request), provider.Model{ID: "grok-4.7"}); err == nil {
			t.Fatal("hosted non-GPT accepted")
		}
	}
}
func TestMessagesHostedSearchRequestReplay(t *testing.T) {
	response := searchResponseFixture(t, "server")
	context := &messagesSearchContext{mode: "server", name: "tool_search_tool_regex"}
	message, err := responsesToMessage(response, "public", context)
	if err != nil {
		t.Fatal(err)
	}
	request := object{"tools": []any{object{"type": "tool_search_tool_regex_20251119", "name": context.name}, object{"name": "read", "defer_loading": true, "input_schema": object{"type": "object"}}}, "messages": []any{message, object{"role": "user", "content": "continue"}}}
	data, _, err := messagesToResponses(encodedDiscovery(t, request), provider.Model{ID: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	input := array(parsed(t, string(data))["input"])
	if !reflect.DeepEqual(input[:2], response["output"]) {
		t.Fatal("hosted replay changed", string(data))
	}
	obj(array(message["content"])[1])["name"] = "other"
	if _, _, err := messagesToResponses(encodedDiscovery(t, request), provider.Model{ID: "gpt-test"}); err == nil {
		t.Fatal("tampered replay accepted")
	}
}

func TestMessagesSearchContractAndBoundaries(t *testing.T) {
	model := provider.Model{ID: "gpt-test"}
	request := claudeSearchRequest()
	request["messages"] = []any{object{"role": "user", "content": "find read"}}
	data, _, err := messagesToResponses(encodedDiscovery(t, request), model)
	if err != nil {
		t.Fatal(err)
	}
	sent := parsed(t, string(data))
	tools := array(sent["tools"])
	if len(tools) != 1 || obj(tools[0])["execution"] != "client" || !reflect.DeepEqual(obj(tools[0])["parameters"], claudeSearchTool()["input_schema"]) {
		t.Fatal(sent)
	}
	for _, mutate := range []func(object){
		func(r object) {
			r["tools"] = append(array(r["tools"]), object{"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"})
		},
		func(r object) {
			obj(array(obj(array(r["messages"])[1])["content"])[0])["content"] = []any{object{"type": "text", "text": "mixed"}, object{"type": "tool_reference", "tool_name": "read"}}
		},
		func(r object) { r["messages"] = array(r["messages"])[:1] },
		func(r object) { obj(array(obj(array(r["messages"])[1])["content"])[0])["tool_use_id"] = "missing" },
	} {
		r := claudeSearchRequest()
		mutate(r)
		if _, _, err := messagesToResponses(encodedDiscovery(t, r), model); err == nil {
			t.Fatal("accepted incompatible search", r)
		}
	}
}

func TestMessagesNativeEmptyAndInvalidSearch(t *testing.T) {
	request := claudeSearchRequest()
	result := obj(array(obj(array(request["messages"])[1])["content"])[0])
	result["content"] = "No matching deferred tools found. Some MCP servers are still connecting."
	data, _, err := messagesToResponses(encodedDiscovery(t, request), provider.Model{ID: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	src := parsed(t, string(data))
	input := array(src["input"])
	if len(array(obj(input[1])["tools"])) != 0 || obj(input[2])["role"] != "user" {
		t.Fatal(src)
	}
	result["is_error"] = true
	if _, _, err := messagesToResponses(encodedDiscovery(t, request), provider.Model{ID: "gpt-test"}); err == nil {
		t.Fatal("failed search accepted")
	}
}
