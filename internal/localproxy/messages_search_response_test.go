package localproxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func searchResponseFixture(t *testing.T, mode string) object {
	t.Helper()
	callID, arguments := `"search_1"`, `{"query":"find read","large":9007199254740993}`
	if mode == "server" {
		callID, arguments = "null", `{"paths":["read","write"]}`
	}
	output := fmt.Sprintf(`[{"id":"ts_1","type":"tool_search_call","execution":%q,"call_id":%s,"arguments":%s,"status":"completed"}`, mode, callID, arguments)
	if mode == "server" {
		output += `,{"id":"tso_1","type":"tool_search_output","execution":"server","call_id":null,"status":"completed","tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{}},"defer_loading":true}]} `
	}
	return parsed(t, `{"id":"resp_search","status":"completed","output":`+output+`]}`)
}

func TestMessagesNativeSearchClient(t *testing.T) {
	response := searchResponseFixture(t, "client")
	ctx := &messagesSearchContext{mode: "client", name: "ToolSearch"}
	message, err := responsesToMessage(response, "public", ctx)
	if err != nil {
		t.Fatal(err)
	}
	block := obj(array(message["content"])[0])
	if block["id"] != "search_1" || block["name"] != "ToolSearch" || message["stop_reason"] != "tool_use" || text(obj(block["input"])["large"]) != "" {
		t.Fatal(message)
	}
	data, _ := json.Marshal(block["input"])
	if !strings.Contains(string(data), "9007199254740993") {
		t.Fatal(string(data))
	}
	for _, ctx := range []*messagesSearchContext{nil, {mode: "server", name: "tool_search_tool_regex"}} {
		if _, err := responsesToMessage(response, "public", ctx); err == nil {
			t.Fatal("accepted unexpected client search")
		}
	}
}

func TestMessagesNativeSearchHostedReplay(t *testing.T) {
	for _, name := range []string{"tool_search_tool_regex", "tool_search_tool_bm25"} {
		t.Run(name, func(t *testing.T) {
			response := searchResponseFixture(t, "server")
			ctx := &messagesSearchContext{mode: "server", name: name}
			message, err := responsesToMessage(response, "public", ctx)
			if err != nil {
				t.Fatal(err)
			}
			blocks := array(message["content"])
			if len(blocks) != 3 || obj(blocks[0])["type"] != "thinking" || obj(blocks[1])["type"] != "server_tool_use" || obj(blocks[2])["type"] != "tool_search_tool_result" || message["stop_reason"] != "end_turn" {
				t.Fatal(message)
			}
			if !strings.HasPrefix(text(obj(blocks[1])["id"]), "srvtoolu_") || obj(blocks[2])["tool_use_id"] != obj(blocks[1])["id"] {
				t.Fatal(blocks)
			}
			again, err := responsesToMessage(response, "public", ctx)
			if err != nil || !reflect.DeepEqual(again["content"], blocks) {
				t.Fatal("unstable blocks", err)
			}
			src := object{"messages": []any{message}}
			if err := restoreMessagesSearchReplay(src); err != nil {
				t.Fatal(err)
			}
			replay := obj(array(message["content"])[0])
			if replay["type"] != "responses_search_replay" || !reflect.DeepEqual(replay["items"], response["output"]) {
				t.Fatal(replay)
			}
		})
	}
}

func TestMessagesNativeSearchReplayRejectsEdits(t *testing.T) {
	for _, edit := range []string{"missing", "input", "result", "role", "native", "encoding", "cache"} {
		t.Run(edit, func(t *testing.T) {
			message, err := responsesToMessage(searchResponseFixture(t, "server"), "public", &messagesSearchContext{mode: "server", name: "tool_search_tool_regex"})
			if err != nil {
				t.Fatal(err)
			}
			blocks := array(message["content"])
			switch edit {
			case "missing":
				message["content"] = blocks[:2]
			case "input":
				obj(blocks[1])["input"] = object{"pattern": "changed"}
			case "result":
				obj(blocks[2])["content"] = []any{}
			case "role":
				message["role"] = "user"
			case "encoding":
				obj(blocks[0])["signature"] = messagesSearchReplayPrefix + "!"
			case "cache":
				obj(blocks[1])["cache_control"] = object{"type": "ephemeral"}
			case "native":
				signature := text(obj(blocks[0])["signature"])
				data, _ := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(signature, messagesSearchReplayPrefix))
				envelope, _ := decodeObject(data)
				obj(array(envelope["native"])[0])["execution"] = "client"
				data, _ = json.Marshal(envelope)
				obj(blocks[0])["signature"] = messagesSearchReplayPrefix + base64.RawStdEncoding.EncodeToString(data)
			}
			if err := restoreMessagesSearchReplay(object{"messages": []any{message}}); err == nil {
				t.Fatal("accepted edited replay")
			}
		})
	}
}

