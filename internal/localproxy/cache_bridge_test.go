package localproxy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestImplicitCacheKeepsToolResultShapeAcrossTurns(t *testing.T) {
	src := parsed(t, `{"system":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"r","content":"result","cache_control":{"type":"ephemeral"}}]}]}`)
	convert := func() object {
		data, _, err := messagesToResponses(encodedDiscovery(t, src), provider.Model{ID: "gpt-test"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "prompt_cache_breakpoint") {
			t.Fatal(string(data))
		}
		return parsed(t, string(data))
	}
	first := convert()
	if obj(first["prompt_cache_options"])["mode"] != "implicit" || obj(array(first["input"])[0])["output"] != "result" {
		t.Fatal(first)
	}
	delete(obj(array(obj(array(src["messages"])[0])["content"])[0]), "cache_control")
	src["messages"] = append(array(src["messages"]), object{"role": "user", "content": []any{object{"type": "text", "text": "next", "cache_control": object{"type": "ephemeral"}}}})
	next := convert()
	if !reflect.DeepEqual(first["instructions"], next["instructions"]) || !reflect.DeepEqual(first["input"], array(next["input"])[:1]) {
		t.Fatal("moving cache marker changed prefix", next)
	}
}

func TestImplicitCacheAcceptsClientMarkerPositions(t *testing.T) {
	const marker = `,"cache_control":{"type":"ephemeral"}`
	for _, test := range []struct {
		name, body string
	}{
		{"assistant text", `{"messages":[{"role":"assistant","content":[{"type":"text","text":"answer","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":"Summarize this conversation"}]}`},
		{"tool call", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"read","input":{"cache_control":"keep"},"cache_control":{"type":"ephemeral"}}]}]}`},
		{"media", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"},"cache_control":{"type":"ephemeral"}},{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"},"cache_control":{"type":"ephemeral"}}]}]}`},
		{"tool result parts", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"text","text":"result","cache_control":{"type":"ephemeral"}},{"type":"image","source":{"type":"url","url":"https://example.com/a.png"},"cache_control":{"type":"ephemeral"}}]}]}]}`},
		{"tool definitions", `{"tools":[{"name":"read","input_schema":{"type":"object","properties":{"cache_control":{"type":"string"}}},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"deferred tools", `{"tools":[{"name":"read","defer_loading":true,"input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},{"name":"unused","defer_loading":true,"input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},{"name":"DeferredToolPlaceholder","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"s","content":[{"type":"tool_reference","tool_name":"read"}]}]}]}`},
		{"hosted search", `{"tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"Find a tool"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := provider.Model{ID: "gpt-test"}
			encoded, _, err := messagesToResponses([]byte(test.body), model)
			if err != nil {
				t.Fatal(err)
			}
			got := parsed(t, string(encoded))
			if !reflect.DeepEqual(got["prompt_cache_options"], object{"mode": "implicit"}) {
				t.Fatal("client marker did not select implicit caching", string(encoded))
			}
			unmarked := strings.ReplaceAll(test.body, marker, "")
			baseline, _, err := messagesToResponses([]byte(unmarked), model)
			if err != nil {
				t.Fatal(err)
			}
			delete(got, "prompt_cache_options")
			if !reflect.DeepEqual(got, parsed(t, string(baseline))) {
				t.Fatal("cache markers changed prompt content or tool arguments", string(encoded), string(baseline))
			}
			if _, _, err := messagesToResponsesWithCache([]byte(test.body), model, true); err == nil || !strings.Contains(err.Error(), "cannot be represented") {
				t.Fatal("explicit bridge accepted an unsupported boundary", err)
			}
		})
	}
}

func TestImplicitCacheRejectsInvalidClientMarkers(t *testing.T) {
	for _, marker := range []string{
		`null`, `"ephemeral"`, `[]`, `{"type":"persistent"}`,
		`{"type":"ephemeral","ttl":"12h"}`, `{"type":"ephemeral","ttl":5}`,
		`{"type":"ephemeral","unexpected":true}`,
	} {
		for _, body := range []string{
			`{"cache_control":MARKER,"messages":[{"role":"user","content":"hi"}]}`,
			`{"tools":[{"name":"unused","defer_loading":true,"cache_control":MARKER}],"messages":[{"role":"user","content":"hi"}]}`,
			`{"messages":[{"role":"assistant","content":[{"type":"text","text":"answer","cache_control":MARKER}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"text","text":"result","cache_control":MARKER}]}]}]}`,
		} {
			body = strings.ReplaceAll(body, "MARKER", marker)
			if _, _, err := messagesToResponses([]byte(body), provider.Model{ID: "gpt-test"}); err == nil {
				t.Fatal("invalid source cache marker was discarded", body)
			}
		}
	}
	body := `{"system":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"assistant","content":[{"type":"text","text":"answer","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`
	if _, _, err := messagesToResponses([]byte(body), provider.Model{ID: "gpt-test"}); err == nil || !strings.Contains(err.Error(), "long cache TTL") {
		t.Fatal("invalid source TTL order was discarded", err)
	}
}

func TestImplicitCacheKeepsSearchReplayWithClientMarkers(t *testing.T) {
	response := searchResponseFixture(t, "server")
	message, err := responsesToMessage(response, "public", &messagesSearchContext{mode: "server", name: "tool_search_tool_regex"})
	if err != nil {
		t.Fatal(err)
	}
	blocks := array(message["content"])
	for _, block := range blocks[1:] {
		obj(block)["cache_control"] = object{"type": "ephemeral"}
	}
	src := object{
		"tools":    []any{object{"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"}},
		"messages": []any{message, object{"role": "user", "content": "Summarize this conversation"}},
	}
	model := provider.Model{ID: "gpt-test"}
	encoded, _, err := messagesToResponses(encodedDiscovery(t, src), model)
	if err != nil {
		t.Fatal(err)
	}
	input := array(parsed(t, string(encoded))["input"])
	if len(input) != 3 || !reflect.DeepEqual(input[:2], response["output"]) {
		t.Fatal("client cache markers changed native search history", string(encoded))
	}
	obj(blocks[1])["input"] = object{"pattern": "changed"}
	if _, _, err := messagesToResponses(encodedDiscovery(t, src), model); err == nil || !strings.Contains(err.Error(), "visible blocks differ") {
		t.Fatal("cache normalization hid an edited search replay", err)
	}
}

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
			forward, _, err := messagesToResponsesWithCache(encoded, provider.Model{ID: "gpt-unlisted"}, true)
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
			next, _, err := messagesToResponsesWithCache(encoded, provider.Model{ID: "gpt-unlisted"}, true)
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
	encoded, _, err := messagesToResponsesWithCache([]byte(src), provider.Model{ID: "gpt"}, true)
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
		encoded, _, err := messagesToResponsesWithCache(body, provider.Model{ID: "gpt"}, true)
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
	encoded, _, err := messagesToResponsesWithCache([]byte(src), provider.Model{ID: "gpt"}, true)
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
	forward, _, err := messagesToResponsesWithCache(body, provider.Model{ID: "gpt"}, true)
	if err != nil {
		t.Fatal("automatic cache should share final explicit slot", err)
	}
	if _, _, _, err := responsesToMessages(forward, provider.Model{ID: "claude"}); err != nil {
		t.Fatal(err)
	}
	// A different lifetime at the automatic endpoint cannot be represented.
	src["cache_control"] = object{"type": "ephemeral", "ttl": "1h"}
	body, _ = json.Marshal(src)
	if _, _, err := messagesToResponsesWithCache(body, provider.Model{ID: "gpt"}, true); err == nil {
		t.Fatal("accepted conflicting final cache TTL")
	}
}

func TestCacheBridgeLeavesUnflaggedRequestsAlone(t *testing.T) {
	forward, _, err := messagesToResponsesWithCache([]byte(`{"system":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"messages":[{"role":"user","content":"hi"}]}`), provider.Model{ID: "gpt"}, true)
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
		if _, _, err := messagesToResponsesWithCache([]byte(body), provider.Model{ID: "gpt"}, true); err == nil {
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
		converted, _, err := messagesToResponsesWithCache(body, provider.Model{ID: "gpt"}, true)
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
