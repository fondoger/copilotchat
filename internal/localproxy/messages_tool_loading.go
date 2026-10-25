package localproxy

import (
	"errors"
	"fmt"
)

// resolveMessagesTools expands client-side tool references before cache
// validation. Keep src.tools intact: even tools omitted upstream must not hide
// cache markers that the bridge cannot represent.
func resolveMessagesTools(src object) ([]any, error) {
	definitions := map[string]object{}
	for _, value := range array(src["tools"]) {
		tool := obj(value)
		name := text(tool["name"])
		if name != "" {
			if definitions[name] != nil {
				return nil, fmt.Errorf("duplicate tool name %q", name)
			}
			definitions[name] = tool
		}
		if value, exists := tool["defer_loading"]; exists {
			if _, ok := value.(bool); !ok {
				return nil, errors.New("tool defer_loading must be a boolean")
			}
		}
	}
	loaded := map[string]bool{}
	for _, value := range array(src["messages"]) {
		for _, value := range array(obj(value)["content"]) {
			result := obj(value)
			if result["type"] != "tool_result" {
				continue
			}
			for _, value := range array(result["content"]) {
				ref := obj(value)
				if ref["type"] != "tool_reference" {
					continue
				}
				name := text(ref["tool_name"])
				tool := definitions[name]
				if tool == nil || name == "DeferredToolPlaceholder" {
					return nil, fmt.Errorf("unknown tool reference %q", name)
				}
				if kind := text(tool["type"]); kind != "" && kind != "custom" {
					return nil, fmt.Errorf("tool reference %q must refer to a client-side tool", name)
				}
				loaded[name] = true
				// Rewrite only the reference, in place. Text, images, result
				// metadata and cache boundaries around it retain their positions.
				ref["type"], ref["text"] = "text", "Loaded tool: "+name
				delete(ref, "tool_name")
			}
		}
	}
	tools := []any{}
	for _, value := range array(src["tools"]) {
		tool := obj(value)
		name := text(tool["name"])
		if name == "DeferredToolPlaceholder" || (tool["defer_loading"] == true && !loaded[name]) {
			continue
		}
		tools = append(tools, value)
	}
	return tools, nil
}

func validateMessagesBridgeFeatures(src object) error {
	// These alter server-side state/execution, not just transport hints. Never
	// silently discard them when clients enable experimental API features.
	for _, key := range []string{"context_management", "container", "mcp_servers", "inference_geo", "speed"} {
		if src[key] != nil {
			return fmt.Errorf("%s is not supported by the Responses bridge", key)
		}
	}
	for _, value := range array(src["messages"]) {
		if obj(value)["context_management"] != nil {
			return errors.New("message context_management is not supported by the Responses bridge")
		}
	}
	return nil
}
