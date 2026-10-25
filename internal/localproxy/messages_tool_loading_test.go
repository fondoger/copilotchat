package localproxy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestMessagesDeferredToolsLoadFromReferences(t *testing.T) {
	body := `{
		"tools":[
			{"name":"discover_anything","description":"Find tools","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}},
			{"name":"read_file","defer_loading":true,"description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"},"large":{"const":9007199254740993}},"required":["path"],"additionalProperties":false}},
			{"name":"unused","defer_loading":true,"input_schema":{"type":"object"}},
			{"name":"DeferredToolPlaceholder","input_schema":{"type":"object"}}
		],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"s1","name":"discover_anything","input":{"query":"read"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"s1","cache_control":{"type":"ephemeral","ttl":"1h"},"content":[
				{"type":"text","text":"before"},
				{"type":"tool_reference","tool_name":"read_file"},
				{"type":"text","text":"between"},
				{"type":"tool_reference","tool_name":"read_file"},
				{"type":"text","text":"after"}
			]}]}
		]}`
	encoded, _, err := messagesToResponses([]byte(body), provider.Model{ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	got := parsed(t, string(encoded))
	tools := array(got["tools"])
	if len(tools) != 2 || obj(tools[0])["name"] != "discover_anything" || obj(tools[1])["name"] != "read_file" {
		t.Fatal("search entry must remain an ordinary function; only referenced tools load", tools)
	}
	original := parsed(t, body)
	if !reflect.DeepEqual(obj(tools[1])["parameters"], obj(array(original["tools"])[1])["input_schema"]) || obj(tools[1])["description"] != "Read a file" {
		t.Fatal("full current tool schema/description must be retained", tools)
	}
	input := array(got["input"])
	output := array(obj(input[1])["output"])
	if len(output) != 5 || obj(output[0])["text"] != "before" || obj(output[2])["text"] != "between" || obj(output[4])["text"] != "after" {
		t.Fatal("mixed tool result order changed", output)
	}
	for _, index := range []int{1, 3} {
		if obj(output[index])["type"] != "input_text" || !strings.Contains(text(obj(output[index])["text"]), "read_file") {
			t.Fatal("reference was not replaced in place with loading information", output)
		}
	}
	if !reflect.DeepEqual(obj(output[4])["prompt_cache_breakpoint"], object{"mode": "explicit", "ttl": "12h"}) {
		t.Fatal("tool result cache boundary lost", output)
	}
	if strings.Contains(string(encoded), "DeferredToolPlaceholder") || strings.Contains(string(encoded), "tool_reference") || strings.Contains(string(encoded), "defer_loading") {
		t.Fatal("internal loading protocol leaked", string(encoded))
	}
}

func TestMessagesDeferredToolsWithoutReferences(t *testing.T) {
	body := `{"tools":[{"name":"lookup","input_schema":{"type":"object"}},{"name":"later","defer_loading":true,"input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}`
	encoded, _, err := messagesToResponses([]byte(body), provider.Model{ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	got := parsed(t, string(encoded))
	if tools := array(got["tools"]); len(tools) != 1 || obj(tools[0])["name"] != "lookup" {
		t.Fatal("unreferenced tool was exposed", string(encoded))
	}
	// A non-deferred tool called ToolSearch is not a special server tool.
	body = `{"tools":[{"name":"ToolSearch","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}`
	encoded, _, err = messagesToResponses([]byte(body), provider.Model{ID: "test"})
	if err != nil || obj(array(parsed(t, string(encoded))["tools"])[0])["type"] != "function" {
		t.Fatal("tool name must not choose a protocol", string(encoded), err)
	}
}

func TestMessagesToolLoadingRejectsUnknownReferencesAndHiddenCacheMarkers(t *testing.T) {
	for _, test := range []struct {
		name, tools, content, want string
	}{
		{"unknown", `[]`, `[{"type":"tool_reference","tool_name":"missing"}]`, "unknown tool reference"},
		{"missing name", `[]`, `[{"type":"tool_reference"}]`, "unknown tool reference"},
		{"placeholder", `[{"name":"DeferredToolPlaceholder"}]`, `[{"type":"tool_reference","tool_name":"DeferredToolPlaceholder"}]`, "unknown tool reference"},
		{"unloaded marker", `[{"name":"later","defer_loading":true,"cache_control":{"type":"ephemeral"}}]`, `[{"type":"text","text":"ok"}]`, "tools[0]"},
		{"loaded marker", `[{"name":"later","defer_loading":true,"cache_control":{"type":"ephemeral"}}]`, `[{"type":"tool_reference","tool_name":"later"}]`, "tools[0]"},
		{"placeholder marker", `[{"name":"DeferredToolPlaceholder","cache_control":{"type":"ephemeral"}}]`, `[{"type":"text","text":"ok"}]`, "tools[0]"},
		{"reference marker", `[{"name":"later","defer_loading":true}]`, `[{"type":"tool_reference","tool_name":"later","cache_control":{"type":"invalid"}}]`, "cache_control"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"tools":` + test.tools + `,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"s1","content":` + test.content + `}]}]}`
			_, _, err := messagesToResponses([]byte(body), provider.Model{ID: "test"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestMessagesToolLoadingLeavesOrdinaryResultsUnchanged(t *testing.T) {
	body := `{"stream":true,"tools":[{"name":"find","description":"An ordinary tool","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"ordinary","is_error":true,"cache_control":{"type":"ephemeral"},"content":[{"type":"text","text":"first"},{"type":"image","source":{"type":"url","url":"https://example.com/result.png"}},{"type":"text","text":"last"}]}]}]}`
	src := parsed(t, body)
	original := parsed(t, body)
	tools, err := resolveMessagesTools(src)
	if err != nil || !reflect.DeepEqual(src, original) || !reflect.DeepEqual(tools, original["tools"]) {
		t.Fatal("ordinary request mutated by loading pass", src, err)
	}
	encoded, stream, err := messagesToResponses([]byte(body), provider.Model{ID: "test"})
	if err != nil || !stream {
		t.Fatal("ordinary result rejected", string(encoded), err)
	}
	output := array(obj(array(parsed(t, string(encoded))["input"])[0])["output"])
	if len(output) != 4 || obj(output[0])["text"] != "[Tool error]" || obj(output[1])["text"] != "first" || obj(output[2])["image_url"] != "https://example.com/result.png" || obj(output[3])["text"] != "last" || obj(obj(output[3])["prompt_cache_breakpoint"])["mode"] != "explicit" {
		t.Fatal("ordinary mixed result/cache boundary changed", output)
	}
}

func TestMessagesToolStrictCapability(t *testing.T) {
	for _, supported := range []bool{false, true} {
		for _, strict := range []string{"", `,"strict":false`, `,"strict":true`, `,"strict":"true"`} {
			body := `{"tools":[{"name":"read","input_schema":{"type":"object","properties":{},"additionalProperties":false}` + strict + `}],"messages":[{"role":"user","content":"hi"}]}`
			encoded, _, err := messagesToResponses([]byte(body), provider.Model{ID: "test", StructuredOutputs: supported})
			wantStrict := strict == `,"strict":true`
			if strict == `,"strict":"true"` || (wantStrict && !supported) {
				if err == nil {
					t.Fatal("unsupported strictness accepted", body)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if obj(array(parsed(t, string(encoded))["tools"])[0])["strict"] != wantStrict {
				t.Fatal("tool strictness was not preserved", string(encoded))
			}
		}
	}
}

func TestMessagesBridgeRejectsUnsupportedExperimentalSemantics(t *testing.T) {
	for _, key := range []string{"context_management", "container", "mcp_servers", "inference_geo", "speed"} {
		body, _ := json.Marshal(object{key: object{}, "messages": []any{object{"role": "user", "content": "hi"}}})
		if _, _, err := messagesToResponses(body, provider.Model{ID: "test"}); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s silently discarded: %v", key, err)
		}
	}
	body := `{"tools":[{"name":"run","allowed_callers":["code_execution_20260120"]}],"messages":[{"role":"user","content":"hi"}]}`
	if _, _, err := messagesToResponses([]byte(body), provider.Model{ID: "test"}); err == nil || !strings.Contains(err.Error(), "allowed_callers") {
		t.Fatal("programmatic execution semantics silently discarded", err)
	}
}
