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

func toolSearchResponseFixture(t *testing.T) (*toolSearchContext, string, string, string) {
	t.Helper()
	c := newToolSearchContext()
	register := func(id toolIdentity) string {
		t.Helper()
		alias, err := c.register(id)
		if err != nil {
			t.Fatal(err)
		}
		return alias
	}
	return c, register(searchIdentity()), register(toolIdentity{namespace: "crm", name: "lookup", kind: "function"}), register(toolIdentity{namespace: "terminal", name: "run", kind: "custom"})
}

func toolSearchNativeReply(body, contentType string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func toolSearchNativeEvents(t *testing.T, body string) []object {
	t.Helper()
	var events []object
	if err := readResponseEvents(strings.NewReader(body), func(event object) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err, body)
	}
	return events
}

func TestToolSearchNativeJSON(t *testing.T) {
	c, search, function, custom := toolSearchResponseFixture(t)
	searchItem := object{"id": "fc_search", "type": "function_call", "name": search, "call_id": "call_search", "status": "completed", "arguments": `{"query":"files","limit":9007199254740993}`}
	functionItem := object{"id": "fc_lookup", "type": "function_call", "name": function, "arguments": `{"id":1}`}
	customItem := object{"id": "ctc_run", "type": "custom_tool_call", "name": custom, "input": "pwd"}
	ordinary := object{"id": "fc_normal", "type": "function_call", "name": "normal", "arguments": "{}"}
	// A tool's own result/arguments are opaque, even if they resemble responses.
	opaque := object{"type": "message", "payload": object{"output": []any{object{"type": "function_call", "name": search, "arguments": "not JSON"}}}}
	for _, envelope := range []object{
		{"object": "response", "output": []any{searchItem, functionItem, customItem, ordinary, opaque}},
		{"wrapper": object{"response": object{"object": "response.compaction", "output": []any{searchItem, functionItem, customItem, ordinary, opaque}}}},
	} {
		data, _ := json.Marshal(envelope)
		w := httptest.NewRecorder()
		writeToolSearchResponse(w, toolSearchNativeReply(string(data), "application/json"), c)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal(w.Code, w.Body.String())
		}
		body, err := decodeObject(w.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if body["wrapper"] != nil {
			body = obj(obj(body["wrapper"])["response"])
		}
		output := array(body["output"])
		searchCall := obj(output[0])
		if searchCall["type"] != "tool_search_call" || searchCall["id"] != "tsc_search" || searchCall["execution"] != "client" || searchCall["call_id"] != "call_search" || searchCall["name"] != nil {
			t.Fatal(searchCall)
		}
		args := obj(searchCall["arguments"])
		if args["query"] != "files" || fmt.Sprint(args["limit"]) != "9007199254740993" {
			t.Fatal(args)
		}
		if item := obj(output[1]); item["namespace"] != "crm" || item["name"] != "lookup" || item["id"] != "fc_lookup" || item["arguments"] != `{"id":1}` {
			t.Fatal(item)
		}
		if item := obj(output[2]); item["namespace"] != "terminal" || item["name"] != "run" || item["id"] != "ctc_run" || item["input"] != "pwd" {
			t.Fatal(item)
		}
		if !reflect.DeepEqual(output[3], ordinary) || !reflect.DeepEqual(output[4], opaque) {
			t.Fatal(output)
		}
	}
}

