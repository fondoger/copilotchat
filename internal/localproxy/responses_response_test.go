package localproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const reverseMessageReply = `{"id":"msg_fixture","type":"message","role":"assistant","model":"upstream","content":[{"type":"thinking","thinking":"consider","signature":"signed"},{"type":"text","text":"Hello"},{"type":"tool_use","id":"toolu_original","name":"read","input":{"number":9007199254740993}},{"type":"tool_use","id":"toolu_custom","name":"shell","input":{"input":"pwd\n"}},{"type":"redacted_thinking","data":"opaque"}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":700,"output_tokens":50}}`

func reverseResponse(t *testing.T, source, contentType string, stream bool, bridge *responsesBridge) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(source))}
	writeResponsesResponse(w, response, "public/claude", stream, bridge)
	return w
}

func reverseSSE(events ...object) string {
	var out strings.Builder
	for _, e := range events {
		data, _ := json.Marshal(e)
		fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", text(e["type"]), data)
	}
	return out.String()
}

func reverseStart() object {
	return object{"type": "message_start", "message": object{"id": "msg_stream", "type": "message", "role": "assistant", "content": []any{}, "stop_reason": nil,
		"usage": object{"input_tokens": 10, "cache_creation_input_tokens": 20, "cache_read_input_tokens": 30, "output_tokens": 0}}}
}

func reverseBlockStart(index int, block object) object {
	return object{"type": "content_block_start", "index": index, "content_block": block}
}
func reverseBlockDelta(index int, delta object) object {
	return object{"type": "content_block_delta", "index": index, "delta": delta}
}
func reverseBlockStop(index int) object { return object{"type": "content_block_stop", "index": index} }
func reverseMessageDelta(reason string) object {
	return object{"type": "message_delta", "delta": object{"stop_reason": reason, "stop_sequence": nil}, "usage": object{"output_tokens": 7}}
}
func reverseMessageStop() object { return object{"type": "message_stop"} }

func reverseTextEvents(reason string) []object {
	return []object{reverseStart(), reverseBlockStart(0, object{"type": "text", "text": ""}),
		reverseBlockDelta(0, object{"type": "text_delta", "text": "Hello "}), reverseBlockDelta(0, object{"type": "text_delta", "text": "world"}),
		reverseBlockStop(0), reverseMessageDelta(reason), reverseMessageStop()}
}

func reverseReadEvents(t *testing.T, body string) []object {
	t.Helper()
	events := []object{}
	if err := readResponseEvents(strings.NewReader(body), func(e object) error { events = append(events, e); return nil }); err != nil {
		t.Fatal(err, body)
	}
	for i, e := range events {
		if number(e["sequence_number"]) != i {
			t.Fatalf("event %d has sequence %v", i, e)
		}
	}
	return events
}

func reverseFinal(t *testing.T, events []object) object {
	t.Helper()
	if len(events) < 3 {
		t.Fatal(events)
	}
	if text(events[0]["type"]) != "response.created" || text(events[1]["type"]) != "response.in_progress" {
		t.Fatal(events)
	}
	return obj(events[len(events)-1]["response"])
}

func TestResponsesReverseJSONToolsUsageAndReplay(t *testing.T) {
	source := parsed(t, reverseMessageReply)
	before, _ := json.Marshal(source)
	bridge := &responsesBridge{custom: map[string]bool{"shell": true}}
	response, err := messagesToResponse(source, "public/claude", bridge)
	if err != nil {
		t.Fatal(err)
	}
	if response["model"] != "public/claude" || response["status"] != "completed" {
		t.Fatal(response)
	}
	output := array(response["output"])
	if len(output) != 4 {
		t.Fatal(output)
	}
	function, custom := obj(output[1]), obj(output[2])
	if function["call_id"] != "toolu_original" || function["arguments"] != `{"number":9007199254740993}` {
		t.Fatal(function)
	}
	if custom["type"] != "custom_tool_call" || custom["call_id"] != "toolu_custom" || custom["input"] != "pwd\n" {
		t.Fatal(custom)
	}
	if obj(output[3])["type"] != "reasoning" || text(obj(output[3])["encrypted_content"]) == "" {
		t.Fatal(output)
	}
	usage := obj(response["usage"])
	if usage["input_tokens"] != 1000 || usage["output_tokens"] != 50 || usage["total_tokens"] != 1050 || obj(usage["input_tokens_details"])["cached_tokens"] != 700 {
		t.Fatal(usage)
	}
	restored, err := restoreAnthropicInput(output)
	if err != nil || len(restored) != 1 || !reflect.DeepEqual(obj(restored[0])["content"], source["content"]) {
		t.Fatal(restored, err)
	}
	after, _ := json.Marshal(source)
	if !bytes.Equal(before, after) {
		t.Fatal("conversion mutated incoming assistant blocks")
	}
}

