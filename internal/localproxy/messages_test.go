package localproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

func parsed(t *testing.T, s string) object {
	t.Helper()
	o, err := decodeObject([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestMessagesRequestConversionPreservesToolsAndImages(t *testing.T) {
	body := `{"model":"public/model","stream":true,"system":[{"type":"text","text":"first","cache_control":{"type":"ephemeral"}},{"type":"text","text":"second"}],"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},{"type":"document","source":{"type":"url","url":"https://example.com/file.pdf"}}]},{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"call_1","name":"read","input":{"number":9007199254740993}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"result"},{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}],"tools":[{"name":"read","description":"Read file","input_schema":{"type":"object","properties":{"number":{"type":"integer"}},"required":[]}}],"tool_choice":{"type":"any","disable_parallel_tool_use":true},"max_tokens":1,"output_config":{"effort":"max","format":{"type":"json_schema","schema":{"type":"object","properties":{}}}}}`
	encoded, stream, err := messagesToResponses([]byte(body), provider.Model{ID: "gpt-6.1-sol", Reasoning: true, ThinkingEfforts: []string{"low", "medium", "high", "xhigh"}})
	if err != nil || !stream {
		t.Fatal(err)
	}
	o := parsed(t, string(encoded))
	if text(o["model"]) != "gpt-6.1-sol" || text(o["instructions"]) != "first\n\nsecond" || number(o["max_output_tokens"]) != 16 || text(obj(o["reasoning"])["effort"]) != "xhigh" || o["store"] != false {
		t.Fatal(string(encoded))
	}
	input := array(o["input"])
	if len(input) != 4 {
		t.Fatal(string(encoded))
	}
	parts := array(obj(input[0])["content"])
	if text(obj(parts[1])["image_url"]) != "data:image/png;base64,AAAA" || text(obj(parts[2])["file_url"]) != "https://example.com/file.pdf" {
		t.Fatal(parts)
	}
	if text(obj(input[2])["call_id"]) != "call_1" || text(obj(input[2])["arguments"]) != `{"number":9007199254740993}` {
		t.Fatal(input)
	}
	output := array(obj(input[3])["output"])
	if len(output) != 2 || text(obj(output[1])["type"]) != "input_image" {
		t.Fatal(output)
	}
	tool := obj(array(o["tools"])[0])
	if tool["strict"] != false || len(array(obj(tool["parameters"])["required"])) != 0 || o["parallel_tool_calls"] != false || o["tool_choice"] != "required" {
		t.Fatal(string(encoded))
	}
	if strings.Contains(string(encoded), "cache_control") || text(obj(obj(o["text"])["format"])["type"]) != "json_schema" {
		t.Fatal(string(encoded))
	}
}

const responsesReply = `{"id":"resp_fixture","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Consider"}],"encrypted_content":"opaque-reasoning"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"number\":9007199254740993}"}],"usage":{"input_tokens":1000,"output_tokens":50,"input_tokens_details":{"cached_tokens":800}}}`

func TestResponsesReplyAndReasoningRoundTrip(t *testing.T) {
	response := parsed(t, responsesReply)
	message, err := responsesToMessage(response, "github-copilot/gpt-6.1-sol")
	if err != nil {
		t.Fatal(err)
	}
	content := array(message["content"])
	if message["model"] != "github-copilot/gpt-6.1-sol" || message["stop_reason"] != "tool_use" || len(content) != 3 {
		t.Fatal(message)
	}
	u := obj(message["usage"])
	if u["input_tokens"] != 200 || u["cache_read_input_tokens"] != 800 || u["output_tokens"] != 50 {
		t.Fatal(u)
	}
	next, _ := json.Marshal(object{"model": "public", "messages": []any{object{"role": "assistant", "content": content}, object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "call_1", "content": "done"}}}}})
	encoded, _, err := messagesToResponses(next, provider.Model{ID: "gpt-6.1-sol"})
	if err != nil {
		t.Fatal(err)
	}
	input := array(parsed(t, string(encoded))["input"])
	if !reflect.DeepEqual(obj(input[0]), obj(array(response["output"])[0])) {
		t.Fatal("encrypted reasoning changed", string(encoded))
	}
	if text(obj(input[2])["call_id"]) != "call_1" || text(obj(input[2])["arguments"]) != `{"number":9007199254740993}` || text(obj(input[3])["output"]) != "done" {
		t.Fatal(string(encoded))
	}
}

