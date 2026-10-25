package localproxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
)

type object = map[string]any

func text(v any) string { s, _ := v.(string); return s }
func obj(v any) object  { o, _ := v.(map[string]any); return o }
func array(v any) []any { a, _ := v.([]any); return a }
func number(v any) int  { n, _ := v.(json.Number); i, _ := n.Int64(); return int(i) }
func decodeObject(data []byte) (object, error) {
	var o object
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber() // Tool arguments and schemas may contain integers above 2^53.
	err := d.Decode(&o)
	if err == nil && o == nil {
		err = errors.New("expected a JSON object")
	}
	if err == nil {
		var extra any
		if d.Decode(&extra) != io.EOF {
			err = errors.New("expected exactly one JSON object")
		}
	}
	return o, err
}

const reasoningSignaturePrefix = "copilotchat-responses:"

func reasoningSignature(item object) string {
	data, _ := json.Marshal(item)
	return reasoningSignaturePrefix + base64.RawStdEncoding.EncodeToString(data)
}

func restoreReasoning(signature string) (object, error) {
	if !strings.HasPrefix(signature, reasoningSignaturePrefix) {
		return nil, nil
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(signature, reasoningSignaturePrefix))
	if err != nil {
		return nil, errors.New("invalid Responses thinking signature")
	}
	item, err := decodeObject(data)
	if err != nil || text(item["type"]) != "reasoning" {
		return nil, errors.New("invalid Responses reasoning item")
	}
	return item, nil
}

// messagesToResponses translates the common client-side Messages contract.
// Ordinary conversations use implicit caching; classifier routes opt into
// explicit boundaries. Never insert timestamps or random input into prompts.
func messagesToResponses(body []byte, model provider.Model) ([]byte, bool, error) {
	return messagesToResponsesWithCache(body, model, false)
}

func messagesToResponsesWithCache(body []byte, model provider.Model, explicit bool) ([]byte, bool, error) {
	encoded, stream, _, err := convertMessagesRequest(body, model, explicit)
	return encoded, stream, err
}