func TestResponsesReverseStopReasonsAndRefusal(t *testing.T) {
	for _, tc := range []struct{ reason, status, detail string }{
		{"end_turn", "completed", ""}, {"stop_sequence", "completed", ""}, {"tool_use", "completed", ""},
		{"max_tokens", "incomplete", "max_output_tokens"}, {"pause_turn", "incomplete", ""}, {"refusal", "completed", ""},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			message := parsed(t, `{"id":"msg_stop","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"usage":{}}`)
			message["stop_reason"] = tc.reason
			response, err := messagesToResponse(message, "model", nil)
			if err != nil || response["status"] != tc.status || text(obj(response["incomplete_details"])["reason"]) != tc.detail {
				t.Fatal(response, err)
			}
			part := obj(array(obj(array(response["output"])[0])["content"])[0])
			if tc.reason == "refusal" && (part["type"] != "refusal" || part["refusal"] != "answer") {
				t.Fatal(part)
			}
		})
	}
}

func TestResponsesReverseSearchFailureCitationsAndReplay(t *testing.T) {
	source := parsed(t, `{"id":"msg_search","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srv_search","name":"web_search","input":{"query":"real query"}},{"type":"web_search_tool_result","tool_use_id":"srv_search","content":{"type":"web_search_tool_result_error","error_code":"too_many_requests"}},{"type":"text","text":"A😀B","citations":[{"type":"web_search_result_location","url":"https://example.com/source","title":"Source","encrypted_index":"private-provenance","cited_text":"A"}]}],"usage":{}}`)
	before, _ := json.Marshal(source)
	response, err := messagesToResponse(source, "model", nil)
	if err != nil {
		t.Fatal(err)
	}
	output := array(response["output"])
	search := obj(output[0])
	if search["type"] != "web_search_call" || search["id"] != "srv_search" || search["status"] != "failed" || obj(search["action"])["type"] != "search" || obj(search["action"])["query"] != "real query" {
		t.Fatal(search)
	}
	annotation := obj(array(obj(array(obj(output[1])["content"])[0])["annotations"])[0])
	if annotation["type"] != "url_citation" || annotation["url"] != "https://example.com/source" || annotation["start_index"] != 0 || annotation["end_index"] != 4 {
		t.Fatal(annotation)
	}
	restored, err := restoreAnthropicInput(output)
	if err != nil || !reflect.DeepEqual(obj(restored[0])["content"], source["content"]) {
		t.Fatal(restored, err)
	}
	after, _ := json.Marshal(source)
	if !bytes.Equal(before, after) {
		t.Fatal("search provenance was mutated")
	}
	content := array(source["content"])
	source["content"] = []any{content[0], content[2]}
	if _, err := messagesToResponse(source, "model", nil); err == nil {
		t.Fatal("unfinished search reported as a completed turn")
	}
	source["stop_reason"] = "pause_turn"
	if response, err := messagesToResponse(source, "model", nil); err != nil || response["status"] != "incomplete" {
		t.Fatal("paused search lost its continuation state", response, err)
	}
	block := obj(content[2])
	block["citations"] = append(array(block["citations"]), array(block["citations"])[0], object{"type": "web_search_result_location", "url": "javascript:alert(1)"})
	if len(array(responsesTextPart(block, false)["annotations"])) != 1 {
		t.Fatal("unsafe or duplicate citation URL exposed")
	}
}