func TestMessagesNativeSearchHostedBoundaries(t *testing.T) {
	for _, variant := range []string{"empty", "namespace", "tool namespace", "missing", "unpaired", "concurrent", "call_id", "execution", "arguments", "status", "id"} {
		t.Run(variant, func(t *testing.T) {
			response := searchResponseFixture(t, "server")
			items := array(response["output"])
			switch variant {
			case "empty":
				obj(items[1])["tools"] = []any{}
			case "namespace":
				obj(items[1])["tools"] = []any{object{"type": "namespace", "name": "fs"}}
			case "tool namespace":
				obj(array(obj(items[1])["tools"])[0])["namespace"] = "fs"
			case "missing":
				response["output"] = items[:1]
			case "unpaired":
				response["output"] = items[1:]
			case "concurrent":
				response["output"] = []any{items[0], items[0], items[1]}
			case "call_id":
				obj(items[0])["call_id"] = "bad"
			case "execution":
				obj(items[1])["execution"] = "client"
			case "status":
				obj(items[1])["status"] = "in_progress"
			case "id":
				obj(items[0])["id"] = false
			case "arguments":
				obj(items[0])["arguments"] = `{"paths":["read"]}`
			}
			_, err := responsesToMessage(response, "public", &messagesSearchContext{mode: "server", name: "tool_search_tool_bm25"})
			if (err == nil) != (variant == "empty") {
				t.Fatal(variant, err)
			}
		})
	}
}

func TestMessagesNativeSearchStreaming(t *testing.T) {
	for _, mode := range []string{"client", "server"} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fallback=%v", mode, fallback), func(t *testing.T) {
				response := searchResponseFixture(t, mode)
				ctx := &messagesSearchContext{mode: mode, name: "ToolSearch"}
				if mode == "server" {
					ctx.name = "tool_search_tool_regex"
				}
				// A real business call must still stream normally after the hosted pair.
				if mode == "server" {
					response["output"] = append(array(response["output"]), parsed(t, `{"type":"function_call","call_id":"business","name":"read","arguments":"{\"path\":\"file\"}"}`))
				}
				var frames []string
				for i, value := range array(response["output"]) {
					for _, suffix := range []string{"added", "done"} {
						event := object{"type": "response.output_item." + suffix, "output_index": i, "item": value}
						data, _ := json.Marshal(event)
						frames = append(frames, string(data))
					}
				}
				terminal, _ := json.Marshal(object{"type": "response.completed", "response": response})
				frames = append(frames, string(terminal))
				upstream := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse(frames...)))}
				if fallback {
					data, _ := json.Marshal(response)
					upstream.Body = io.NopCloser(strings.NewReader(string(data)))
					upstream.Header.Set("Content-Type", "application/json")
				}
				w := httptest.NewRecorder()
				writeMessagesResponse(w, upstream, "public", true, ctx)
				var starts []object
				stops := 0
				signature := ""
				reason := ""
				err := readResponseEvents(strings.NewReader(w.Body.String()), func(e object) error {
					switch e["type"] {
					case "error":
						t.Fatal(e)
					case "content_block_start":
						starts = append(starts, obj(e["content_block"]))
					case "content_block_stop":
						stops++
					case "content_block_delta":
						signature += text(obj(e["delta"])["signature"])
					case "message_delta":
						reason = text(obj(e["delta"])["stop_reason"])
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if mode == "server" {
					want = 4
				}
				if len(starts) != want || stops != want || reason != "tool_use" {
					t.Fatal(w.Body.String())
				}
				if mode == "server" && (starts[0]["type"] != "thinking" || starts[1]["type"] != "server_tool_use" || starts[2]["type"] != "tool_search_tool_result" || starts[3]["id"] != "business" || !strings.HasPrefix(signature, messagesSearchReplayPrefix)) {
					t.Fatal(w.Body.String())
				}
			})
		}
	}
}