func TestMessagesRequestConversionPreservesTurnInstructions(t *testing.T) {
	// Claude Code sends environment text and per-turn effort as system messages
	// inside messages, including empty messages that only carry output_config.
	body := `{"system":"Stable instructions","output_config":{"effort":"high"},"messages":[
		{"role":"user","content":"Read the file"},
		{"role":"system","content":[{"type":"text","text":"# Environment","cache_control":{"type":"ephemeral"}}],"output_config":{"effort":"high"}},
		{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"File contents"}]},
		{"role":"system","content":"Remember the instructions"},
		{"role":"developer","content":[{"type":"text","text":"Be concise"}]},
		{"role":"system","content":[],"output_config":{"effort":"low"}},
		{"role":"system","content":[{"type":"text","text":""}]}
	]}`
	model := provider.Model{ID: "mai-code-2-flash", ThinkingEfforts: []string{"low", "medium", "high"}}
	encoded, _, err := messagesToResponses([]byte(body), model)
	if err != nil {
		t.Fatal(err)
	}
	got := parsed(t, string(encoded))
	want := parsed(t, `{"input":[
		{"role":"user","content":[{"type":"input_text","text":"Read the file"}]},
		{"role":"system","content":[{"type":"input_text","text":"# Environment"}]},
		{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"File contents"},
		{"role":"system","content":[{"type":"input_text","text":"Remember the instructions"}]},
		{"role":"developer","content":[{"type":"input_text","text":"Be concise"}]}
	]}`)
	if !reflect.DeepEqual(got["input"], want["input"]) || got["instructions"] != "Stable instructions" || text(obj(got["reasoning"])["effort"]) != "low" {
		t.Fatal(string(encoded))
	}
	if strings.Contains(string(encoded), "output_config") || strings.Contains(string(encoded), "cache_control") {
		t.Fatal("Anthropic-only fields sent to Responses", string(encoded))
	}

	// Keep new instructions in place rather than invalidating the cached prefix.
	src := parsed(t, body)
	src["messages"] = append(array(src["messages"]), object{"role": "system", "content": "Next turn context"})
	next, _ := json.Marshal(src)
	encoded, _, err = messagesToResponses(next, model)
	if err != nil {
		t.Fatal(err)
	}
	nextGot := parsed(t, string(encoded))
	if !reflect.DeepEqual(array(nextGot["input"])[:len(array(got["input"]))], got["input"]) || nextGot["instructions"] != got["instructions"] {
		t.Fatal("per-turn instructions changed the previous prompt prefix", string(encoded))
	}
	encoded, _, err = messagesToResponses(next, provider.Model{ID: model.ID})
	if err != nil || parsed(t, string(encoded))["reasoning"] != nil {
		t.Fatal("effort enabled reasoning on a model without advertised efforts", err, string(encoded))
	}
}

func TestMessagesRequestRejectsInvalidRolesAndInstructionContent(t *testing.T) {
	for _, message := range []string{
		`null`,
		`{"content":"missing role"}`,
		`{"role":"tool","content":"wrong protocol"}`,
		`{"role":"unknown","content":"bad"}`,
		`{"role":"system","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}`,
		`{"role":"developer","content":[{"type":"tool_result","tool_use_id":"c1","content":"bad"}]}`,
	} {
		_, _, err := messagesToResponses([]byte(`{"messages":[{"role":"user","content":"hi"},`+message+`]}`), provider.Model{ID: "test"})
		if err == nil || !strings.Contains(err.Error(), "messages[1]") {
			t.Fatalf("invalid message %s: want indexed validation error, got %v", message, err)
		}
	}
}