func TestResponsesReverseJSONAndSSESources(t *testing.T) {
	for _, clientStream := range []bool{false, true} {
		for _, sourceSSE := range []bool{false, true} {
			t.Run(fmt.Sprintf("client_stream=%v/source_sse=%v", clientStream, sourceSSE), func(t *testing.T) {
				source, contentType := `{"id":"msg_json","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Hello world"}],"usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":7}}`, "application/json"
				if sourceSSE {
					source, contentType = reverseSSE(reverseTextEvents("end_turn")...), "text/event-stream"
				}
				w := reverseResponse(t, source, contentType, clientStream, nil)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				var response object
				if clientStream {
					events := reverseReadEvents(t, w.Body.String())
					response = reverseFinal(t, events)
					var value strings.Builder
					done := map[string]bool{}
					for _, e := range events {
						kind, id := text(e["type"]), text(e["item_id"])
						if strings.HasSuffix(kind, ".delta") && done[id] {
							t.Fatal("delta after done", e)
						}
						if kind == "response.output_text.delta" {
							value.WriteString(text(e["delta"]))
						}
						if kind == "response.output_item.done" {
							done[text(obj(e["item"])["id"])] = true
						}
					}
					if value.String() != "Hello world" {
						t.Fatal("duplicate or missing text", value.String())
					}
					if text(events[len(events)-1]["type"]) != "response.completed" {
						t.Fatal(events)
					}
					for _, e := range events[:len(events)-1] {
						if r := obj(e["response"]); r != nil && r["id"] != response["id"] {
							t.Fatal("response ID changed", events)
						}
					}
				} else {
					response = parsed(t, w.Body.String())
				}
				output := array(response["output"])
				if len(output) != 2 || obj(output[1])["type"] != "reasoning" || text(obj(array(obj(output[0])["content"])[0])["text"]) != "Hello world" {
					t.Fatal(response)
				}
				usage := obj(response["usage"])
				if number(usage["input_tokens"]) != 60 || number(usage["output_tokens"]) != 7 || number(obj(usage["input_tokens_details"])["cached_tokens"]) != 30 {
					t.Fatal(usage)
				}
			})
		}
	}
}

type reverseProbeWriter struct {
	*httptest.ResponseRecorder
	frames chan string
}

func (w *reverseProbeWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.frames <- string(p)
	return n, err
}

func TestResponsesReverseIncrementalTextAndPartialToolArguments(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	w := &reverseProbeWriter{ResponseRecorder: httptest.NewRecorder(), frames: make(chan string, 64)}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		writeResponsesResponse(w, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, "model", true, nil)
	}()
	prefix := []object{reverseStart(), reverseBlockStart(0, object{"type": "text", "text": ""}),
		reverseBlockDelta(0, object{"type": "text_delta", "text": "visible now"}), reverseBlockStop(0),
		reverseBlockStart(1, object{"type": "tool_use", "id": "toolu_stream", "name": "read", "input": object{}}),
		reverseBlockDelta(1, object{"type": "input_json_delta", "partial_json": "{\"n\":"})}
	if _, err := io.WriteString(writer, reverseSSE(prefix...)); err != nil {
		t.Fatal(err)
	}
	sawText, sawArguments := false, false
	for !sawText || !sawArguments {
		select {
		case frame := <-w.frames:
			if strings.Contains(frame, "event: response.output_text.delta") {
				sawText = true
			}
			if strings.Contains(frame, "event: response.function_call_arguments.delta") {
				sawArguments = true
			}
		case <-finished:
			t.Fatal("translator completed before upstream terminal event")
		case <-time.After(3 * time.Second):
			t.Fatal("text/tool argument deltas were buffered until completion")
		}
	}
	suffix := []object{reverseBlockDelta(1, object{"type": "input_json_delta", "partial_json": "9007199254740993}"}),
		reverseBlockStop(1), reverseMessageDelta("tool_use"), reverseMessageStop()}
	if _, err := io.WriteString(writer, reverseSSE(suffix...)); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("translator did not finish")
	}
	events := reverseReadEvents(t, w.Body.String())
	var args, visible strings.Builder
	for _, e := range events {
		if e["type"] == "response.function_call_arguments.delta" {
			args.WriteString(text(e["delta"]))
		}
		if e["type"] == "response.output_text.delta" {
			visible.WriteString(text(e["delta"]))
		}
	}
	if args.String() != `{"n":9007199254740993}` || visible.String() != "visible now" {
		t.Fatal(args.String(), visible.String())
	}
	response := reverseFinal(t, events)
	output := array(response["output"])
	if obj(output[1])["call_id"] != "toolu_stream" || obj(output[len(output)-1])["type"] != "reasoning" {
		t.Fatal(output)
	}
	if _, err := restoreAnthropicInput(output); err != nil {
		t.Fatal("streamed arguments broke replay", err)
	}
}

