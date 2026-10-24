package localproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestMessagesSearchToolConversion(t *testing.T) {
	for _, choice := range []string{
		`{"type":"auto"}`, `{"type":"any"}`, `{"type":"tool","name":"web_search"}`, `{"type":"tool","name":"read"}`,
	} {
		body := `{"messages":[{"role":"user","content":"Search"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2,"cache_control":{"type":"ephemeral"}},{"name":"read","input_schema":{"type":"object"}}],"tool_choice":` + choice + `}`
		data, _, err := messagesToResponses([]byte(body), provider.Model{ID: "gpt-6.1-sol"})
		if err != nil {
			t.Fatal(err)
		}
		got := parsed(t, string(data))
		tools := array(got["tools"])
		if number(got["max_tool_calls"]) != 2 || obj(tools[0])["max_uses"] != nil || obj(tools[0])["max_tool_calls"] != nil {
			t.Fatal("search limit was not mapped to the Responses request", string(data))
		}
		if len(tools) != 2 || obj(tools[0])["type"] != "web_search" || obj(tools[1])["name"] != "read" || obj(tools[1])["type"] != "function" {
			t.Fatal(string(data))
		}
		c := parsed(t, choice)
		if c["type"] == "tool" {
			mapped := obj(got["tool_choice"])
			if c["name"] == "web_search" && mapped["type"] != "web_search" || c["name"] == "read" && (mapped["type"] != "function" || mapped["name"] != "read") {
				t.Fatal(string(data))
			}
		} else if c["type"] == "auto" && got["tool_choice"] != "auto" || c["type"] == "any" && got["tool_choice"] != "required" {
			t.Fatal(string(data))
		}
	}
	// A client-side function with the same name must still be a function.
	data, _, err := messagesToResponses([]byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"web_search"}],"tool_choice":{"type":"tool","name":"web_search"}}`), provider.Model{ID: "gpt"})
	if err != nil || obj(parsed(t, string(data))["tool_choice"])["type"] != "function" {
		t.Fatal(err, string(data))
	}
	for _, limit := range []string{`0`, `-1`, `1.5`, `"2"`, `true`, `9223372036854775808`} {
		body := `{"messages":[{"role":"user","content":"Search"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":` + limit + `}]}`
		if _, _, err := messagesToResponses([]byte(body), provider.Model{ID: "gpt"}); err == nil || !strings.Contains(err.Error(), "max_uses must be a positive integer") {
			t.Fatal("invalid search limit accepted", limit, err)
		}
	}
	for _, option := range []string{
		`"allowed_domains":["example.com"]`, `"blocked_domains":["example.com"]`,
		`"user_location":{"type":"approximate","country":"US"}`,
	} {
		body := `{"messages":[{"role":"user","content":"Search"}],"tools":[{"type":"web_search_20250305","name":"web_search",` + option + `}]}`
		if _, _, err := messagesToResponses([]byte(body), provider.Model{ID: "gpt"}); err == nil || !strings.Contains(err.Error(), "web_search option") {
			t.Fatal("search constraints silently dropped", option, err)
		}
	}
}

const searchReply = `{"id":"resp_search","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"example"}},{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"open_page","url":"https://example.com"}},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"搜索结果","annotations":[{"type":"url_citation","url":"https://example.com/one","title":"来源一","start_index":0,"end_index":4},{"type":"url_citation","url":"https://example.com/two","title":"Source two","start_index":0,"end_index":4}]}]}]}`

func TestSearchAnswersSourcesAndFollowUp(t *testing.T) {
	reply := parsed(t, searchReply)
	message := obj(array(reply["output"])[2])
	part := obj(array(message["content"])[0])
	part["annotations"] = array(part["annotations"])[:1] // The second source arrives only in the terminal response.
	item, _ := json.Marshal(message)
	upstream := sse(
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"in_progress"}}`,
		`{"type":"response.web_search_call.searching","output_index":0}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"example"}}}`,
		`{"type":"response.output_text.delta","output_index":2,"content_index":0,"delta":"搜索结果"}`,
		`{"type":"response.output_text.annotation.added","output_index":2,"content_index":0,"annotation":{"type":"url_citation","url":"https://example.com/one","title":"来源一"}}`,
		`{"type":"response.output_text.annotation.added","output_index":2,"content_index":0,"annotation":{"type":"url_citation","url":"https://example.com/event-only","title":"Stream source"}}`,
		`{"type":"response.output_item.done","output_index":2,"item":`+string(item)+`}`,
		`{"type":"response.completed","response":`+searchReply+`}`,
	)
	for _, transport := range []string{"json", "sse", "terminal-only"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", transport, stream), func(t *testing.T) {
				res := response(upstream)
				if transport == "json" {
					res.Header.Set("Content-Type", "application/json")
					res.Body = io.NopCloser(strings.NewReader(searchReply))
				} else if transport == "terminal-only" {
					res.Body = io.NopCloser(strings.NewReader(sse(`{"type":"response.completed","response":` + searchReply + `}`)))
				}
				w := httptest.NewRecorder()
				writeMessagesResponse(w, res, "gpt", stream)
				content := []any{}
				reason := ""
				if !stream {
					message := parsed(t, w.Body.String())
					content, reason = array(message["content"]), text(message["stop_reason"])
				} else {
					stopped := map[int]bool{}
					err := readResponseEvents(strings.NewReader(w.Body.String()), func(e object) error {
						i := number(e["index"])
						switch text(e["type"]) {
						case "error":
							t.Fatal(e)
						case "content_block_start":
							if i != len(content) || obj(e["content_block"])["type"] != "text" {
								t.Fatal("server search exposed as a client tool or invalid block", e)
							}
							content = append(content, obj(e["content_block"]))
						case "content_block_delta":
							if stopped[i] || i >= len(content) {
								t.Fatal("delta outside an open block", e)
							}
							b := obj(content[i])
							b["text"] = text(b["text"]) + text(obj(e["delta"])["text"])
						case "content_block_stop":
							if stopped[i] {
								t.Fatal("block stopped twice", e)
							}
							stopped[i] = true
						case "message_delta":
							reason = text(obj(e["delta"])["stop_reason"])
						}
						return nil
					})
					if err != nil || len(stopped) != len(content) || !strings.Contains(w.Body.String(), "event: message_stop") {
						t.Fatal(err, w.Body.String())
					}
				}
				var answer strings.Builder
				for _, value := range content {
					if obj(value)["type"] != "text" {
						t.Fatal("unexpected server history block", value)
					}
					answer.WriteString(text(obj(value)["text"]))
				}
				if reason != "end_turn" || !strings.Contains(answer.String(), "搜索结果") || strings.Count(answer.String(), "https://example.com/one") != 1 || strings.Count(answer.String(), "https://example.com/two") != 1 {
					t.Fatal(reason, answer.String())
				}
				if stream && transport == "sse" && strings.Count(answer.String(), "https://example.com/event-only") != 1 {
					t.Fatal("stream-only citation lost", answer.String())
				}
				next, _ := json.Marshal(object{"messages": []any{object{"role": "assistant", "content": content}, object{"role": "user", "content": "Tell me more about the first source"}}})
				data, _, err := messagesToResponses(next, provider.Model{ID: "gpt"})
				if err != nil || !strings.Contains(string(data), "https://example.com/one") || strings.Contains(string(data), "function_call") {
					t.Fatal("search answer cannot be replayed", err, string(data))
				}
			})
		}
	}
}

func TestSearchWithClientToolAndFailure(t *testing.T) {
	reply := parsed(t, searchReply)
	reply["output"] = append(array(reply["output"]), object{"type": "function_call", "call_id": "c1", "name": "read", "arguments": "{}"})
	message, err := responsesToMessage(reply, "gpt")
	if err != nil || message["stop_reason"] != "tool_use" {
		t.Fatal(message, err)
	}
	encoded, _ := json.Marshal(reply)
	w := httptest.NewRecorder()
	writeMessagesResponse(w, response(sse(`{"type":"response.completed","response":`+string(encoded)+`}`)), "gpt", true)
	if strings.Contains(w.Body.String(), "event: error") || !strings.Contains(w.Body.String(), `"stop_reason":"tool_use"`) || strings.Count(w.Body.String(), `"type":"tool_use"`) != 1 {
		t.Fatal(w.Body.String())
	}
	for _, terminal := range []string{"", `{"type":"response.failed","response":{"status":"failed","error":{"message":"search unavailable"}}}`} {
		upstream := sse(`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call"}}`)
		if terminal != "" {
			upstream += sse(terminal)
		}
		w := httptest.NewRecorder()
		writeMessagesResponse(w, response(upstream), "gpt", true)
		if !strings.Contains(w.Body.String(), "event: error") || strings.Contains(w.Body.String(), "event: message_stop") {
			t.Fatal("failed search reported as successful", w.Body.String())
		}
	}
}
