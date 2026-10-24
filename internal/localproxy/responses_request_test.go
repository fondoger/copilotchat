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

const reverseTools = `[{"type":"function","name":"read","parameters":{"type":"object","properties":{"offset":{"type":"integer"}}}},{"type":"custom","name":"apply_patch","description":"Apply edits","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}},{"type":"web_search","external_web_access":true,"filters":{"allowed_domains":["example.com"]}}]`

func TestResponsesRequestToolsAndHistory(t *testing.T) {
	request := `{"instructions":"Keep the prefix","input":[{"role":"developer","content":"Initial permissions"},{"role":"user","content":[{"type":"input_text","text":"Fix this"},{"type":"input_image","image_url":"data:image/png;base64,aGk="}]},{"type":"function_call","call_id":"read1","name":"read","arguments":"{\"offset\":9007199254740993}"},{"type":"custom_tool_call","call_id":"patch1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},{"type":"function_call_output","call_id":"read1","output":"file"},{"type":"custom_tool_call_output","call_id":"patch1","output":"done"}],"tools":` + reverseTools + `,"tool_choice":{"type":"web_search"},"parallel_tool_calls":false,"max_output_tokens":12000,"store":false}`
	data, stream, bridge, err := responsesToMessages([]byte(request), provider.Model{ID: "claude", MaxOutputTokens: 10000})
	if err != nil {
		t.Fatal(err)
	}
	got := parsed(t, string(data))
	messages := array(got["messages"])
	tools := array(got["tools"])
	if stream || !bridge.custom["apply_patch"] || got["system"] != "Keep the prefix\n\nInitial permissions" || number(got["max_tokens"]) != 10000 || len(messages) != 3 || len(tools) != 3 {
		t.Fatal(string(data))
	}
	calls := array(obj(messages[1])["content"])
	if text(obj(obj(calls[0])["input"])["offset"]) != "" || number(obj(obj(calls[0])["input"])["offset"]) != 9007199254740993 || obj(obj(calls[1])["input"])["input"] != "*** Begin Patch\n*** End Patch" {
		t.Fatal("tool content changed", string(data))
	}
	if obj(tools[2])["type"] != "web_search_20250305" || obj(got["tool_choice"])["name"] != "web_search" || obj(got["tool_choice"])["disable_parallel_tool_use"] != true || obj(array(obj(messages[0])["content"])[1])["type"] != "image" {
		t.Fatal(string(data))
	}
}

func TestAnthropicReplayRestoresSignedSearchAndTools(t *testing.T) {
	message := parsed(t, `{"id":"msg_original","type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"Look first","signature":"real-signature"},{"type":"redacted_thinking","data":"real-redacted"},{"type":"server_tool_use","id":"srv1","name":"web_search","input":{"query":"example"}},{"type":"web_search_tool_result","tool_use_id":"srv1","content":[{"type":"web_search_result","url":"https://example.com","title":"Example","encrypted_content":"real-search-ciphertext"}]},{"type":"text","text":"Found it","citations":[{"type":"web_search_result_location","url":"https://example.com","title":"Example","encrypted_index":"real-index","cited_text":"it"}]},{"type":"tool_use","id":"read1","name":"read","input":{"offset":9007199254740993}},{"type":"tool_use","id":"patch1","name":"apply_patch","input":{"input":"raw patch"}}],"usage":{"input_tokens":10,"output_tokens":3}}`)
	model := provider.Model{ID: "claude"}
	initial := parsed(t, `{"input":[{"role":"user","content":"search"}],"tools":`+reverseTools+`}`)
	raw, _ := json.Marshal(initial)
	first, _, bridge, err := responsesToMessages(raw, model)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := messagesToResponse(message, "public/claude", bridge)
	if err != nil {
		t.Fatal(err)
	}
	input := append(array(initial["input"]), array(reply["output"])...)
	input = append(input, object{"type": "function_call_output", "call_id": "read1", "output": "file"}, object{"type": "custom_tool_call_output", "call_id": "patch1", "output": "done"})
	initial["input"] = input
	raw, _ = json.Marshal(initial)
	second, _, _, err := responsesToMessages(raw, model)
	if err != nil {
		t.Fatal(err, string(raw))
	}
	before := array(parsed(t, string(first))["messages"])
	after := array(parsed(t, string(second))["messages"])
	if len(after) != 3 || !reflect.DeepEqual(after[:len(before)], before) || !reflect.DeepEqual(obj(after[1])["content"], message["content"]) {
		t.Fatal("signed history changed, duplicated, or lost its prefix", string(second))
	}
	// Neither altered visible output nor a missing replay envelope can be silently accepted.
	altered := parsed(t, string(raw))
	items := array(altered["input"])
	for _, v := range items {
		if obj(v)["type"] == "message" && obj(v)["role"] == "assistant" {
			obj(array(obj(v)["content"])[0])["text"] = "changed"
			break
		}
	}
	encoded, _ := json.Marshal(altered)
	if _, _, _, err := responsesToMessages(encoded, model); err == nil {
		t.Fatal("modified replay accepted")
	}
	without := []any{}
	for _, v := range input {
		if obj(v)["type"] != "reasoning" {
			without = append(without, v)
		}
	}
	initial["input"] = without
	encoded, _ = json.Marshal(initial)
	if _, _, _, err := responsesToMessages(encoded, model); err == nil {
		t.Fatal("search state silently discarded")
	}
}