func TestClaudeCodeTurnSystemMessagesThroughGateway(t *testing.T) {
	for _, endpoint := range []string{"/responses", "/v1/messages"} {
		t.Run(endpoint, func(t *testing.T) {
			calls := make(chan provider.RawRequest, 4)
			client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
				calls <- r
				res := response(responsesReply)
				res.Header.Set("Content-Type", "application/json")
				return res, nil
			}}
			s := New("fixture-key")
			s.Update(map[string]Target{"p1": {Client: client}}, map[string]Profile{"fixture": {ID: "fixture-id", Models: []Model{
				{ProviderID: "p1", ID: "github-copilot/mai-code-2-flash", Model: provider.Model{ID: "mai-code-2-flash", Endpoint: endpoint}},
			}}})
			if err := s.Start(0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			base := "http://" + s.Address() + "/fixture/v1/messages"
			headers := map[string]string{"X-Api-Key": "fixture-key", "X-Claude-Code-Session-Id": "fixture-session"}
			var conversation string
			for _, test := range []struct {
				messages string
				agent    bool
			}{
				{`{"role":"user","content":"Hi"},{"role":"system","content":[{"type":"text","text":"# Environment"}]}`, false},
				{`{"role":"user","content":"Hi"},{"role":"system","content":[],"output_config":{"effort":"low"}}`, false},
				{`{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"result"}]},{"role":"system","content":"Reminder"}`, true},
				{`{"role":"user","content":"Hi"},{"role":"developer","content":"Reminder"}`, false},
			} {
				res := send(t, "POST", base, `{"model":"github-copilot/mai-code-2-flash","system":"Stable instructions","messages":[`+test.messages+`]}`, headers)
				data, _ := io.ReadAll(res.Body)
				if res.StatusCode != 200 {
					t.Fatal(res.Status, string(data))
				}
				call := <-calls
				if call.Endpoint != endpoint || call.AgentInitiated != test.agent || call.ConversationID == "" || (conversation != "" && call.ConversationID != conversation) {
					t.Fatalf("wrong routing/session/initiator: endpoint=%s agent=%v conversation=%s", call.Endpoint, call.AgentInitiated, call.ConversationID)
				}
				conversation = call.ConversationID
				upstream := parsed(t, string(call.Body))
				if upstream["model"] != "mai-code-2-flash" {
					t.Fatal("public model prefix reached upstream", upstream["model"])
				}
				if endpoint == "/v1/messages" && upstream["messages"] == nil {
					t.Fatal("native Messages unexpectedly converted")
				}
			}
		})
	}
}

func sse(values ...string) string {
	var out strings.Builder
	for _, value := range values {
		fmt.Fprintf(&out, "data: %s\n\n", value)
	}
	return out.String()
}

