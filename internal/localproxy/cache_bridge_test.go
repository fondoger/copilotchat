package localproxy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestCacheBridgeStableClassifierPrefix(t *testing.T) {
	for _, ttl := range []string{"", "5m", "1h"} {
		t.Run(ttl, func(t *testing.T) {
			marker := object{"type": "ephemeral"}
			if ttl != "" {
				marker["ttl"] = ttl
			}
			src := object{"system": []any{
				object{"type": "text", "text": "fixed header"},
				object{"type": "text", "text": "shared safety rules", "cache_control": marker},
				object{"type": "text", "text": "uncached suffix"},
			}, "messages": []any{object{"role": "user", "content": "operation A"}}, "stop_sequences": []any{"</severity>"}}
			encoded, _ := json.Marshal(src)
			forward, _, err := messagesToResponses(encoded, provider.Model{ID: "gpt-unlisted"})
			if err != nil {
				t.Fatal(err)
			}
			got := parsed(t, string(forward))
			input := array(got["input"])
			parts := array(obj(input[0])["content"])
			if got["instructions"] != nil || obj(input[0])["role"] != "developer" || len(parts) != 3 || obj(parts[0])["prompt_cache_breakpoint"] != nil || obj(parts[2])["prompt_cache_breakpoint"] != nil {
				t.Fatal("cache boundary moved", string(forward))
			}
			want := object{"mode": "explicit"}
			if ttl == "1h" {
				want["ttl"] = "12h"
			}
			if !reflect.DeepEqual(obj(parts[1])["prompt_cache_breakpoint"], want) || !reflect.DeepEqual(got["prompt_cache_options"], object{"mode": "explicit"}) {
				t.Fatal("incorrect caching policy", string(forward))
			}
			obj(array(src["messages"])[0])["content"] = "operation B"
			encoded, _ = json.Marshal(src)
			next, _, err := messagesToResponses(encoded, provider.Model{ID: "gpt-unlisted"})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(obj(array(parsed(t, string(next))["input"])[0]), obj(input[0])) {
				t.Fatal("operation changed stable prefix")
			}
			back, _, _, err := responsesToMessages(forward, provider.Model{ID: "claude-fixture"})
			if err != nil {
				t.Fatal(err)
			}
			restored := parsed(t, string(back))
			wantSystem := array(src["system"])
			normalized := object{"type": "ephemeral"}
			if ttl == "1h" {
				normalized["ttl"] = "1h"
			}
			obj(wantSystem[1])["cache_control"] = normalized
			if !reflect.DeepEqual(restored["system"], wantSystem) || restored["cache_control"] != nil {
				t.Fatal("system boundary did not round trip", string(back))
			}
		})
	}
}