func TestResponsesReverseStreamingCustomToolsSearchAndThinking(t *testing.T) {
	events := []object{reverseStart(),
		reverseBlockStart(0, object{"type": "thinking", "thinking": "", "signature": ""}),
		reverseBlockDelta(0, object{"type": "thinking_delta", "thinking": "private thought"}),
		reverseBlockDelta(0, object{"type": "signature_delta", "signature": "signed"}), reverseBlockStop(0),
		reverseBlockStart(1, object{"type": "server_tool_use", "id": "srv_stream", "name": "web_search", "input": object{}}),
		reverseBlockDelta(1, object{"type": "input_json_delta", "partial_json": "{\"query\":\"actual search\"}"}), reverseBlockStop(1),
		reverseBlockStart(2, object{"type": "web_search_tool_result", "tool_use_id": "srv_stream", "content": object{"type": "web_search_tool_result_error", "error_code": "unavailable"}}), reverseBlockStop(2),
		reverseBlockStart(3, object{"type": "tool_use", "id": "toolu_shell", "name": "shell", "input": object{}}),
		reverseBlockDelta(3, object{"type": "input_json_delta", "partial_json": "{\"input\":\"he"}),
		reverseBlockDelta(3, object{"type": "input_json_delta", "partial_json": "llo\\n\"}"}), reverseBlockStop(3),
		reverseMessageDelta("tool_use"), reverseMessageStop()}
	w := reverseResponse(t, reverseSSE(events...), "text/event-stream", true, &responsesBridge{custom: map[string]bool{"shell": true}})
	translated := reverseReadEvents(t, w.Body.String())
	response := reverseFinal(t, translated)
	if response["status"] != "completed" {
		t.Fatal(response)
	}
	output := array(response["output"])
	if len(output) != 3 || obj(output[0])["type"] != "web_search_call" || obj(output[0])["status"] != "failed" || obj(obj(output[0])["action"])["query"] != "actual search" {
		t.Fatal(output)
	}
	if obj(output[1])["call_id"] != "toolu_shell" || obj(output[1])["input"] != "hello\n" || obj(output[2])["type"] != "reasoning" {
		t.Fatal(output)
	}
	var custom strings.Builder
	lastDone := ""
	for _, e := range translated {
		if e["type"] == "response.custom_tool_call_input.delta" {
			custom.WriteString(text(e["delta"]))
		}
		if e["type"] == "response.output_item.done" {
			lastDone = text(obj(e["item"])["type"])
		}
	}
	if custom.String() != "hello\n" || lastDone != "reasoning" {
		t.Fatal(custom.String(), lastDone)
	}
	restored, err := restoreAnthropicInput(output)
	if err != nil {
		t.Fatal(err)
	}
	blocks := array(obj(restored[0])["content"])
	if obj(blocks[0])["thinking"] != "private thought" || obj(blocks[0])["signature"] != "signed" {
		t.Fatal(blocks)
	}
}

func TestResponsesReverseStreamingIncompleteAndRefusal(t *testing.T) {
	for _, tc := range []struct {
		reason string
		detail any
	}{{"pause_turn", nil}, {"max_tokens", "max_output_tokens"}, {"refusal", "content_filter"}} {
		t.Run(tc.reason, func(t *testing.T) {
			w := reverseResponse(t, reverseSSE(reverseTextEvents(tc.reason)...), "text/event-stream", true, nil)
			events := reverseReadEvents(t, w.Body.String())
			response := reverseFinal(t, events)
			if events[len(events)-1]["type"] != "response.incomplete" || obj(response["incomplete_details"])["reason"] != tc.detail {
				t.Fatal(response)
			}
			if tc.reason == "pause_turn" && (obj(response["error"])["code"] != "server_error" || !strings.Contains(text(obj(response["error"])["message"]), "pause_turn")) {
				t.Fatal("pause_turn lost its non-success explanation", response)
			}
			var content strings.Builder
			for _, e := range events {
				if e["type"] == "response.output_text.delta" {
					content.WriteString(text(e["delta"]))
				}
				if e["type"] == "response.refusal.delta" {
					t.Fatal("reclassified streamed text", e)
				}
			}
			if content.String() != "Hello world" {
				t.Fatal(content.String())
			}
		})
	}
	message := `{"id":"msg_refusal","type":"message","role":"assistant","stop_reason":"refusal","content":[{"type":"text","text":"Cannot help"}],"usage":{}}`
	events := reverseReadEvents(t, reverseResponse(t, message, "application/json", true, nil).Body.String())
	response := reverseFinal(t, events)
	part := obj(array(obj(array(response["output"])[0])["content"])[0])
	if part["type"] != "refusal" || part["refusal"] != "Cannot help" {
		t.Fatal(part)
	}
}