func TestResponsesStreamAndJSONFallback(t *testing.T) {
	upstream := sse(
		`{"type":"response.created","response":{"id":"resp_fixture"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"Consider"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Hel"}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"lo"}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"number\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"9007199254740993}"}`,
		`{"type":"response.completed","response":`+responsesReply+`}`,
	)
	for _, jsonFallback := range []bool{false, true} {
		t.Run(fmt.Sprint(jsonFallback), func(t *testing.T) {
			res := response(upstream)
			if jsonFallback {
				res.Header.Set("Content-Type", "application/json")
				res.Body = io.NopCloser(strings.NewReader(responsesReply))
			}
			w := httptest.NewRecorder()
			writeMessagesResponse(w, res, "github-copilot/gpt-6.1-sol", true)
			var events []object
			if err := readResponseEvents(strings.NewReader(w.Body.String()), func(e object) error { events = append(events, e); return nil }); err != nil {
				t.Fatal(err)
			}
			texts := map[int]string{}
			starts, stops := 0, 0
			reason := ""
			signature := ""
			for _, e := range events {
				switch text(e["type"]) {
				case "content_block_start":
					starts++
				case "content_block_stop":
					stops++
				case "error":
					t.Fatal(e)
				case "content_block_delta":
					d := obj(e["delta"])
					texts[number(e["index"])] += text(d["text"]) + text(d["thinking"]) + text(d["partial_json"])
					signature += text(d["signature"])
				case "message_delta":
					reason = text(obj(e["delta"])["stop_reason"])
					u := obj(e["usage"])
					if number(u["input_tokens"]) != 200 || number(u["cache_read_input_tokens"]) != 800 {
						t.Fatal(u)
					}
				}
			}
			if starts != 3 || stops != 3 || reason != "tool_use" || text(events[len(events)-1]["type"]) != "message_stop" {
				t.Fatal(w.Body.String())
			}
			if texts[0] != "Consider" || texts[1] != "Hello" || texts[2] != `{"number":9007199254740993}` || !strings.HasPrefix(signature, reasoningSignaturePrefix) {
				t.Fatal(texts, signature)
			}
		})
	}
}