func TestCacheBridgeTextAndToolResults(t *testing.T) {
	src := `{"messages":[{"role":"user","content":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral"}},{"type":"text","text":"variable"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"read","input":{"cache_control":"ordinary argument"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"result","cache_control":{"type":"ephemeral"}}]}]}`
	encoded, _, err := messagesToResponses([]byte(src), provider.Model{ID: "gpt"})
	if err != nil {
		t.Fatal(err)
	}
	got := parsed(t, string(encoded))
	items := array(got["input"])
	result := obj(items[len(items)-1])
	if result["prompt_cache_breakpoint"] != nil || obj(array(result["output"])[0])["prompt_cache_breakpoint"] == nil {
		t.Fatal("result boundary must be on output text", string(encoded))
	}
	back, _, _, err := responsesToMessages(encoded, provider.Model{ID: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	messages := array(parsed(t, string(back))["messages"])
	last := obj(array(obj(messages[2])["content"])[0])
	if obj(last["cache_control"])["type"] != "ephemeral" || obj(array(last["content"])[0])["cache_control"] != nil || text(obj(array(last["content"])[0])["text"]) != "result" {
		t.Fatal("result breakpoint misplaced", string(back))
	}
	first := array(obj(messages[0])["content"])
	if obj(first[0])["cache_control"] == nil || obj(first[1])["cache_control"] != nil {
		t.Fatal("user breakpoint moved", string(back))
	}
}

func TestCacheBridgeAutomaticPolicies(t *testing.T) {
	for _, ttl := range []string{"", "1h"} {
		src := object{"cache_control": object{"type": "ephemeral"}, "messages": []any{object{"role": "user", "content": "hi"}}}
		if ttl != "" {
			obj(src["cache_control"])["ttl"] = ttl
		}
		body, _ := json.Marshal(src)
		encoded, _, err := messagesToResponses(body, provider.Model{ID: "gpt"})
		if err != nil {
			t.Fatal(err)
		}
		options := obj(parsed(t, string(encoded))["prompt_cache_options"])
		if options["mode"] != "implicit" || (ttl == "1h" && options["ttl"] != "12h") {
			t.Fatal(string(encoded))
		}
		back, _, _, err := responsesToMessages(encoded, provider.Model{ID: "claude"})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(parsed(t, string(back))["cache_control"], src["cache_control"]) {
			t.Fatal("automatic policy lost", string(back))
		}
	}
	src := `{"cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"prefix","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`
	encoded, _, err := messagesToResponses([]byte(src), provider.Model{ID: "gpt"})
	if err != nil {
		t.Fatal(err)
	}
	back, _, _, err := responsesToMessages(encoded, provider.Model{ID: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	got := parsed(t, string(back))
	if obj(got["cache_control"])["type"] != "ephemeral" || obj(obj(array(got["system"])[0])["cache_control"])["ttl"] != "1h" {
		t.Fatal("mixed automatic and explicit policy lost", string(back))
	}
	// Inherited request TTL must apply to markers that omit their own TTL.
	body := `{"instructions":"header","prompt_cache_options":{"mode":"explicit","ttl":"12h"},"input":[{"role":"developer","content":[{"type":"input_text","text":"prefix","prompt_cache_breakpoint":{"mode":"explicit"}}]},{"role":"developer","content":"suffix"},{"role":"user","content":"hi"}]}`
	back, _, _, err = responsesToMessages([]byte(body), provider.Model{ID: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	system := array(parsed(t, string(back))["system"])
	if len(system) != 3 || text(obj(system[0])["text"]) != "header" || text(obj(system[2])["text"]) != "suffix" || obj(obj(system[1])["cache_control"])["ttl"] != "1h" {
		t.Fatal("inherited lifetime or instructions lost", string(back))
	}
}

func TestCacheBridgeAutomaticFinalBreakpoint(t *testing.T) {
	blocks := []any{}
	for i := 0; i < 4; i++ {
		blocks = append(blocks, object{"type": "text", "text": "prefix", "cache_control": object{"type": "ephemeral"}})
	}
	src := object{"cache_control": object{"type": "ephemeral"}, "messages": []any{object{"role": "user", "content": blocks}}}
	body, _ := json.Marshal(src)
	forward, _, err := messagesToResponses(body, provider.Model{ID: "gpt"})
	if err != nil {
		t.Fatal("automatic cache should share final explicit slot", err)
	}
	if _, _, _, err := responsesToMessages(forward, provider.Model{ID: "claude"}); err != nil {
		t.Fatal(err)
	}
	// A different lifetime at the automatic endpoint cannot be represented.
	src["cache_control"] = object{"type": "ephemeral", "ttl": "1h"}
	body, _ = json.Marshal(src)
	if _, _, err := messagesToResponses(body, provider.Model{ID: "gpt"}); err == nil {
		t.Fatal("accepted conflicting final cache TTL")
	}
}

func TestCacheBridgeLeavesUnflaggedRequestsAlone(t *testing.T) {
	forward, _, err := messagesToResponses([]byte(`{"system":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"messages":[{"role":"user","content":"hi"}]}`), provider.Model{ID: "gpt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(forward), "prompt_cache_") || parsed(t, string(forward))["instructions"] != "a\n\nb" {
		t.Fatal(string(forward))
	}
	for _, options := range []string{``, `,"prompt_cache_options":{"mode":"explicit"}`} {
		back, _, _, err := responsesToMessages([]byte(`{"instructions":"prefix","input":"hi","prompt_cache_key":"routing-hint"`+options+`}`), provider.Model{ID: "claude"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(back), "cache_control") || parsed(t, string(back))["system"] != "prefix" {
			t.Fatal(string(back))
		}
	}
}

func TestCacheBridgeRejectsUnrepresentableControls(t *testing.T) {
	messages := []string{
		`{"system":[{"type":"text","text":"prefix","cache_control":{"type":"persistent"}}],"messages":[{"role":"user","content":"hi"}]}`,
		`{"cache_control":{"type":"ephemeral","ttl":"12h"},"messages":[{"role":"user","content":"hi"}]}`,
		`{"tools":[{"name":"read","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"},"cache_control":{"type":"ephemeral"}}]}]}`,
		`{"system":[{"type":"text","text":"","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`,
		`{"system":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`,
	}
	for _, body := range messages {
		if _, _, err := messagesToResponses([]byte(body), provider.Model{ID: "gpt"}); err == nil {
			t.Fatal("accepted unsupported cache intent", body)
		}
	}
	responses := []string{
		`{"input":"hi","prompt_cache_options":{"mode":"other"}}`,
		`{"input":"hi","prompt_cache_options":{"mode":"implicit","prewarm":true}}`,
		`{"input":"hi","prompt_cache_options":{"ttl":"1h"}}`,
		`{"input":"hi","prompt_cache_options":null}`,
		`{"input":"hi","tools":[{"type":"function","name":"f","parameters":{},"prompt_cache_breakpoint":{"mode":"explicit"}}]}`,
		`{"input":[{"role":"assistant","content":[{"type":"output_text","text":"hi","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_text","text":"hi","prompt_cache_breakpoint":{"mode":"explicit","ttl":"5m"}}]}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_text","text":"hi","prompt_cache_breakpoint":null}]}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque","prompt_cache_breakpoint":{"mode":"explicit"}}]}`,
	}
	for _, body := range responses {
		if _, _, _, err := responsesToMessages([]byte(body), provider.Model{ID: "claude"}); err == nil {
			t.Fatal("accepted unsupported cache intent", body)
		}
	}
	for _, count := range []int{4, 5} {
		blocks := []any{}
		for i := 0; i < count; i++ {
			blocks = append(blocks, object{"type": "text", "text": "prefix", "cache_control": object{"type": "ephemeral"}})
		}
		body, _ := json.Marshal(object{"system": blocks, "messages": []any{object{"role": "user", "content": "hi"}}})
		converted, _, err := messagesToResponses(body, provider.Model{ID: "gpt"})
		if count == 5 {
			if err == nil {
				t.Fatal("accepted five writes")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := responsesToMessages(converted, provider.Model{ID: "claude"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCacheBridgeReplayDoesNotDiscardAddedMarkers(t *testing.T) {
	blocks := []any{object{"type": "thinking", "thinking": "hidden", "signature": "real-signature"}, object{"type": "text", "text": "answer"}}
	visible := []any{object{"type": "message", "role": "assistant", "content": []any{object{"type": "output_text", "text": "answer"}}}}
	replay, err := anthropicReplay(blocks, visible)
	if err != nil {
		t.Fatal(err)
	}
	input := append(visible, object{"type": "reasoning", "encrypted_content": replay}, object{"role": "user", "content": "next"})
	src := object{"input": input, "prompt_cache_options": object{"mode": "explicit"}}
	body, _ := json.Marshal(src)
	converted, _, _, err := responsesToMessages(body, provider.Model{ID: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(obj(array(parsed(t, string(converted))["messages"])[0])["content"], blocks) {
		t.Fatal("signed replay changed", string(converted))
	}
	obj(array(obj(visible[0])["content"])[0])["prompt_cache_breakpoint"] = object{"mode": "explicit"}
	body, _ = json.Marshal(src)
	if _, _, _, err := responsesToMessages(body, provider.Model{ID: "claude"}); err == nil || !strings.Contains(err.Error(), "cannot be represented") {
		t.Fatal("silently discarded replay marker", err)
	}
}
