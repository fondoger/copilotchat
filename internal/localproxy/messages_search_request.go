package localproxy

import (
	"errors"
	"fmt"

	"github.com/fondoger/copilotchat/internal/provider"
)

// Claude Code's SDK has a fixed client search contract, not a hosted type.
func detectMessagesSearch(src object, model provider.Model) (*messagesSearchContext, error) {
	if !usesNativeToolSearch(model.ID) {
		return nil, nil
	}
	var context *messagesSearchContext
	for _, value := range array(src["tools"]) {
		tool := obj(value)
		kind, name := text(tool["type"]), text(tool["name"])
		mode := ""
		switch kind {
		case "tool_search_tool_regex_20251119", "tool_search_tool_bm25_20251119":
			mode = "server"
			want := "tool_search_tool_regex"
			if kind == "tool_search_tool_bm25_20251119" {
				want = "tool_search_tool_bm25"
			}
			if name != want {
				return nil, errors.New("hosted tool search requires its canonical name")
			}
			for key, value := range tool {
				if key != "type" && key != "name" && value != nil {
					return nil, fmt.Errorf("hosted tool search option %q cannot be converted", key)
				}
			}
		case "", "custom":
			schema := obj(tool["input_schema"])
			properties := obj(schema["properties"])
			if name == "ToolSearch" && schema["type"] == "object" && text(obj(properties["query"])["type"]) == "string" {
				required := false
				for _, v := range array(schema["required"]) {
					required = required || v == "query"
				}
				if required {
					mode = "client"
				}
			}
		}
		if mode != "" {
			if context != nil {
				return nil, errors.New("multiple client/hosted search entries cannot be converted")
			}
			context = &messagesSearchContext{mode: mode, name: name}
		}
	}
	return context, nil
}

// Convert already validated/serialized tool definitions and historical calls.
// References are captured before the ordinary resolver rewrites them as text.
func (c *messagesSearchContext) request(dst object, original object) error {
	if c == nil {
		return nil
	}
	definitions := map[string]object{}
	source := map[string]object{}
	for _, value := range array(original["tools"]) {
		t := obj(value)
		source[text(t["name"])] = t
	}
	for _, value := range array(dst["tools"]) {
		t := obj(value)
		definitions[text(t["name"])] = t
	}
	calls := map[string]object{}
	results := map[string]object{}
	notes := map[string][]any{}
	loaded := map[string]bool{}
	for _, value := range array(original["messages"]) {
		message := obj(value)
		for _, value := range array(message["content"]) {
			block := obj(value)
			if block["type"] == "tool_use" && block["name"] == c.name {
				id := text(block["id"])
				if c.mode != "client" || message["role"] != "assistant" || id == "" || calls[id] != nil {
					return errors.New("invalid or duplicate client search call")
				}
				args := block["input"]
				if args == nil {
					args = object{}
				}
				if obj(args) == nil {
					return errors.New("client search requires object arguments")
				}
				calls[id] = obj(args)
			}
			if block["type"] != "tool_result" || calls[text(block["tool_use_id"])] == nil {
				continue
			}
			id := text(block["tool_use_id"])
			if message["role"] != "user" {
				return errors.New("client search result must be in a user message")
			}
			if results[id] != nil {
				return errors.New("duplicate client search result")
			}
			if block["is_error"] == true {
				return errors.New("failed client search results cannot be converted")
			}
			tools := []any{}
			seen := map[string]bool{}
			content := block["content"]
			if s, ok := content.(string); ok {
				if s != "" {
					notes[id] = append(notes[id], object{"type": "input_text", "text": s})
				}
			} else {
				for _, value := range array(content) {
					ref := obj(value)
					if ref["type"] != "tool_reference" {
						return errors.New("native client search expects tool references or a text-only empty result")
					}
					name := text(ref["tool_name"])
					if seen[name] {
						continue
					}
					seen[name] = true
					definition := definitions[name]
					if definition == nil {
						return fmt.Errorf("missing discovered tool definition %q", name)
					}
					t := cloneToolObject(definition)
					t["defer_loading"] = true
					tools = append(tools, t)
					loaded[name] = true
				}
			}
			results[id] = object{"type": "tool_search_output", "execution": "client", "status": "completed", "call_id": id, "tools": tools}
		}
	}
	tools := []any{}
	for _, value := range array(dst["tools"]) {
		t := obj(value)
		name := text(t["name"])
		if name == c.name {
			declaration := object{"type": "tool_search", "execution": c.mode}
			if c.mode == "client" {
				declaration["parameters"] = t["parameters"]
				if t["description"] != nil {
					declaration["description"] = t["description"]
				}
			}
			tools = append(tools, declaration)
			continue
		}
		if c.mode == "client" && loaded[name] {
			continue
		}
		if source[name]["defer_loading"] == true {
			t = cloneToolObject(t)
			t["defer_loading"] = true
		}
		tools = append(tools, t)
	}
	dst["tools"] = tools
	input := []any{}
	for _, value := range array(dst["input"]) {
		item := obj(value)
		if item["type"] == "function_call" && item["name"] == c.name {
			if c.mode != "client" {
				return errors.New("hosted search cannot be replayed as a client function")
			}
			id := text(item["call_id"])
			if results[id] == nil {
				return errors.New("input has unfinished client tool search call")
			}
			args := calls[id]
			item = cloneToolObject(item)
			item["type"], item["execution"], item["arguments"] = "tool_search_call", "client", args
			delete(item, "name")
		} else if item["type"] == "function_call_output" && results[text(item["call_id"])] != nil {
			id := text(item["call_id"])
			input = append(input, results[id])
			if len(notes[id]) > 0 {
				input = append(input, object{"role": "user", "content": notes[id]})
			}
			continue
		}
		input = append(input, item)
	}
	dst["input"] = input
	choice := dst["tool_choice"]
	if selected := obj(choice); selected != nil && selected["name"] == c.name || choice == "required" && len(tools) == 1 {
		return errors.New("native tool search does not support forced tool_choice; use auto or omit tool_choice")
	}
	return nil
}

func copyMessagesObject(src object) object {
	return cloneMessagesValue(src).(object)
}

func cloneMessagesValue(value any) any {
	switch value := value.(type) {
	case object:
		copy := make(object, len(value))
		for key, item := range value {
			copy[key] = cloneMessagesValue(item)
		}
		return copy
	case []any:
		if value == nil {
			return []any(nil)
		}
		copy := make([]any, len(value))
		for i, item := range value {
			copy[i] = cloneMessagesValue(item)
		}
		return copy
	default:
		return value
	}
}
