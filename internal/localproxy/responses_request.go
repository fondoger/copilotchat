package localproxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
)

type responsesBridge struct {
	custom          map[string]bool
	pendingSearches map[string]bool
	tools           *toolSearchContext
}

func updatePendingSearches(pending map[string]bool, block object) error {
	switch text(block["type"]) {
	case "server_tool_use":
		id := text(block["id"])
		if text(block["name"]) != "web_search" || id == "" || pending[id] {
			return errors.New("invalid or duplicate Anthropic search call")
		}
		pending[id] = true
	case "web_search_tool_result":
		id := text(block["tool_use_id"])
		if !pending[id] {
			return errors.New("Anthropic search result has no matching pending search call")
		}
		delete(pending, id)
	}
	return nil
}

const anthropicReplayPrefix = "copilotchat-anthropic-v1:"

// This is an opaque transport envelope, not encryption or an Anthropic signature.
// Keep the actual signed/server blocks intact, alongside their visible translation.
func anthropicReplay(blocks []any, output []any) (string, error) {
	data, err := json.Marshal(object{"content": blocks, "output": output})
	if err != nil {
		return "", err
	}
	if len(data) > MaxBody/2 {
		return "", errors.New("Anthropic replay state exceeds the bridge limit")
	}
	return anthropicReplayPrefix + base64.RawStdEncoding.EncodeToString(data), nil
}

func replayItem(value any) any {
	item := obj(value)
	kind := text(item["type"])
	if kind == "" {
		kind = "message"
	}
	switch kind {
	case "message":
		content := item["content"]
		if s, ok := content.(string); ok {
			content = []any{object{"type": "output_text", "text": s}}
		}
		parts := []any{}
		for _, p := range array(content) {
			part := obj(p)
			if text(part["type"]) == "refusal" {
				parts = append(parts, object{"refusal": part["refusal"]})
			} else {
				parts = append(parts, object{"text": part["text"]})
			}
		}
		return object{"type": kind, "role": item["role"], "content": parts}
	case "function_call":
		args, err := decodeObject([]byte(text(item["arguments"])))
		if err != nil {
			return nil
		}
		return object{"type": kind, "call_id": item["call_id"], "namespace": text(item["namespace"]), "name": item["name"], "arguments": args}
	case "tool_search_call":
		if text(item["execution"]) != "client" || obj(item["arguments"]) == nil {
			return nil
		}
		return object{"type": kind, "call_id": item["call_id"], "execution": item["execution"], "arguments": item["arguments"]}
	case "custom_tool_call":
		return object{"type": kind, "call_id": item["call_id"], "namespace": text(item["namespace"]), "name": item["name"], "input": item["input"]}
	case "web_search_call":
		return object{"type": kind, "id": item["id"], "action": item["action"]}
	default:
		return nil
	}
}

func restoreAnthropicInput(input []any) ([]any, error) {
	restored := []any{}
	for _, v := range input {
		item := obj(v)
		if text(item["type"]) == "anthropic_replay" {
			return nil, errors.New("internal replay items cannot appear in Responses input")
		}
		if text(item["type"]) != "reasoning" {
			restored = append(restored, v)
			continue
		}
		encoded := text(item["encrypted_content"])
		if !strings.HasPrefix(encoded, anthropicReplayPrefix) {
			return nil, errors.New("reasoning history is not an Anthropic bridge replay envelope")
		}
		data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, anthropicReplayPrefix))
		if err != nil || len(data) > MaxBody/2 {
			return nil, errors.New("invalid Anthropic replay envelope")
		}
		state, err := decodeObject(data)
		if err != nil || array(state["content"]) == nil || array(state["output"]) == nil {
			return nil, errors.New("invalid Anthropic replay state")
		}
		visible := array(state["output"])
		start := len(restored) - len(visible)
		if start < 0 {
			return nil, errors.New("Anthropic replay is missing its visible output")
		}
		for i, expected := range visible {
			key := replayItem(expected)
			if key == nil || !reflect.DeepEqual(key, replayItem(restored[start+i])) {
				return nil, errors.New("Anthropic replay does not match its visible output")
			}
		}
		for _, b := range array(state["content"]) {
			switch text(obj(b)["type"]) {
			case "text", "thinking", "redacted_thinking", "tool_use", "server_tool_use", "web_search_tool_result":
			default:
				return nil, errors.New("unsupported block in Anthropic replay state")
			}
		}
		restored = append(restored[:start], object{"type": "anthropic_replay", "content": state["content"]})
	}
	return restored, nil
}