func convertMessagesRequest(body []byte, model provider.Model, explicit bool) ([]byte, bool, *messagesSearchContext, error) {
	src, err := decodeObject(body)
	if err != nil {
		return nil, false, nil, err
	}
	stream, _ := src["stream"].(bool)
	if len(array(src["messages"])) == 0 {
		return nil, false, nil, errors.New("messages must be a non-empty array")
	}
	if err := validateMessagesBridgeFeatures(src); err != nil {
		return nil, false, nil, err
	}
	searchContext, err := detectMessagesSearch(src, model)
	if err != nil {
		return nil, false, nil, err
	}
	if err := restoreMessagesSearchReplay(src); err != nil {
		return nil, false, nil, err
	}
	var original object
	if searchContext != nil {
		original = copyMessagesObject(src)
	}
	loading, err := resolveMessagesTools(src)
	if err != nil {
		return nil, false, nil, err
	}
	cache, err := newBridgeCache(src, true)
	if err != nil {
		return nil, false, nil, err
	}
	if err := cache.validateMessages(src); err != nil {
		return nil, false, nil, err
	}
	if !explicit {
		removeMessagesCacheMarkers(src)
	}
	dst := object{"model": model.ID, "stream": stream, "store": false, "include": []string{"reasoning.encrypted_content"}}
	input := []any{}
	if system := src["system"]; system != nil {
		if hasCacheMarker(array(system), "cache_control") {
			parts := []any{}
			for _, value := range array(system) {
				block := obj(value)
				if text(block["type"]) != "text" {
					return nil, false, nil, errors.New("only text system blocks can be converted")
				}
				part, err := responsePart(block, "developer")
				if err != nil {
					return nil, false, nil, err
				}
				parts = append(parts, part)
			}
			input = append(input, object{"role": "developer", "content": parts})
		} else {
			instructions, err := systemText(system)
			if err != nil {
				return nil, false, nil, err
			}
			dst["instructions"] = instructions
		}
	}
	for index, value := range array(src["messages"]) {
		message := obj(value)
		role := text(message["role"])
		switch role {
		case "user", "assistant", "system", "developer":
		default:
			return nil, false, nil, fmt.Errorf("messages[%d].role %q is unsupported; expected user, assistant, system or developer", index, role)
		}
		instruction := role == "system" || role == "developer"
		parts := []any{}
		flush := func() {
			if len(parts) > 0 {
				input = append(input, object{"role": role, "content": parts})
				parts = []any{}
			}
		}
		content := message["content"]
		if s, ok := content.(string); ok {
			content = []any{object{"type": "text", "text": s}}
		}
		if array(content) == nil {
			return nil, false, nil, errors.New("message content must be text or an array")
		}
		for _, value := range array(content) {
			block := obj(value)
			if instruction {
				// Recent Claude Code puts environment/reminder text in system
				// messages between turns. Keep its priority and original position
				// so adding a turn doesn't rewrite the cached instruction prefix.
				if text(block["type"]) != "text" {
					return nil, false, nil, fmt.Errorf("messages[%d]: only text blocks are supported for role %q", index, role)
				}
				if text(block["text"]) == "" {
					continue // An effort-only system message has no prompt content.
				}
			}
			switch text(block["type"]) {
			case "text", "image", "document":
				part, err := responsePart(block, role)
				if err != nil {
					return nil, false, nil, err
				}
				parts = append(parts, part)
			case "responses_search_replay":
				flush()
				if searchContext == nil {
					return nil, false, nil, errors.New("native search replay requires its search declaration and a GPT Responses target")
				}
				input = append(input, array(block["items"])...)
			case "server_tool_use", "tool_search_tool_result":
				return nil, false, nil, errors.New("hosted tool search history requires its intact gateway replay marker")
			case "tool_use":
				flush()
				if role != "assistant" || text(block["id"]) == "" || text(block["name"]) == "" {
					return nil, false, nil, errors.New("invalid tool_use block")
				}
				args := block["input"]
				if args == nil {
					args = object{}
				}
				encoded, _ := json.Marshal(args)
				input = append(input, object{"type": "function_call", "call_id": block["id"], "name": block["name"], "arguments": string(encoded)})
			case "tool_result":
				flush()
				if text(block["tool_use_id"]) == "" {
					return nil, false, nil, errors.New("tool_result needs tool_use_id")
				}
				output, err := toolOutput(block)
				if err != nil {
					return nil, false, nil, err
				}
				input = append(input, object{"type": "function_call_output", "call_id": block["tool_use_id"], "output": output})
			case "thinking", "redacted_thinking":
				flush()
				item, err := restoreReasoning(text(block["signature"]))
				if err != nil {
					return nil, false, nil, err
				}
				if item != nil {
					input = append(input, item)
				}
			default:
				return nil, false, nil, fmt.Errorf("Messages content block %q cannot be converted to Responses", text(block["type"]))
			}
		}
		flush()
	}
	dst["input"] = input
	if n := number(src["max_tokens"]); n > 0 {
		if model.MaxOutputTokens > 0 {
			n = min(n, model.MaxOutputTokens)
		}
		dst["max_output_tokens"] = max(16, n)
	}
	// Reasoning models commonly reject sampling controls. Preserve them only
	// for models that don't advertise reasoning.
	if !model.Reasoning && len(model.ThinkingEfforts) == 0 {
		for _, k := range []string{"temperature", "top_p"} {
			if v, ok := src[k]; ok {
				dst[k] = v
			}
		}
	}
	if effort := responseEffort(src, model); effort != "" {
		dst["reasoning"] = object{"effort": effort, "summary": "auto"}
	}
	tools := []any{}
	search := false
	loadedTools := loading
	if searchContext != nil && searchContext.mode == "server" {
		loadedTools = array(src["tools"])
	}
	for _, value := range loadedTools {
		tool := obj(value)
		if tool["allowed_callers"] != nil {
			return nil, false, nil, errors.New("tool allowed_callers is not supported by the Responses bridge")
		}
		kind := text(tool["type"])
		if searchContext != nil && searchContext.mode == "server" && tool["name"] == searchContext.name {
			tools = append(tools, object{"type": "function", "name": tool["name"]}) // Replaced by the native declaration below.
			continue
		}
		if kind == "web_search_20250305" {
			if text(tool["name"]) != "web_search" {
				return nil, false, nil, errors.New("web_search_20250305 requires the name web_search")
			}
			for key, value := range tool {
				if key == "max_uses" && value != nil {
					limit, ok := value.(json.Number)
					n, err := limit.Int64()
					if !ok || err != nil || n <= 0 {
						return nil, false, nil, errors.New("web_search max_uses must be a positive integer")
					}
					// Responses limits all built-in tool calls, not just searches.
					dst["max_tool_calls"] = limit
					continue
				}
				if key != "type" && key != "name" && key != "cache_control" && value != nil {
					return nil, false, nil, fmt.Errorf("web_search option %q is not supported by the Responses bridge", key)
				}
			}
			tools = append(tools, object{"type": "web_search"})
			search = true
			continue
		}
		if kind != "" && kind != "custom" {
			return nil, false, nil, fmt.Errorf("server tool %q is not supported by the Responses bridge; use client-side tools", kind)
		}
		if text(tool["name"]) == "" {
			return nil, false, nil, errors.New("tool name is required")
		}
		schema := tool["input_schema"]
		if schema == nil {
			schema = object{"type": "object", "properties": object{}}
		}
		strict := false
		if value, exists := tool["strict"]; exists {
			var ok bool
			strict, ok = value.(bool)
			if !ok {
				return nil, false, nil, errors.New("tool strict must be a boolean")
			}
		}
		if strict && !model.StructuredOutputs {
			return nil, false, nil, errors.New("model does not advertise strict tool schemas")
		}
		mapped := object{"type": "function", "name": tool["name"], "parameters": schema, "strict": strict}
		if v, ok := tool["description"]; ok {
			mapped["description"] = v
		}
		tools = append(tools, mapped)
	}
	if len(tools) > 0 {
		dst["tools"] = tools
	}
	choice := obj(src["tool_choice"])
	if choice != nil {
		switch text(choice["type"]) {
		case "auto", "none":
			dst["tool_choice"] = choice["type"]
		case "any":
			dst["tool_choice"] = "required"
		case "tool":
			if search && text(choice["name"]) == "web_search" {
				dst["tool_choice"] = object{"type": "web_search"}
			} else {
				dst["tool_choice"] = object{"type": "function", "name": choice["name"]}
			}
		default:
			return nil, false, nil, errors.New("unsupported tool_choice")
		}
		if disabled, ok := choice["disable_parallel_tool_use"].(bool); ok {
			dst["parallel_tool_calls"] = !disabled
		}
	}
	format := obj(obj(src["output_config"])["format"])
	if format == nil {
		format = obj(src["output_format"])
	}
	if text(format["type"]) == "json_schema" {
		dst["text"] = object{"format": object{"type": "json_schema", "name": "response", "schema": format["schema"], "strict": true}}
	}
	if err := searchContext.request(dst, original); err != nil {
		return nil, false, nil, err
	}
	if explicit {
		if err := cache.apply(dst); err != nil {
			return nil, false, nil, err
		}
	} else if cache.count > 0 || cache.configured {
		dst["prompt_cache_options"] = object{"mode": "implicit"}
	}
	encoded, err := json.Marshal(dst)
	return encoded, stream, searchContext, err
}