func TestResponsesReverseRejectsTruncationAndStreamErrors(t *testing.T) {
	prefix := []object{reverseStart(), reverseBlockStart(0, object{"type": "text", "text": ""}), reverseBlockDelta(0, object{"type": "text_delta", "text": "partial"})}
	cases := map[string]string{
		"truncated":         reverseSSE(prefix...),
		"malformed":         reverseSSE(prefix...) + "data: {not json}\n\n",
		"error":             reverseSSE(append(prefix, object{"type": "error", "error": object{"type": "overloaded_error", "message": "overloaded"}})...),
		"unfinished_block":  reverseSSE(append(prefix, reverseMessageDelta("end_turn"), reverseMessageStop())...),
		"missing_reason":    reverseSSE(reverseStart(), reverseMessageStop()),
		"late_delta":        reverseSSE(append(prefix, reverseBlockStop(0), reverseBlockDelta(0, object{"type": "text_delta", "text": "late"}), reverseMessageDelta("end_turn"), reverseMessageStop())...),
		"invalid_arguments": reverseSSE(reverseStart(), reverseBlockStart(0, object{"type": "tool_use", "id": "toolu_bad", "name": "read", "input": object{}}), reverseBlockDelta(0, object{"type": "input_json_delta", "partial_json": "{broken"}), reverseBlockStop(0), reverseMessageDelta("tool_use"), reverseMessageStop()),
	}
	for name, source := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", name, stream), func(t *testing.T) {
				w := reverseResponse(t, source, "text/event-stream", stream, nil)
				if !stream {
					if w.Code != 502 || obj(parsed(t, w.Body.String())["error"]) == nil {
						t.Fatal(w.Code, w.Body.String())
					}
					return
				}
				events := reverseReadEvents(t, w.Body.String())
				if text(events[len(events)-1]["type"]) != "response.failed" || obj(reverseFinal(t, events)["error"])["code"] != "server_error" {
					t.Fatal(events)
				}
				for _, e := range events {
					if e["type"] == "response.completed" {
						t.Fatal("failed upstream reported success", e)
					}
				}
			})
		}
	}
}

type reverseReadFailure struct{ io.Reader }

func TestResponsesReverseBoundsAccumulatedBlockStartPayloads(t *testing.T) {
	w := httptest.NewRecorder()
	s := newResponsesStream(w, "model", nil, true)
	if err := s.event(reverseStart()); err != nil {
		t.Fatal(err)
	}
	// Each frame fits the per-event limit, but together these encrypted blocks
	// exceed the reply limit without sending a single text or argument delta.
	payload := strings.Repeat("a", MaxBody/2)
	first := reverseBlockStart(0, object{"type": "redacted_thinking", "data": payload})
	first["index"] = json.Number("0")
	if err := s.event(first); err != nil {
		t.Fatal(err)
	}
	stop := reverseBlockStop(0)
	stop["index"] = json.Number("0")
	if err := s.event(stop); err != nil {
		t.Fatal(err)
	}
	second := reverseBlockStart(1, object{"type": "redacted_thinking", "data": payload})
	second["index"] = json.Number("1")
	err := s.event(second)
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatal("unbounded encrypted block accumulation", err)
	}
	s.fail(err)
	events := reverseReadEvents(t, w.Body.String())
	if events[len(events)-1]["type"] != "response.failed" || obj(reverseFinal(t, events)["error"])["code"] != "server_error" {
		t.Fatal(events)
	}
}

func (r reverseReadFailure) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func TestResponsesReverseReadFailuresAndHTTPError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		w := httptest.NewRecorder()
		response := &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))}
		writeResponsesResponse(w, response, "model", stream, nil)
		if w.Code != 429 || text(obj(parsed(t, w.Body.String())["error"])["message"]) != "slow down" {
			t.Fatal(w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		response = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(reverseReadFailure{strings.NewReader(reverseSSE(reverseStart()))})}
		writeResponsesResponse(w, response, "model", stream, nil)
		if stream {
			events := reverseReadEvents(t, w.Body.String())
			if events[len(events)-1]["type"] != "response.failed" {
				t.Fatal(events)
			}
		} else if w.Code != 502 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, source := range []string{`{`, `{"type":"error","error":{"message":"bad"}}`, `{"id":"msg_missing","type":"message","role":"assistant","content":[]}`} {
		w := reverseResponse(t, source, "application/json", false, nil)
		if w.Code != 502 {
			t.Fatal("invalid JSON response succeeded", w.Code, w.Body.String())
		}
	}
}