func TestToolSearchNativeSSE(t *testing.T) {
	c, search, function, custom := toolSearchResponseFixture(t)
	searchAdded := object{"type": "response.output_item.added", "output_index": 0, "item": object{"type": "function_call", "name": search, "id": "fc_search", "status": "in_progress", "arguments": ""}}
	functionAdded := object{"type": "response.output_item.added", "output_index": 1, "item": object{"type": "function_call", "name": function, "id": "fc_lookup", "arguments": ""}}
	customAdded := object{"type": "response.output_item.added", "output_index": 2, "item": object{"type": "custom_tool_call", "name": custom, "id": "ctc_run", "input": ""}}
	searchDone := object{"type": "response.output_item.done", "output_index": 0, "item": object{"type": "function_call", "name": search, "id": "fc_search", "status": "completed", "arguments": `{"query":"files"}`}}
	ordinaryDelta := "event: response.function_call_arguments.delta\nid: ordinary-4\ndata: {\"type\":\"response.function_call_arguments.delta\", \"item_id\":\"fc_lookup\",\"output_index\":1,\"delta\":\"{}\"}\n\n"
	unchanged := ": heartbeat\r\nid: frame-1\r\nevent: response.created\r\ndata: {\"type\":\"response.created\",\r\ndata: \"response\":{\"output\":[]}}\r\n\r\n"
	addedJSON, _ := json.Marshal(searchAdded)
	// Mutated frames retain non-data metadata and their CRLF framing too.
	addedFrame := ": search\r\nid: search-2\r\nevent: response.output_item.added\r\ndata: " + string(addedJSON) + "\r\n\r\n"
	body := unchanged + addedFrame + reverseSSE(functionAdded, customAdded) + reverseSSE(
		object{"type": "response.function_call_arguments.delta", "item_id": "fc_search", "delta": "{"},
		object{"type": "response.function_call_arguments.done", "output_index": 0, "arguments": `{"query":"files"}`},
	) + ordinaryDelta + reverseSSE(
		object{"type": "response.custom_tool_call_input.delta", "item_id": "ctc_run", "output_index": 2, "delta": "pwd"},
		object{"type": "response.function_call_arguments.done", "item_id": "fc_lookup", "output_index": 1, "arguments": "{}"},
		object{"type": "response.search_progress", "item_id": "fc_search", "output_index": 0},
		searchDone,
		object{"type": "response.completed", "response": object{"output": []any{searchDone["item"], functionAdded["item"], customAdded["item"]}}},
	) + "data: [DONE]\n\n"
	w := httptest.NewRecorder()
	writeToolSearchResponse(w, toolSearchNativeReply(body, "text/event-stream; charset=utf-8"), c)
	out := w.Body.String()
	if !strings.HasPrefix(out, unchanged+": search\r\nid: search-2\r\nevent: response.output_item.added\r\ndata: ") || !strings.Contains(out, ordinaryDelta) || !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Fatal(out)
	}
	events := toolSearchNativeEvents(t, out)
	if len(events) != 10 {
		t.Fatal(len(events), out)
	}
	if item := obj(events[1]["item"]); item["id"] != "tsc_search" || item["type"] != "tool_search_call" || obj(item["arguments"]) == nil {
		t.Fatal(item)
	}
	if item := obj(events[2]["item"]); item["id"] != "fc_lookup" || item["namespace"] != "crm" || item["name"] != "lookup" {
		t.Fatal(item)
	}
	if item := obj(events[3]["item"]); item["id"] != "ctc_run" || item["namespace"] != "terminal" {
		t.Fatal(item)
	}
	for _, event := range events {
		kind := text(event["type"])
		if strings.HasPrefix(kind, "response.function_call_arguments.") && event["item_id"] != "fc_lookup" {
			t.Fatal("search arguments leaked as function events", event)
		}
		if kind == "response.search_progress" && event["item_id"] != "tsc_search" {
			t.Fatal(event)
		}
	}
	if item := obj(events[8]["item"]); item["id"] != "tsc_search" || obj(item["arguments"])["query"] != "files" {
		t.Fatal(item)
	}
	output := array(obj(events[9]["response"])["output"])
	if obj(output[0])["type"] != "tool_search_call" || obj(output[1])["namespace"] != "crm" || obj(output[2])["namespace"] != "terminal" {
		t.Fatal(output)
	}
}