func systemText(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	if array(v) == nil {
		return "", errors.New("system must be text or an array")
	}
	var parts []string
	for _, v := range array(v) {
		b := obj(v)
		if text(b["type"]) != "text" {
			return "", errors.New("only text system blocks can be converted")
		}
		parts = append(parts, text(b["text"]))
	}
	return strings.Join(parts, "\n\n"), nil
}

func responsePart(b object, role string) (object, error) {
	part, err := responseContentPart(b, role)
	if err == nil {
		responseCachePart(b, part)
	}
	return part, err
}

func responseContentPart(b object, role string) (object, error) {
	switch text(b["type"]) {
	case "text":
		kind := "input_text"
		if role == "assistant" {
			kind = "output_text"
		}
		return object{"type": kind, "text": text(b["text"])}, nil
	case "image":
		source := obj(b["source"])
		url := text(source["url"])
		if text(source["type"]) == "base64" {
			url = "data:" + text(source["media_type"]) + ";base64," + text(source["data"])
		}
		if url == "" {
			return nil, errors.New("image needs a URL or base64 source")
		}
		return object{"type": "input_image", "image_url": url}, nil
	case "document":
		source := obj(b["source"])
		if text(source["type"]) == "text" {
			return object{"type": "input_text", "text": text(source["data"])}, nil
		}
		if text(source["type"]) == "url" {
			return object{"type": "input_file", "file_url": text(source["url"])}, nil
		}
		if text(source["type"]) == "base64" {
			name := text(b["title"])
			if name == "" {
				name = "document.pdf"
			}
			return object{"type": "input_file", "filename": name, "file_data": "data:" + text(source["media_type"]) + ";base64," + text(source["data"])}, nil
		}
	}
	return nil, errors.New("unsupported document source")
}

