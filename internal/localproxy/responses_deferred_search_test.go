package localproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func deferredSearchReply(t *testing.T, message object, stream bool) *http.Response {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	contentType := "application/json"
	if stream {
		start := reverseStart()
		start["message"] = object{"id": message["id"], "type": "message", "role": "assistant", "content": []any{}, "usage": message["usage"]}
		events := []object{start}
		for i, block := range array(message["content"]) {
			events = append(events, reverseBlockStart(i, obj(block)), reverseBlockStop(i))
		}
		events = append(events, reverseMessageDelta(text(message["stop_reason"])), reverseMessageStop())
		data = []byte(reverseSSE(events...))
		contentType = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(string(data)))}
}

func readDeferredResponse(t *testing.T, res *http.Response, stream bool) object {
	t.Helper()
	data, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 {
		t.Fatal(res.Status, err, string(data))
	}
	if !stream {
		return parsed(t, string(data))
	}
	events := reverseReadEvents(t, string(data))
	if events[len(events)-1]["type"] != "response.completed" {
		t.Fatal(string(data))
	}
	return reverseFinal(t, events)
}

func TestResponsesPausedSearchCompletesWithoutResubmitting(t *testing.T) {
	message := parsed(t, `{"id":"msg_pause","type":"message","role":"assistant","stop_reason":"pause_turn","content":[{"type":"server_tool_use","id":"srv_pause","name":"web_search","input":{"query":"example"}},{"type":"text","text":"Searching"}],"usage":{}}`)
	for _, upstreamStream := range []bool{false, true} {
		for _, clientStream := range []bool{false, true} {
			t.Run(fmt.Sprintf("upstream=%v/client=%v", upstreamStream, clientStream), func(t *testing.T) {
				var calls atomic.Int32
				_, base := fixture(t, &rawClient{fn: func(_ context.Context, _ provider.RawRequest) (*http.Response, error) {
					calls.Add(1)
					return deferredSearchReply(t, message, upstreamStream), nil
				}})
				request := object{"model": "claude-fixture", "input": "search", "stream": clientStream, "tools": []any{object{"type": "web_search"}}}
				body, _ := json.Marshal(request)
				reply := readDeferredResponse(t, send(t, "POST", base+"/responses", string(body), map[string]string{"Authorization": "Bearer fixture-key"}), clientStream)
				if reply["status"] != "completed" || reply["error"] != nil || reply["incomplete_details"] != nil || calls.Load() != 1 {
					t.Fatal(reply, calls.Load())
				}
				output := array(reply["output"])
				if text(obj(array(obj(output[1])["content"])[0])["text"]) != "Searching" || obj(output[0])["status"] != "in_progress" {
					t.Fatal("partial output or search state misreported", reply)
				}
				replay, err := restoreAnthropicInput(output)
				if err != nil || len(replay) != 1 || !reflect.DeepEqual(obj(replay[0])["content"], message["content"]) {
					t.Fatal("paused state lost", replay, err)
				}
			})
		}
	}
}