func TestResponsesRequestRejectsUnrepresentableSemantics(t *testing.T) {
	for _, request := range []string{
		`{"input":"hi","previous_response_id":"resp_old"}`,
		`{"input":"hi","background":true}`,
		`{"input":[{"type":"function_call_output","call_id":"unknown","output":"x"}]}`,
		`{"input":[{"role":"user","content":"hi"},{"role":"developer","content":"turn instructions"}]}`,
		`{"input":"hi","tools":[{"type":"web_search","external_web_access":false}]}`,
		`{"input":"hi","tools":[{"type":"web_search","search_context_size":"high"}]}`,
		`{"input":"hi","tools":[{"type":"web_search"},{"type":"function","name":"web_search","parameters":{}}]}`,
		`{"input":"hi","tools":[{"type":"function","name":"f","parameters":{},"strict":true}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"foreign"}]}`,
	} {
		if _, _, _, err := responsesToMessages([]byte(request), provider.Model{ID: "claude"}); err == nil {
			t.Fatal("unsupported semantics accepted", request)
		}
	}
	for _, model := range []provider.Model{
		{ID: "adaptive", AdaptiveThinking: true, ThinkingEfforts: []string{"medium"}},
		{ID: "budget", MinThinkingBudget: 1024, MaxThinkingBudget: 8000},
	} {
		data, _, _, err := responsesToMessages([]byte(`{"input":"hi","reasoning":{"effort":"medium"}}`), model)
		if err != nil || obj(parsed(t, string(data))["thinking"]) == nil {
			t.Fatal(err, string(data))
		}
	}
}

func TestResponsesBridgeRoutingAndNativeIsolation(t *testing.T) {
	calls := make(chan provider.RawRequest, 10)
	client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
		calls <- r
		body := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		if r.Endpoint == "/responses" {
			body = `{"id":"resp_native","object":"response","output":[],"status":"completed"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	s, base := fixture(t, client)
	s.Update(map[string]Target{"p1": {Client: client}}, map[string]Profile{"fixture": {ID: "fixture-id", Models: []Model{
		{ProviderID: "p1", ID: "public-claude", Model: provider.Model{ID: "upstream-claude", Endpoint: "/v1/messages"}},
		{ProviderID: "p1", ID: "dual", Model: provider.Model{ID: "upstream-dual", Endpoints: []string{"/responses", "/v1/messages"}}},
	}}})
	headers := map[string]string{"Authorization": "Bearer fixture-key", "Thread-Id": "one-thread", "OpenAI-Beta": "responses=experimental", "Anthropic-Beta": "foreign-client-beta"}
	var session string
	for _, path := range []string{"/responses", "/v1/responses"} {
		res := send(t, "POST", base+path+"?beta=true", `{"model":"public-claude","input":"hi"}`, headers)
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatal(res.Status, string(data))
		}
		got := parsed(t, string(data))
		request := <-calls
		if got["model"] != "public-claude" || request.Endpoint != "/v1/messages" || request.Header.Get("OpenAI-Beta") != "" || request.Header.Get("Anthropic-Beta") != "" || request.RawQuery != "" || parsed(t, string(request.Body))["model"] != "upstream-claude" {
			t.Fatal(got, request)
		}
		if session != "" && request.ConversationID != session {
			t.Fatal("thread affinity lost")
		}
		session = request.ConversationID
	}
	for _, path := range []string{"/responses/compact", "/v1/responses/input_tokens"} {
		res := send(t, "POST", base+path, `{"model":"public-claude","input":"hi"}`, headers)
		io.Copy(io.Discard, res.Body)
		if res.StatusCode != 400 {
			t.Fatal("auxiliary endpoint bridged", path, res.Status)
		}
	}
	raw := `{"model":"dual","input":"hi","future":9007199254740993}`
	res := send(t, "POST", base+"/responses", raw, headers)
	data, _ := io.ReadAll(res.Body)
	request := <-calls
	if parsed(t, string(data))["id"] != "resp_native" || request.Endpoint != "/responses" || request.Header.Get("OpenAI-Beta") == "" || !strings.Contains(string(request.Body), "9007199254740993") {
		t.Fatal("native route changed", string(data), request)
	}
	res = send(t, "GET", base+"/codex/models", "", headers)
	var catalog struct{ Models []struct{ Slug string } }
	if json.NewDecoder(res.Body).Decode(&catalog) != nil || len(catalog.Models) != 2 || catalog.Models[0].Slug != "public-claude" {
		t.Fatal("Messages model not selectable", catalog)
	}
	if len(calls) != 0 {
		t.Fatal("rejected request reached provider")
	}
}