func toolOutput(block object) (any, error) {
	output, err := responseToolOutput(block)
	if err != nil {
		return nil, err
	}
	if _, marked := block["cache_control"]; !marked {
		return output, nil
	}
	if s, ok := output.(string); ok {
		output = []any{object{"type": "input_text", "text": s}}
	}
	parts := array(output)
	if len(parts) == 0 {
		return nil, errors.New("cached tool result needs non-empty text")
	}
	last := obj(parts[len(parts)-1])
	if last["type"] != "input_text" || text(last["text"]) == "" {
		return nil, errors.New("tool result cache breakpoint requires a final non-empty text block")
	}
	responseCachePart(block, last)
	return output, nil
}

func responseToolOutput(block object) (any, error) {
	errorResult, _ := block["is_error"].(bool)
	if s, ok := block["content"].(string); ok {
		if errorResult {
			s = "[Tool error]\n" + s
		}
		return s, nil
	}
	parts := []any{}
	if errorResult {
		parts = append(parts, object{"type": "input_text", "text": "[Tool error]"})
	}
	for _, value := range array(block["content"]) {
		part, err := responsePart(obj(value), "user")
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return parts, nil
}

func responseEffort(src object, m provider.Model) string {
	if len(m.ThinkingEfforts) == 0 {
		return ""
	}
	effort := text(obj(src["output_config"])["effort"])
	// Claude Code can append output_config to an otherwise empty system
	// message when effort changes. The latest turn overrides the initial value.
	messages := array(src["messages"])
	for i := len(messages) - 1; i >= 0; i-- {
		message := obj(messages[i])
		role := text(message["role"])
		if role == "system" || role == "developer" {
			if turnEffort := text(obj(message["output_config"])["effort"]); turnEffort != "" {
				effort = turnEffort
				break
			}
		}
	}
	if effort == "" {
		effort = text(src["reasoning_effort"])
	}
	thinking := obj(src["thinking"])
	if effort == "" {
		switch text(thinking["type"]) {
		case "disabled":
			for _, level := range []string{"none", "minimal", "low"} {
				if slices.Contains(m.ThinkingEfforts, level) {
					return level
				}
			}
			return ""
		case "enabled":
			effort = "medium"
			if budget := number(thinking["budget_tokens"]); budget >= 10000 {
				effort = "high"
			} else if budget > 0 && budget < 4000 {
				effort = "low"
			}
		case "adaptive":
			effort = "medium"
		default:
			return ""
		}
	}
	return supportedEffort(effort, m.ThinkingEfforts)
}

func supportedEffort(effort string, supported []string) string {
	if slices.Contains(supported, effort) {
		return effort
	}
	levels := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	// Claude's max means the highest available upstream effort. Prefer a lower
	// advertised level rather than silently promoting to a more expensive one.
	index := slices.Index(levels, effort)
	for i := index; i >= 0; i-- {
		if slices.Contains(supported, levels[i]) {
			return levels[i]
		}
	}
	return ""
}
