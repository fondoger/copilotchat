package localproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestToolDiscoveryThroughGateway(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("native=%v/stream=%v", native, streaming), func(t *testing.T) {
				calls := make(chan provider.RawRequest, 3)
				step := 0
				_, base := fixture(t, &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
					calls <- r
					src := parsed(t, string(r.Body))
					tools := array(src["tools"])
					nameKey := "name"
					// Every target gets only the schema for search initially, then discovered
					// schemas on subsequent requests, never a native tool_search declaration.
					if step == 0 && len(tools) != 1 || step > 0 && len(tools) != 3 {
						t.Errorf("unexpected loaded tools: %s", r.Body)
					}
					var item object
					args := object{"query": "customer"}
					name := text(obj(tools[0])[nameKey])
					callID := "search1"
					if step > 0 {
						args = object{"customer": "42"}
						name = text(obj(tools[1])[nameKey])
						callID = "lookup1"
					}
					if native {
						if r.Endpoint != "/responses" {
							t.Errorf("wrong native endpoint: %s", r.Endpoint)
						}
						encoded, _ := json.Marshal(args)
						item = object{"type": "function_call", "id": "fc_" + callID, "call_id": callID, "name": name, "arguments": string(encoded), "status": "completed"}
					} else {
						if r.Endpoint != "/v1/messages" {
							t.Errorf("wrong bridge endpoint: %s", r.Endpoint)
						}
						item = object{"type": "tool_use", "id": callID, "name": name, "input": args}
					}
					step++
					result := object{"id": "r", "object": "response", "status": "completed", "output": []any{item}}
					if !native {
						result = object{"id": "r", "type": "message", "role": "assistant", "content": []any{item}, "stop_reason": "tool_use", "usage": object{}}
					}
					data, _ := json.Marshal(result)
					contentType := "application/json"
					if streaming {
						contentType = "text/event-stream"
						events := []object{}
						if native {
							initial := cloneToolObject(item)
							initial["arguments"], initial["status"] = "", "in_progress"
							events = []object{
								{"type": "response.output_item.added", "output_index": 0, "item": initial},
								{"type": "response.function_call_arguments.delta", "item_id": item["id"], "output_index": 0, "delta": item["arguments"]},
								{"type": "response.function_call_arguments.done", "item_id": item["id"], "output_index": 0, "arguments": item["arguments"]},
								{"type": "response.output_item.done", "output_index": 0, "item": item},
								{"type": "response.completed", "response": result},
							}
						} else {
							encoded, _ := json.Marshal(args)
							initial := cloneToolObject(item)
							initial["input"] = object{}
							events = []object{
								{"type": "message_start", "message": object{"id": "r", "type": "message", "role": "assistant", "content": []any{}, "usage": object{}}},
								{"type": "content_block_start", "index": 0, "content_block": initial},
								{"type": "content_block_delta", "index": 0, "delta": object{"type": "input_json_delta", "partial_json": string(encoded)}},
								{"type": "content_block_stop", "index": 0},
								{"type": "message_delta", "delta": object{"stop_reason": "tool_use"}, "usage": object{}},
								{"type": "message_stop"},
							}
						}
						var stream strings.Builder
						for _, e := range events {
							b, _ := json.Marshal(e)
							stream.WriteString(sse(string(b)))
						}
						data = []byte(stream.String())
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				}})
				model := "claude-fixture"
				if native {
					model = "gpt-fixture"
				}
				history := []any{object{"role": "user", "content": "Find a customer"}}
				body := object{"model": model, "input": history, "stream": streaming, "tools": []any{discoveryTool()}}
				exchange := func() object {
					data, _ := json.Marshal(body)
					res := send(t, "POST", base+"/responses", string(data), map[string]string{"Authorization": "Bearer fixture-key"})
					reply, _ := io.ReadAll(res.Body)
					if res.StatusCode != 200 {
						t.Fatal(res.Status, string(reply))
					}
					if !streaming {
						return parsed(t, string(reply))
					}
					var terminal object
					if err := readResponseEvents(strings.NewReader(string(reply)), func(e object) error {
						if e["type"] == "response.completed" {
							terminal = obj(e["response"])
						}
						if e["type"] == "error" || e["type"] == "response.failed" {
							t.Fatal(e)
						}
						return nil
					}); err != nil || terminal == nil {
						t.Fatal(err, string(reply))
					}
					return terminal
				}
				first := exchange()
				<-calls
				search := obj(array(first["output"])[0])
				if search["type"] != "tool_search_call" || obj(search["arguments"])["query"] != "customer" {
					t.Fatal(first)
				}
				history = append(history, array(first["output"])...)
				history = append(history, object{"type": "tool_search_output", "execution": "client", "call_id": "search1", "tools": discoveredTools()})
				body["input"] = history
				second := exchange()
				request := <-calls
				if !request.AgentInitiated {
					t.Fatal("search results lost continuation classification")
				}
				lookup := obj(array(second["output"])[0])
				if lookup["type"] != "function_call" || lookup["namespace"] != "crm" || lookup["name"] != "lookup" {
					t.Fatal(second)
				}
				history = append(history, array(second["output"])...)
				history = append(history, object{"type": "function_call_output", "call_id": "lookup1", "output": "found"})
				body["input"] = history
				exchange()
				<-calls
			})
		}
	}
}

func TestToolDiscoveryAuxiliaryHistoryAndNativeMessages(t *testing.T) {
	calls := make(chan provider.RawRequest, 3)
	_, base := fixture(t, &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
		calls <- r
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":5}`))}, nil
	}})
	headers := map[string]string{"Authorization": "Bearer fixture-key", "Anthropic-Beta": "advanced-tool-use-2025-11-20"}
	for _, endpoint := range []string{"/responses/compact", "/responses/input_tokens"} {
		body := object{"model": "gpt-fixture", "input": searchHistory()}
		res := send(t, "POST", base+endpoint, string(encodedDiscovery(t, body)), headers)
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatal(res.Status, string(data))
		}
		req := <-calls
		src := parsed(t, string(req.Body))
		if req.Endpoint != endpoint || src["tools"] != nil || obj(array(src["input"])[1])["type"] != "function_call" {
			t.Fatal(req.Endpoint, string(req.Body))
		}
	}
	native := `{"model":"claude-fixture","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"s","content":[{"type":"tool_reference","tool_name":"read"}]}]}],"tools":[{"name":"read","input_schema":{"type":"object"},"defer_loading":true}]}`
	res := send(t, "POST", base+"/v1/messages", native, headers)
	io.Copy(io.Discard, res.Body)
	req := <-calls
	if string(req.Body) != native || req.Header.Get("Anthropic-Beta") != headers["Anthropic-Beta"] {
		t.Fatal("native Messages discovery was rewritten", string(req.Body))
	}
}