func TestToolSearchNativeErrors(t *testing.T) {
	c, alias, _, _ := toolSearchResponseFixture(t)
	for _, body := range []string{"upstream unavailable", `{"error":{"message":"slow down"}}`} {
		response := toolSearchNativeReply(body, "application/problem+json")
		response.StatusCode = http.StatusTooManyRequests
		w := httptest.NewRecorder()
		writeToolSearchResponse(w, response, c)
		if w.Code != response.StatusCode || w.Body.String() != body || w.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatal(w.Code, w.Body.String(), w.Header())
		}
	}
	for _, arguments := range []string{"not JSON", "[]", "null"} {
		item := object{"type": "function_call", "id": "fc_bad", "name": alias, "status": "completed", "arguments": arguments}
		data, _ := json.Marshal(object{"output": []any{item}})
		w := httptest.NewRecorder()
		writeToolSearchResponse(w, toolSearchNativeReply(string(data), "application/json"), c)
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "arguments") {
			t.Fatal(arguments, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		writeToolSearchResponse(w, toolSearchNativeReply(reverseSSE(object{"type": "response.output_item.done", "item": item}), "text/event-stream"), c)
		events := toolSearchNativeEvents(t, w.Body.String())
		if len(events) != 1 || events[0]["type"] != "error" || !strings.Contains(text(events[0]["message"]), "arguments") {
			t.Fatal(arguments, events)
		}
	}
	w := httptest.NewRecorder()
	writeToolSearchResponse(w, toolSearchNativeReply("data: not-json\n\n", "text/event-stream"), c)
	if events := toolSearchNativeEvents(t, w.Body.String()); len(events) != 1 || events[0]["type"] != "error" {
		t.Fatal(events)
	}
}

func TestToolSearchNativeBodyLimitsAndFinalFrame(t *testing.T) {
	for _, contentType := range []string{"application/json", "text/event-stream"} {
		body := strings.Repeat("x", MaxBody+1)
		if contentType == "text/event-stream" {
			body = "data: " + body
		}
		w := httptest.NewRecorder()
		writeToolSearchResponse(w, toolSearchNativeReply(body, contentType), nil)
		if contentType == "application/json" {
			if w.Code != http.StatusBadGateway {
				t.Fatal(w.Code)
			}
		} else if events := toolSearchNativeEvents(t, w.Body.String()); len(events) != 1 || events[0]["type"] != "error" {
			t.Fatal(events)
		}
	}
	body := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}"
	w := httptest.NewRecorder()
	writeToolSearchResponse(w, toolSearchNativeReply(body, "text/event-stream"), nil)
	if w.Body.String() != body {
		t.Fatal(w.Body.String())
	}
}

type toolSearchFlushRecorder struct {
	*httptest.ResponseRecorder
	frames chan string
}

func (w *toolSearchFlushRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.frames <- w.Body.String()
}

func TestToolSearchNativeFlushBeforeUpstreamEOF(t *testing.T) {
	r, upstream := io.Pipe()
	defer r.Close()
	defer upstream.Close()
	w := &toolSearchFlushRecorder{ResponseRecorder: httptest.NewRecorder(), frames: make(chan string, 2)}
	response := toolSearchNativeReply("", "text/event-stream")
	response.Body = r
	done := make(chan struct{})
	go func() {
		defer close(done)
		writeToolSearchResponse(w, response, nil)
	}()
	frame := "data: {\"type\":\"response.created\"}\n\n"
	if _, err := io.WriteString(upstream, frame); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-w.frames:
		if got != frame {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("frame was buffered until upstream EOF")
	}
	_ = upstream.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not finish after upstream EOF")
	}
}

type toolSearchFailingWriter struct {
	header http.Header
	writes int
}

func (w *toolSearchFailingWriter) Header() http.Header { return w.header }
func (w *toolSearchFailingWriter) WriteHeader(int)     {}
func (w *toolSearchFailingWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("client disconnected")
}

func TestToolSearchNativeStopsOnDownstreamFailure(t *testing.T) {
	// A reader that never reaches EOF must not keep the writer running after
	// the downstream client goes away.
	r, upstream := io.Pipe()
	defer r.Close()
	defer upstream.Close()
	w := &toolSearchFailingWriter{header: make(http.Header)}
	response := toolSearchNativeReply("", "text/event-stream")
	response.Body = r
	done := make(chan struct{})
	go func() {
		defer close(done)
		writeToolSearchResponse(w, response, nil)
	}()
	if _, err := io.Copy(upstream, bytes.NewBufferString("data: {\"type\":\"response.created\"}\n\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if w.writes != 1 {
			t.Fatal("writer tried to send a diagnostic after client disconnected", w.writes)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer kept reading upstream after downstream failure")
	}
}