func TestResponsesDeferredSearchAcrossClientToolAndFollowUp(t *testing.T) {
	first := parsed(t, `{"id":"msg_mixed","type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"server_tool_use","id":"srv_deferred","name":"web_search","input":{"query":"example"}},{"type":"tool_use","id":"read_1","name":"read","input":{}}],"usage":{}}`)
	second := parsed(t, `{"id":"msg_resumed","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"web_search_tool_result","tool_use_id":"srv_deferred","content":[{"type":"web_search_result","url":"https://example.com","title":"Example","encrypted_content":"original-ciphertext"}]},{"type":"text","text":"Found it"}],"usage":{}}`)
	third := parsed(t, `{"id":"msg_followup","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"More detail"}],"usage":{}}`)
	for _, upstreamStream := range []bool{false, true} {
		for _, clientStream := range []bool{false, true} {
			t.Run(fmt.Sprintf("upstream=%v/client=%v", upstreamStream, clientStream), func(t *testing.T) {
				var count atomic.Int32
				calls := make(chan provider.RawRequest, 4)
				_, base := fixture(t, &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
					calls <- r
					index := int(count.Add(1)) - 1
					if index > 2 {
						return nil, fmt.Errorf("unexpected automatic request")
					}
					return deferredSearchReply(t, []object{first, second, third}[index], upstreamStream), nil
				}})
				initial := object{"role": "user", "content": "Search and read"}
				input := []any{initial}
				request := object{"model": "claude-fixture", "input": input, "stream": clientStream, "tools": []any{object{"type": "web_search"}, object{"type": "function", "name": "read", "parameters": object{"type": "object"}}}}
				exchange := func() object {
					t.Helper()
					data, _ := json.Marshal(request)
					return readDeferredResponse(t, send(t, "POST", base+"/responses", string(data), map[string]string{"Authorization": "Bearer fixture-key", "Thread-Id": "mixed-thread"}), clientStream)
				}
				reply := exchange()
				sent := <-calls
				output := array(reply["output"])
				if obj(output[0])["type"] != "web_search_call" || obj(output[0])["status"] != "in_progress" || obj(output[1])["type"] != "function_call" {
					t.Fatal(reply)
				}
				input = append(input, output...)
				input = append(input, object{"type": "function_call_output", "call_id": "read_1", "output": "local file"})
				request["input"] = input
				reply = exchange()
				resumed := <-calls
				history := array(parsed(t, string(resumed.Body))["messages"])
				if len(history) != 3 || !reflect.DeepEqual(obj(history[1])["content"], first["content"]) {
					t.Fatal("mixed assistant history changed", string(resumed.Body))
				}
				results := array(obj(history[2])["content"])
				if len(results) != 1 || obj(results[0])["type"] != "tool_result" || obj(results[0])["tool_use_id"] != "read_1" || obj(results[0])["content"] != "local file" || resumed.ConversationID != sent.ConversationID || !resumed.AgentInitiated {
					t.Fatal("tool-only continuation changed", string(resumed.Body))
				}
				for _, v := range array(reply["output"]) {
					if obj(v)["type"] == "web_search_call" {
						t.Fatal("historical search was re-emitted", reply)
					}
				}
				input = append(input, array(reply["output"])...)
				request["input"] = append(input, object{"role": "user", "content": "More detail"})
				exchange()
				followup := <-calls
				finalHistory := array(parsed(t, string(followup.Body))["messages"])
				if len(finalHistory) != 5 || !reflect.DeepEqual(obj(finalHistory[3])["content"], second["content"]) || count.Load() != 3 {
					t.Fatal("deferred result did not replay unchanged", string(followup.Body), count.Load())
				}
				// A pending search still requires its original tool declaration.
				request["tools"] = []any{object{"type": "function", "name": "read", "parameters": object{"type": "object"}}}
				request["input"] = append(append([]any{initial}, output...), object{"type": "function_call_output", "call_id": "read_1", "output": "local file"})
				bad, _ := json.Marshal(request)
				if _, _, _, err := responsesToMessages(bad, provider.Model{ID: "claude"}); err == nil {
					t.Fatal("pending search accepted without its tool")
				}
			})
		}
	}
	// An unknown or already-completed ID must not be accepted as delayed work.
	for _, pending := range []map[string]bool{nil, {"different": true}} {
		if _, err := messagesToResponse(second, "claude", &responsesBridge{pendingSearches: pending}); err == nil {
			t.Fatal("orphan search result accepted")
		}
	}
	duplicate := parsed(t, `{"id":"msg_duplicate","type":"message","role":"assistant","stop_reason":"end_turn","content":[],"usage":{}}`)
	duplicate["content"] = []any{array(second["content"])[0], array(second["content"])[0]}
	bridge := &responsesBridge{pendingSearches: map[string]bool{"srv_deferred": true}}
	if _, err := messagesToResponse(duplicate, "claude", bridge); err == nil || !bridge.pendingSearches["srv_deferred"] {
		t.Fatal("duplicate result accepted or request state mutated", err)
	}
}