func TestResponsesFailuresAreNotSuccessfulEmptyReplies(t *testing.T) {
	for _, body := range []string{
		sse(`{"type":"response.output_text.delta","output_index":0,"delta":"partial"}`),
		sse(`{"type":"response.failed","response":{"status":"failed","error":{"message":"quota exceeded"}}}`),
		sse(`{"type":"error","message":"broken"}`),
		"data: broken\n\n",
		sse(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`),
		sse(`{"type":"response.completed","response":{"status":"completed"}}`),
	} {
		w := httptest.NewRecorder()
		writeMessagesResponse(w, response(body), "public", true)
		if !strings.Contains(w.Body.String(), "event: error") || strings.Contains(w.Body.String(), "event: message_stop") {
			t.Fatal(w.Body.String())
		}
	}
	res := response(`{"status":"failed","error":{"message":"quota exceeded"}}`)
	res.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	writeMessagesResponse(w, res, "public", false)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "quota exceeded") {
		t.Fatal(w.Code, w.Body.String())
	}
	res.StatusCode = 429
	res.Body = io.NopCloser(strings.NewReader(`{"error":{"message":"slow down"}}`))
	w = httptest.NewRecorder()
	writeMessagesResponse(w, res, "public", true)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "slow down") || w.Header().Get("Content-Type") != "application/json" {
		t.Fatal(w)
	}
}

func TestStreamLateToolArgumentsAndMultipleThinkingParts(t *testing.T) {
	upstream := sse(
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"First"}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"Second"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_late","summary":[{"type":"summary_text","text":"First"},{"type":"summary_text","text":"Second"}],"encrypted_content":"opaque"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_late"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"x\":"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_late","call_id":"call_late","name":"read","arguments":"{\"x\":42}"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs_late","summary":[{"type":"summary_text","text":"First"},{"type":"summary_text","text":"Second"}],"encrypted_content":"opaque"},{"type":"function_call","id":"fc_late","call_id":"call_late","name":"read","arguments":"{\"x\":42}"}]}}`,
	)
	w := httptest.NewRecorder()
	writeMessagesResponse(w, response(upstream), "public", true)
	if strings.Contains(w.Body.String(), "event: error") || !strings.Contains(w.Body.String(), `"id":"call_late"`) || !strings.Contains(w.Body.String(), `"partial_json":"{\"x\":42}"`) || !strings.Contains(w.Body.String(), "event: message_stop") {
		t.Fatal(w.Body.String())
	}
	// Non-streaming callers receive a JSON message even when the provider
	// responds with SSE. The terminal event must finish without waiting for EOF.
	w = httptest.NewRecorder()
	writeMessagesResponse(w, response(upstream), "public", false)
	message := parsed(t, w.Body.String())
	if message["stop_reason"] != "tool_use" || text(obj(array(message["content"])[0])["thinking"]) != "First\nSecond" {
		t.Fatal(w.Body.String())
	}
}

func TestMessagesConversionValidationAndEffortBounds(t *testing.T) {
	model := provider.Model{ID: "gpt", MaxOutputTokens: 1024, ThinkingEfforts: []string{"none", "low", "high"}}
	for _, test := range []struct{ config, want string }{
		{`"output_config":{"effort":"max"}`, "high"},
		{`"output_config":{"effort":"medium"}`, "low"},
		{`"thinking":{"type":"disabled"}`, "none"},
		{`"thinking":{"type":"enabled","budget_tokens":16000}`, "high"},
	} {
		data, _, err := messagesToResponses([]byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":50000,`+test.config+`}`), model)
		if err != nil {
			t.Fatal(err)
		}
		p := parsed(t, string(data))
		if number(p["max_output_tokens"]) != 1024 || text(obj(p["reasoning"])["effort"]) != test.want {
			t.Fatal(string(data))
		}
	}
	for _, body := range []string{
		`{"messages":[]}`,
		`{"messages":[{"role":"user","content":[{"type":"unknown"}]}]}`,
		`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
		`{"messages":[{"role":"user","content":"hi"}],"system":42}`,
	} {
		if _, _, err := messagesToResponses([]byte(body), model); err == nil {
			t.Fatal("invalid or unsupported request silently accepted", body)
		}
	}
	if _, err := decodeObject([]byte(`{} {}`)); err == nil {
		t.Fatal("multiple JSON objects accepted")
	}
}

func TestMessagesBridgeRoutesAndRetainsConversation(t *testing.T) {
	calls := make(chan provider.RawRequest, 5)
	client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
		calls <- r
		res := response(responsesReply)
		res.Header.Set("Content-Type", "application/json")
		return res, nil
	}}
	_, base := fixture(t, client)
	h := map[string]string{"X-Api-Key": "fixture-key", "X-Claude-Code-Session-Id": "thread-1", "Anthropic-Beta": "private-beta"}
	for _, stream := range []bool{false, true} {
		res := send(t, "POST", base+"/v1/messages?beta=true", fmt.Sprintf(`{"model":"gpt-fixture","stream":%v,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}]}`, stream), h)
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || !strings.Contains(string(data), "cache_read_input_tokens") {
			t.Fatal(res.Status, string(data))
		}
	}
	a, b := <-calls, <-calls
	if a.Endpoint != "/responses" || a.ConversationID != b.ConversationID || !a.AgentInitiated || !a.Vision || a.Header.Get("Anthropic-Beta") != "" || a.RawQuery != "" {
		t.Fatal(a, b)
	}
	res := send(t, "POST", base+"/v1/messages/count_tokens", `{"model":"gpt-fixture"}`, h)
	if res.StatusCode != 400 {
		t.Fatal("token counting accepted a request without messages", res.Status)
	}
}

func TestConvertedStreamFlushAndCancellation(t *testing.T) {
	cancelled := make(chan struct{})
	client := &rawClient{fn: func(ctx context.Context, _ provider.RawRequest) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			io.WriteString(writer, sse(`{"type":"response.created","response":{"id":"resp_live"}}`))
			<-ctx.Done()
			close(cancelled)
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	}}
	s, base := fixture(t, client)
	res := send(t, "POST", base+"/v1/messages", `{"model":"gpt-fixture","stream":true,"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"X-Api-Key": "fixture-key"})
	first := make([]byte, len("event: message_start"))
	if _, err := io.ReadFull(res.Body, first); err != nil || string(first) != "event: message_start" {
		t.Fatal(string(first), err)
	}
	s.Update(nil, nil)
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge failed to cancel revoked provider")
	}
}