func TestMessagesNativeSearchHostedBufferedUntilOutput(t *testing.T) {
	response := searchResponseFixture(t, "server")
	items := array(response["output"])
	w := httptest.NewRecorder()
	stream := newMessagesStream(w, "public", &messagesSearchContext{mode: "server", name: "tool_search_tool_bm25"})
	if err := stream.item(0, obj(items[0]), true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "content_block_start") {
		t.Fatal("hosted call escaped before its output")
	}
	changed := copyMessagesObject(response)
	obj(array(changed["output"])[0])["id"] = "rotated-pending-call"
	if err := stream.complete(changed); err != nil {
		t.Fatal(err)
	}
	if stream.tools || !strings.Contains(w.Body.String(), `"stop_reason":"end_turn"`) || strings.Count(w.Body.String(), "event: content_block_start") != 3 {
		t.Fatal(w.Body.String())
	}
}

func TestMessagesNativeSearchReplayRejectsRawInternalBlock(t *testing.T) {
	src := object{"messages": []any{object{"role": "assistant", "content": []any{object{"type": "responses_search_replay", "items": []any{}}}}}}
	if err := restoreMessagesSearchReplay(src); err == nil {
		t.Fatal("accepted raw internal replay")
	}
}

func TestMessagesNativeSearchStreamRejectsChangedTerminal(t *testing.T) {
	changes := object{
		"arguments": object{"query": "tampered"}, "call_id": "changed", "execution": "invalid",
		"namespace": "changed", "status": "in_progress", "type": "message",
		"tools": []any{}, "future": true,
	}
	for _, mode := range []string{"client", "server"} {
		for field, value := range changes {
			t.Run(mode+"/"+field, func(t *testing.T) {
				response := searchResponseFixture(t, mode)
				context := &messagesSearchContext{mode: mode, name: "ToolSearch"}
				if mode == "server" {
					context.name = "tool_search_tool_regex"
				}
				stream := newMessagesStream(httptest.NewRecorder(), "public", context)
				for index, value := range array(response["output"]) {
					if err := stream.item(index, obj(value), true); err != nil {
						t.Fatal(err)
					}
				}
				changed := copyMessagesObject(response)
				items := array(changed["output"])
				obj(items[len(items)-1])[field] = value
				if err := stream.complete(changed); err == nil {
					t.Fatal("accepted changed terminal search item", changed)
				}
			})
		}
	}
}

func TestMessagesNativeSearchNamespacedBusinessReplay(t *testing.T) {
	call := parsed(t, `{"type":"function_call","id":"fc_ns","call_id":"business","name":"read","namespace":"functions","status":"completed","arguments":"{\"large\":9007199254740993}"}`)
	response := object{"id": "resp_ns", "status": "completed", "output": []any{call}}
	message, err := responsesToMessage(response, "public", nil)
	if err != nil {
		t.Fatal(err)
	}
	blocks := array(message["content"])
	if len(blocks) != 2 || obj(blocks[0])["type"] != "thinking" || obj(blocks[1])["type"] != "tool_use" || obj(blocks[1])["name"] != "read" {
		t.Fatal(message)
	}
	// Both a legitimate replay and mutations of the same visible block.
	for _, changed := range []bool{false, true} {
		copied, _ := json.Marshal(message)
		candidate, _ := decodeObject(copied)
		if changed {
			obj(array(candidate["content"])[1])["input"] = object{"large": "edited"}
		}
		err := restoreMessagesSearchReplay(object{"messages": []any{candidate}})
		if changed {
			if err == nil {
				t.Fatal("accepted changed namespaced tool")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		replay := obj(array(candidate["content"])[0])
		if !reflect.DeepEqual(replay["items"], []any{call}) {
			t.Fatal(replay)
		}
	}
	w := httptest.NewRecorder()
	stream := newMessagesStream(w, "public", nil)
	added := parsed(t, `{"type":"function_call","id":"fc_ns","call_id":"business","name":"read","namespace":"functions","arguments":"","status":"in_progress"}`)
	if err := stream.item(0, added, false); err != nil {
		t.Fatal(err)
	}
	if err := stream.event(object{"type": "response.function_call_arguments.delta", "output_index": json.Number("0"), "delta": "ignored until done"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "content_block_start") {
		t.Fatal("namespaced call was emitted before done")
	}
	if err := stream.item(0, call, true); err != nil {
		t.Fatal(err)
	}
	if err := stream.complete(response); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	if err := readResponseEvents(strings.NewReader(w.Body.String()), func(e object) error {
		if e["type"] == "content_block_start" {
			kinds = append(kinds, text(obj(e["content_block"])["type"]))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kinds, []string{"thinking", "tool_use"}) {
		t.Fatal(kinds, w.Body.String())
	}
}