func responsesToMessages(body []byte, model provider.Model) ([]byte, bool, *responsesBridge, error) {
	src, err := decodeObject(body)
	if err != nil {
		return nil, false, nil, err
	}
	stream, _ := src["stream"].(bool)
	bridge := &responsesBridge{custom: map[string]bool{}}
	fail := func(err error) ([]byte, bool, *responsesBridge, error) { return nil, false, nil, err }
	// Client-only hints (store/include/cache keys/metadata) need no upstream state.
	for _, key := range []string{"previous_response_id", "conversation", "context_management", "max_tool_calls"} {
		if src[key] != nil {
			return fail(fmt.Errorf("Responses %s is not supported by the Messages bridge", key))
		}
	}
	if src["background"] == true || text(src["truncation"]) == "auto" {
		return fail(errors.New("background execution and automatic truncation are not supported by the Messages bridge"))
	}
	cache, err := newBridgeCache(src, false)
	if err != nil {
		return fail(err)
	}
	if err := cache.validateResponses(src); err != nil {
		return fail(err)
	}
	bridge.tools, err = normalizeResponsesTools(src, true)
	if err != nil {
		return fail(err)
	}
	dst := object{"model": model.ID, "stream": stream}
	budget := number(src["max_output_tokens"])
	if src["max_output_tokens"] != nil && budget <= 0 {
		return fail(errors.New("max_output_tokens must be a positive integer"))
	}
	if budget <= 0 {
		budget = 8192
	}
	if model.MaxOutputTokens > 0 {
		budget = min(budget, model.MaxOutputTokens)
	}
	dst["max_tokens"] = budget
	if instructions := src["instructions"]; instructions != nil {
		if _, ok := instructions.(string); !ok {
			return fail(errors.New("instructions must be text"))
		}
		dst["system"] = instructions
	}
	for _, key := range []string{"temperature", "top_p"} {
		if src[key] != nil {
			dst[key] = src[key]
		}
	}
	if format := obj(obj(src["text"])["format"]); format != nil && text(format["type"]) != "text" {
		if text(format["type"]) != "json_schema" || !model.StructuredOutputs {
			return fail(errors.New("model does not support this structured output format"))
		}
		dst["output_config"] = object{"format": object{"type": "json_schema", "schema": format["schema"]}}
	}
	if effort := text(obj(src["reasoning"])["effort"]); effort != "" && effort != "none" {
		if model.AdaptiveThinking && slices.Contains(model.ThinkingEfforts, effort) {
			dst["thinking"] = object{"type": "adaptive", "display": "summarized"}
			config := obj(dst["output_config"])
			if config == nil {
				config = object{}
				dst["output_config"] = config
			}
			config["effort"] = effort
		} else if !model.AdaptiveThinking && model.MaxThinkingBudget > 0 {
			n := map[string]int{"minimal": 1024, "low": 1024, "medium": 4096, "high": 8192, "xhigh": 16384, "max": 32768}[effort]
			if n == 0 {
				return fail(fmt.Errorf("unsupported reasoning effort %q", effort))
			}
			n = min(max(n, max(1024, model.MinThinkingBudget)), model.MaxThinkingBudget, budget-1)
			if n < max(1024, model.MinThinkingBudget) {
				return fail(errors.New("output budget is too small for thinking"))
			}
			dst["thinking"] = object{"type": "enabled", "budget_tokens": n}
		} else {
			return fail(fmt.Errorf("model does not advertise reasoning effort %q", effort))
		}
	}
	tools := []any{}
	if src["tools"] != nil && array(src["tools"]) == nil {
		return fail(errors.New("tools must be an array"))
	}
	names := map[string]bool{}
	search := false
	for _, v := range array(src["tools"]) {
		tool := obj(v)
		kind, name := text(tool["type"]), text(tool["name"])
		switch kind {
		case "web_search", "web_search_preview":
			name = "web_search"
			converted := object{"type": "web_search_20250305", "name": name}
			for key, value := range tool {
				if value == nil || key == "type" {
					continue
				}
				switch key {
				case "external_web_access":
					if value != true {
						return fail(errors.New("cached-only web search is not supported by the Messages bridge"))
					}
				case "user_location":
					location := obj(value)
					if text(location["type"]) != "approximate" {
						return fail(errors.New("only approximate search locations are supported"))
					}
					copy := object{"type": "approximate"}
					for k, v := range location {
						if !slices.Contains([]string{"type", "city", "region", "country", "timezone"}, k) {
							return fail(fmt.Errorf("unsupported search location field %q", k))
						}
						copy[k] = v
					}
					converted[key] = copy
				case "filters":
					filters := obj(value)
					if len(filters) != 1 || array(filters["allowed_domains"]) == nil {
						return fail(errors.New("only allowed_domains search filters are supported"))
					}
					converted["allowed_domains"] = filters["allowed_domains"]
				default:
					return fail(fmt.Errorf("web search option %q is not supported by the Messages bridge", key))
				}
			}
			tools = append(tools, converted)
			search = true
		case "function", "custom":
			if name == "" {
				return fail(errors.New("tool needs a name"))
			}
			converted := object{"name": name, "description": text(tool["description"])}
			if tool["strict"] == true {
				if !model.StructuredOutputs {
					return fail(errors.New("model does not advertise strict tool schemas"))
				}
				converted["strict"] = true
			}
			if kind == "function" {
				converted["input_schema"] = tool["parameters"]
				if obj(tool["parameters"]) == nil {
					return fail(errors.New("function parameters must be a JSON schema object"))
				}
			} else {
				bridge.custom[name] = true
				converted["input_schema"] = object{"type": "object", "properties": object{"input": object{"type": "string"}}, "required": []string{"input"}, "additionalProperties": false}
				if format := obj(tool["format"]); format != nil && text(format["type"]) != "text" {
					if text(format["type"]) != "grammar" || !slices.Contains([]string{"lark", "regex"}, text(format["syntax"])) {
						return fail(errors.New("unsupported custom tool format"))
					}
					converted["description"] = text(tool["description"]) + "\nReturn the raw tool input in the input string. Input format (" + text(format["syntax"]) + "):\n" + text(format["definition"])
				}
			}
			tools = append(tools, converted)
		default:
			return fail(fmt.Errorf("Responses tool %q is not supported by the Messages bridge", kind))
		}
		if names[name] {
			return fail(fmt.Errorf("duplicate or conflicting tool name %q", name))
		}
		names[name] = true
	}
	if len(tools) > 0 {
		dst["tools"] = tools
	}
	choice := src["tool_choice"]
	if choice == nil {
		choice = "auto"
	}
	mapped := object{}
	if s, ok := choice.(string); ok {
		switch s {
		case "auto", "none":
			mapped["type"] = s
		case "required":
			mapped["type"] = "any"
		default:
			return fail(errors.New("unsupported tool_choice"))
		}
	} else {
		c := obj(choice)
		kind, name := text(c["type"]), text(c["name"])
		if kind == "web_search" || kind == "web_search_preview" {
			if !search {
				return fail(errors.New("search tool_choice requires web_search"))
			}
			name = "web_search"
		} else if kind != "function" && kind != "custom" {
			return fail(errors.New("unsupported tool_choice"))
		}
		if !names[name] {
			return fail(errors.New("tool_choice names an undeclared tool"))
		}
		mapped = object{"type": "tool", "name": name}
	}
	if len(tools) > 0 {
		if src["parallel_tool_calls"] == false {
			mapped["disable_parallel_tool_use"] = true
		}
		if dst["thinking"] != nil && (mapped["type"] == "tool" || mapped["type"] == "any") {
			return fail(errors.New("forced tool_choice cannot be combined with Anthropic thinking"))
		}
		dst["tool_choice"] = mapped
	}
	input := array(src["input"])
	if s, ok := src["input"].(string); ok {
		input = []any{object{"role": "user", "content": s}}
	}
	if input == nil {
		return fail(errors.New("input must be text or an array"))
	}
	messages := []any{}
	appendBlocks := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if len(messages) > 0 && obj(messages[len(messages)-1])["role"] == role {
			last := obj(messages[len(messages)-1])
			last["content"] = append(array(last["content"]), blocks...)
		} else {
			messages = append(messages, object{"role": role, "content": blocks})
		}
	}
	pending := map[string]bool{}
	seenCalls := map[string]bool{}
	register := func(block object) error {
		id := text(block["id"])
		if id == "" || seenCalls[id] {
			return errors.New("tool call has an empty or duplicate call ID")
		}
		seenCalls[id] = true
		pending[id] = true
		return nil
	}
	for _, v := range input {
		item := obj(v)
		kind := text(item["type"])
		switch kind {
		case "anthropic_replay":
			if len(pending) > 0 {
				return fail(errors.New("assistant turn precedes pending tool results"))
			}
			for _, b := range array(item["content"]) {
				if obj(b)["type"] == "tool_use" {
					if err := register(obj(b)); err != nil {
						return fail(err)
					}
				}
			}
			messages = append(messages, object{"role": "assistant", "content": item["content"]})
		case "", "message":
			role := text(item["role"])
			if role == "system" || role == "developer" {
				if len(messages) > 0 {
					return fail(errors.New("turn-local system/developer messages cannot be represented faithfully by Messages; use instructions"))
				}
				blocks, err := anthropicContent(item["content"], "assistant", cache)
				if err != nil {
					return fail(err)
				}
				if hasCacheMarker(blocks, "cache_control") || array(dst["system"]) != nil {
					previous := array(dst["system"])
					if s, ok := dst["system"].(string); ok && s != "" {
						previous = []any{object{"type": "text", "text": s}}
					}
					dst["system"] = append(previous, blocks...)
				} else {
					instruction, err := systemText(blocks)
					if err != nil {
						return fail(err)
					}
					if previous := text(dst["system"]); previous != "" {
						instruction = previous + "\n\n" + instruction
					}
					dst["system"] = instruction
				}
				continue
			}
			if role != "user" && role != "assistant" {
				return fail(errors.New("message role must be user or assistant"))
			}
			if role == "user" && len(pending) > 0 {
				return fail(errors.New("user message precedes pending tool results"))
			}
			content, err := anthropicContent(item["content"], role, cache)
			if err != nil {
				return fail(err)
			}
			messages = append(messages, object{"role": role, "content": content})
		case "function_call", "custom_tool_call":
			var block object
			if kind == "function_call" {
				block, err = responseTool(item)
			} else {
				if text(item["call_id"]) == "" || text(item["name"]) == "" {
					return fail(errors.New("custom tool call needs call_id and name"))
				}
				if _, ok := item["input"].(string); !ok {
					return fail(errors.New("custom tool input must be text"))
				}
				block = object{"type": "tool_use", "id": item["call_id"], "name": item["name"], "input": object{"input": item["input"]}}
			}
			if err != nil {
				return fail(err)
			}
			if err := register(block); err != nil {
				return fail(err)
			}
			appendBlocks("assistant", []any{block})
		case "function_call_output", "custom_tool_call_output":
			id := text(item["call_id"])
			if !pending[id] {
				return fail(errors.New("tool output has no matching call"))
			}
			delete(pending, id)
			result := item["output"]
			if _, ok := result.(string); !ok {
				result, err = anthropicContent(result, "user", cache)
				if err != nil {
					return fail(err)
				}
			}
			block := object{"type": "tool_result", "tool_use_id": id, "content": result}
			if parts := array(result); len(parts) > 0 {
				last := obj(parts[len(parts)-1])
				if marker, exists := last["cache_control"]; exists {
					block["cache_control"] = marker
					delete(last, "cache_control")
				}
			}
			appendBlocks("user", []any{block})
		case "web_search_call":
			return fail(errors.New("web search history requires its Anthropic replay envelope"))
		default:
			return fail(fmt.Errorf("Responses input item %q is not supported by the Messages bridge", kind))
		}
	}
	if len(messages) == 0 || len(pending) > 0 {
		return fail(errors.New("input needs a conversation with completed client tool results"))
	}
	// Search calls can wait for client tools and receive their results in a
	// later response. Reconstruct that state from history, never server storage.
	bridge.pendingSearches = map[string]bool{}
	for _, value := range messages {
		message := obj(value)
		if message["role"] != "assistant" {
			continue
		}
		for _, block := range array(message["content"]) {
			if err := updatePendingSearches(bridge.pendingSearches, obj(block)); err != nil {
				return fail(err)
			}
		}
	}
	if len(bridge.pendingSearches) > 0 && !search {
		return fail(errors.New("pending Anthropic searches require the web_search tool"))
	}
	dst["messages"] = messages
	if err := cache.apply(dst); err != nil {
		return fail(err)
	}
	data, err := json.Marshal(dst)
	return data, stream, bridge, err
}

