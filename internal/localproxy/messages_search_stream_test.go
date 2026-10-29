package localproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func readMessagesStream(t *testing.T, body string) object {
	t.Helper()
	message := object{"role": "assistant"}
	var blocks []any
	arguments := map[int]string{}
	stops, finished := 0, false
	if err := readResponseEvents(strings.NewReader(body), func(event object) error {
		index := number(event["index"])
		switch event["type"] {
		case "error":
			t.Fatal(event)
		case "content_block_start":
			if index != len(blocks) {
				t.Fatal("unexpected block order", event)
			}
			blocks = append(blocks, event["content_block"])
		case "content_block_delta":
			delta, block := obj(event["delta"]), obj(blocks[index])
			switch delta["type"] {
			case "signature_delta", "thinking_delta", "text_delta":
				field := map[string]string{"signature_delta": "signature", "thinking_delta": "thinking", "text_delta": "text"}[text(delta["type"])]
				block[field] = text(block[field]) + text(delta[field])
			case "input_json_delta":
				arguments[index] += text(delta["partial_json"])
			}
		case "content_block_stop":
			stops++
		case "message_delta":
			message["stop_reason"] = obj(event["delta"])["stop_reason"]
		case "message_stop":
			finished = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !finished || stops != len(blocks) {
		t.Fatal("stream did not close every block and message", body)
	}
	for index, value := range arguments {
		obj(blocks[index])["input"] = parsed(t, value)
	}
	message["content"] = blocks
	return message
}

func assertMessagesAppendOnlyPrefix(t *testing.T, request object) object {
	t.Helper()
	convert := func() object {
		data, _, err := messagesToResponses(encodedDiscovery(t, request), provider.Model{ID: "gpt-test"})
		if err != nil {
			t.Fatal(err)
		}
		return parsed(t, string(data))
	}
	first := convert()
	previous, _ := json.Marshal(first["input"])
	tools, _ := json.Marshal(first["tools"])
	for turn := range 3 {
		request["messages"] = append(array(request["messages"]),
			object{"role": "assistant", "content": "Done."},
			object{"role": "user", "content": fmt.Sprintf("Continue %d", turn)},
		)
		next := convert()
		input, _ := json.Marshal(next["input"])
		nextTools, _ := json.Marshal(next["tools"])
		if !bytes.HasPrefix(input, previous[:len(previous)-1]) || !bytes.Equal(tools, nextTools) || !reflect.DeepEqual(first["instructions"], next["instructions"]) {
			t.Fatal("appending a turn changed the upstream history, tool catalog or instructions")
		}
		previous = input
	}
	return first
}

func TestMessagesRotatingItemIDsKeepReplayAndCachePrefix(t *testing.T) {
	for _, mode := range []string{"client", "server", "namespace"} {
		t.Run(mode, func(t *testing.T) {
			context := &messagesSearchContext{mode: "client", name: "ToolSearch"}
			search := any(claudeSearchTool())
			reply := searchResponseFixture(t, "client")
			result := object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "search_1", "content": []any{object{"type": "tool_reference", "tool_name": "read"}}}}}
			read := object{"name": "read", "defer_loading": true, "input_schema": object{"type": "object", "properties": object{}}}
			switch mode {
			case "server":
				context = &messagesSearchContext{mode: "server", name: "tool_search_tool_regex"}
				search = object{"type": "tool_search_tool_regex_20251119", "name": context.name}
				reply = searchResponseFixture(t, "server")
				result = object{"role": "user", "content": "Continue."}
			case "namespace":
				reply["output"] = []any{parsed(t, `{"type":"function_call","id":"fc_original","call_id":"business","name":"read","namespace":"functions","status":"completed","arguments":"{\"large\":9007199254740993}"}`)}
				delete(read, "defer_loading")
				result = object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "business", "content": "ok"}}}
			}
			reasoning := parsed(t, `{"type":"reasoning","id":"reasoning_original","summary":[],"encrypted_content":"sealed-original","future":{"number":9007199254740993}}`)
			reply["output"] = append([]any{reasoning}, array(reply["output"])...)
			var frames []string
			for index, value := range array(reply["output"]) {
				for _, phase := range []string{"added", "done", "repeat"} {
					item := cloneToolObject(obj(value))
					kind := phase
					if phase != "done" {
						item["id"] = fmt.Sprintf("%s_%d", phase, index)
					}
					if phase == "repeat" {
						kind = "done"
					}
					data, _ := json.Marshal(object{"type": "response.output_item." + kind, "output_index": index, "item": item})
					frames = append(frames, string(data))
				}
			}
			terminal := copyMessagesObject(reply)
			for index, value := range array(terminal["output"]) {
				obj(value)["id"] = fmt.Sprintf("terminal_%d", index)
			}
			obj(array(terminal["output"])[0])["encrypted_content"] = "sealed-terminal"
			data, _ := json.Marshal(object{"type": "response.completed", "response": terminal})
			frames = append(frames, string(data))
			w := httptest.NewRecorder()
			writeMessagesResponse(w, response(sse(frames...)), "public", true, context)
			message := readMessagesStream(t, w.Body.String())
			wantBlocks := map[string]int{"client": 2, "server": 4, "namespace": 3}[mode]
			if len(array(message["content"])) != wantBlocks {
				t.Fatal("duplicate native item was emitted", message)
			}
			request := object{"system": "Stable instructions", "tools": []any{search, read}, "messages": []any{
				object{"role": "user", "content": "Find and read"}, message, result,
			}}
			first := assertMessagesAppendOnlyPrefix(t, request)
			input := array(first["input"])
			if !reflect.DeepEqual(input[1], reasoning) {
				t.Fatal("stream completion replaced original reasoning replay", input[1])
			}
			if mode == "client" {
				call := obj(input[2])
				original := obj(array(reply["output"])[1])
				if call["call_id"] != original["call_id"] || !reflect.DeepEqual(call["arguments"], original["arguments"]) {
					t.Fatal("client search identity or arguments changed", call)
				}
			} else if !reflect.DeepEqual(input[2:len(array(reply["output"]))+1], array(reply["output"])[1:]) {
				t.Fatal("completion replaced the native replay IDs or content", input)
			}
		})
	}
}