func anthropicContent(value any, role string, cache *bridgeCache) ([]any, error) {
	if s, ok := value.(string); ok {
		return []any{object{"type": "text", "text": s}}, nil
	}
	if array(value) == nil {
		return nil, errors.New("content must be text or an array")
	}
	blocks := []any{}
	for _, v := range array(value) {
		p := obj(v)
		switch text(p["type"]) {
		case "input_text", "output_text", "text":
			blocks = append(blocks, object{"type": "text", "text": text(p["text"])})
		case "refusal":
			blocks = append(blocks, object{"type": "text", "text": text(p["refusal"])})
		case "input_image":
			if role != "user" {
				return nil, errors.New("images require a user message")
			}
			u := text(p["image_url"])
			if strings.HasPrefix(u, "data:") {
				parts := strings.SplitN(strings.TrimPrefix(u, "data:"), ";base64,", 2)
				if len(parts) != 2 || !strings.HasPrefix(parts[0], "image/") {
					return nil, errors.New("invalid image data URL")
				}
				if _, err := base64.StdEncoding.DecodeString(parts[1]); err != nil {
					return nil, errors.New("invalid image base64")
				}
				blocks = append(blocks, object{"type": "image", "source": object{"type": "base64", "media_type": parts[0], "data": parts[1]}})
			} else if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
				blocks = append(blocks, object{"type": "image", "source": object{"type": "url", "url": u}})
			} else {
				return nil, errors.New("input_image requires a URL or image data URL")
			}
		default:
			return nil, fmt.Errorf("Responses content %q cannot be converted to Messages", text(p["type"]))
		}
		if marker, exists := p["prompt_cache_breakpoint"]; exists {
			converted, err := cacheMarker(marker, false, cache.ttl)
			if err != nil {
				return nil, err
			}
			obj(blocks[len(blocks)-1])["cache_control"] = converted
		}
	}
	return blocks, nil
}
