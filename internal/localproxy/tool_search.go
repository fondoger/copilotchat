package localproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func usesNativeToolSearch(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "gpt")
}

type toolSearchOptions struct {
	replay bool
	native bool
}

type toolIdentity struct {
	namespace, name, kind string
}

type toolSearchContext struct {
	aliases map[string]toolIdentity
	names   map[toolIdentity]string
}

func newToolSearchContext() *toolSearchContext {
	return &toolSearchContext{aliases: map[string]toolIdentity{}, names: map[toolIdentity]string{}}
}

func (c *toolSearchContext) register(id toolIdentity) (string, error) {
	if id.name == "" {
		return "", errors.New("tool name is required")
	}
	if alias, ok := c.names[id]; ok {
		return alias, nil
	}
	alias := id.name
	if id.namespace != "" || id.kind == "tool_search" {
		sum := sha256.Sum256([]byte(id.kind + "\x00" + id.namespace + "\x00" + id.name))
		alias = "cc_tool_" + hex.EncodeToString(sum[:16])
	}
	if previous, exists := c.aliases[alias]; exists && previous != id {
		return "", fmt.Errorf("tool alias collision for %q", id.name)
	}
	c.names[id], c.aliases[alias] = alias, id
	return alias, nil
}

func (c *toolSearchContext) restoreOutputItem(item object) error {
	if c == nil {
		return nil
	}
	kind := text(item["type"])
	if kind != "function_call" && kind != "custom_tool_call" {
		return nil
	}
	id, ok := c.aliases[text(item["name"])]
	if !ok {
		return nil
	}
	if id.kind == "tool_search" {
		args, ok := item["arguments"].(string)
		if !ok {
			return errors.New("tool search function arguments must be JSON text")
		}
		var value object
		if args == "" && item["status"] == "in_progress" {
			value = object{}
		} else {
			var err error
			value, err = decodeObject([]byte(args))
			if err != nil {
				return fmt.Errorf("invalid tool search arguments: %w", err)
			}
		}
		item["type"], item["execution"], item["arguments"] = "tool_search_call", "client", value
		if s := text(item["id"]); s != "" {
			item["id"] = "tsc_" + strings.TrimPrefix(s, "fc_")
		}
		delete(item, "name")
		delete(item, "namespace")
		return nil
	}
	item["name"] = id.name
	if id.namespace != "" {
		item["namespace"] = id.namespace
	}
	return nil
}

func (b *responsesBridge) restoreToolItem(item object) (object, error) {
	if b != nil && b.tools != nil {
		if err := b.tools.restoreOutputItem(item); err != nil {
			return nil, err
		}
	}
	return item, nil
}

func searchIdentity() toolIdentity { return toolIdentity{name: "tool_search", kind: "tool_search"} }

// normalizeResponsesTools lowers client-side discovery, not hosted search. All
// state is reconstructed from the request; the client retains execution/permissions.
// Replay is validated before touching visible items, whose original shapes are
// part of the Anthropic envelope's match key.
func normalizeResponsesTools(src object, options toolSearchOptions) (*toolSearchContext, error) {
	if options.native {
		return nil, nil // Native Responses owns client/server discovery and namespace replay.
	}
	original := array(src["input"])
	relevant, searchDeclared := false, false
	for _, v := range array(src["tools"]) {
		t := obj(v)
		if t["type"] == "tool_search" {
			searchDeclared = true
		}
		if t["type"] == "tool_search" || t["type"] == "namespace" || t["defer_loading"] == true {
			relevant = true
		}
	}
	for _, v := range original {
		t := obj(v)
		if t["type"] == "tool_search_call" || t["type"] == "tool_search_output" || t["namespace"] != nil {
			relevant = true
		}
	}
	var c *toolSearchContext
	if relevant {
		c = newToolSearchContext()
	}
	input := original
	if options.replay && input != nil {
		var err error
		input, err = restoreAnthropicInput(input)
		if err != nil {
			return nil, err
		}
		src["input"] = input
	}
	if c == nil {
		return nil, nil
	}
	if src["previous_response_id"] != nil || src["conversation"] != nil {
		return nil, errors.New("tool search bridging requires full input history, not previous_response_id or conversation")
	}
	if src["background"] == true {
		return nil, errors.New("background tool search bridging is not supported")
	}

	loaded := map[toolIdentity]bool{}
	discovered := []any{}
	calls := map[string]bool{}
	// Index the original visible calls as well as outputs, before replay folds
	// assistant content into its signed/opaque block sequence.
	for _, v := range original {
		item := obj(v)
		switch text(item["type"]) {
		case "tool_search_call":
			if text(item["execution"]) != "client" || text(item["call_id"]) == "" {
				return nil, errors.New("only client tool_search_call with call_id can be bridged")
			}
			call := text(item["call_id"])
			if calls[call] {
				return nil, errors.New("duplicate tool search call_id")
			}
			calls[call] = true
			if obj(item["arguments"]) == nil {
				return nil, errors.New("tool search arguments must be an object")
			}
			if _, err := c.register(searchIdentity()); err != nil {
				return nil, err
			}
		case "tool_search_output":
			call := text(item["call_id"])
			if text(item["execution"]) != "client" || call == "" || !calls[call] {
				return nil, errors.New("tool search output has no matching client search call")
			}
			delete(calls, call)
			if item["status"] != nil && item["status"] != "completed" {
				return nil, errors.New("unsupported tool search output status")
			}
			if array(item["tools"]) == nil {
				return nil, errors.New("tool search output tools must be an array")
			}
			discovered = append(discovered, array(item["tools"])...)
		case "function_call", "custom_tool_call":
			kind := "function"
			if item["type"] == "custom_tool_call" {
				kind = "custom"
			}
			if _, err := c.register(toolIdentity{text(item["namespace"]), text(item["name"]), kind}); err != nil {
				return nil, err
			}
		}
	}
	if len(calls) > 0 {
		return nil, errors.New("input has unfinished client tool search calls")
	}

	// Catalog entries are flattened deterministically. Search outputs provide the
	// schemas, unlike Anthropic tool_reference blocks which only contain names.
	type definition struct {
		id   toolIdentity
		tool object
	}
	flatten := func(values []any) ([]definition, error) {
		out := []definition{}
		for _, v := range values {
			tool := obj(v)
			if tool == nil {
				return nil, errors.New("tool definition must be an object")
			}
			if tool["cache_control"] != nil || tool["prompt_cache_breakpoint"] != nil {
				return nil, errors.New("cache boundaries on discovery tool definitions are not supported")
			}
			namespace := ""
			members := []any{tool}
			if tool["type"] == "namespace" {
				namespace = text(tool["name"])
				if namespace == "" || array(tool["tools"]) == nil {
					return nil, errors.New("namespace needs a name and tools array")
				}
				members = array(tool["tools"])
			}
			for _, member := range members {
				t := obj(member)
				if t == nil {
					return nil, errors.New("tool definition must be an object")
				}
				if t["cache_control"] != nil || t["prompt_cache_breakpoint"] != nil {
					return nil, errors.New("cache boundaries on discovery tool definitions are not supported")
				}
				if value, exists := t["defer_loading"]; exists {
					if _, ok := value.(bool); !ok {
						return nil, errors.New("defer_loading must be a boolean")
					}
				}
				kind := text(t["type"])
				if namespace != "" && kind != "function" && kind != "custom" {
					return nil, errors.New("namespace supports only function and custom tools")
				}
				id := toolIdentity{namespace, text(t["name"]), kind}
				if kind == "function" || kind == "custom" {
					if _, err := c.register(id); err != nil {
						return nil, err
					}
				}
				out = append(out, definition{id, t})
			}
		}
		return out, nil
	}
	historical, err := flatten(discovered)
	if err != nil {
		return nil, err
	}
	historyIndex := map[toolIdentity]int{}
	latest := make([]definition, 0, len(historical))
	for _, d := range historical {
		if d.id.kind != "function" && d.id.kind != "custom" {
			return nil, errors.New("tool search may return only function/custom tools or namespaces")
		}
		loaded[d.id] = true
		if index, exists := historyIndex[d.id]; exists {
			latest[index] = d
		} else {
			historyIndex[d.id] = len(latest)
			latest = append(latest, d)
		}
	}
	historical = latest
	current, err := flatten(array(src["tools"]))
	if err != nil {
		return nil, err
	}
	tools := []any{}
	seen := map[toolIdentity]bool{}
	available := map[string]bool{}
	emit := func(d definition) error {
		t := cloneToolObject(d.tool)
		switch d.id.kind {
		case "tool_search":
			if text(t["execution"]) != "client" {
				return errors.New("hosted tool_search cannot be lowered to client tool search")
			}
			if obj(t["parameters"]) == nil {
				return errors.New("client tool_search needs parameters")
			}
			alias, err := c.register(searchIdentity())
			if err != nil {
				return err
			}
			t["type"], t["name"] = "function", alias
			delete(t, "execution")
		case "function", "custom":
			if t["defer_loading"] == true && !loaded[d.id] {
				return nil
			}
			t["name"] = c.names[d.id]
		}
		delete(t, "defer_loading")
		available[text(t["name"])] = true
		if !seen[d.id] {
			tools = append(tools, t)
			seen[d.id] = true
		}
		return nil
	}
	declared := map[toolIdentity]bool{}
	for _, d := range current {
		if declared[d.id] {
			return nil, fmt.Errorf("duplicate tool declaration %q", d.id.name)
		}
		declared[d.id] = true
		if err := emit(d); err != nil {
			return nil, err
		}
	}
	// When Codex removes discovery (e.g. compaction), keep the history readable
	// but do not resurrect its entire tool catalog.
	if searchDeclared {
		for _, d := range historical {
			if err := emit(d); err != nil {
				return nil, err
			}
		}
	}
	if _, exists := src["tools"]; exists {
		src["tools"] = tools
	}

	converted := []any{}
	for _, v := range input {
		item := obj(v)
		mapped := item
		switch text(item["type"]) {
		case "tool_search_call":
			mapped = cloneToolObject(item)
			args, _ := json.Marshal(item["arguments"])
			mapped["type"], mapped["name"], mapped["arguments"] = "function_call", c.names[searchIdentity()], string(args)
			delete(mapped, "execution")
			delete(mapped, "id")
		case "tool_search_output":
			definitions, err := flatten(array(item["tools"]))
			if err != nil {
				return nil, err
			}
			loadedNames := []string{}
			for _, d := range definitions {
				loadedNames = append(loadedNames, c.names[d.id])

			}
			data, _ := json.Marshal(loadedNames)
			mapped = object{"type": "function_call_output", "call_id": item["call_id"], "output": "Tools loaded by client search: " + string(data)}
		case "function_call", "custom_tool_call":
			kind := "function"
			if item["type"] == "custom_tool_call" {
				kind = "custom"
			}
			mapped = cloneToolObject(item)
			mapped["name"] = c.names[toolIdentity{text(item["namespace"]), text(item["name"]), kind}]
			delete(mapped, "namespace")
		}
		converted = append(converted, mapped)

	}
	if original != nil {
		src["input"] = converted
	}
	mapChoice := func(choice object) (object, error) {
		kind := text(choice["type"])
		if kind != "function" && kind != "custom" && kind != "tool_search" {
			return choice, nil
		}
		id := toolIdentity{text(choice["namespace"]), text(choice["name"]), kind}
		if kind == "tool_search" {
			id = searchIdentity()
		}
		alias, ok := c.names[id]
		if !ok || !available[alias] {
			return nil, errors.New("tool_choice names an unavailable tool")
		}
		choice = cloneToolObject(choice)
		choice["name"] = alias
		if kind == "tool_search" {
			choice["type"] = "function"
		}
		delete(choice, "namespace")
		return choice, nil
	}
	if choice := obj(src["tool_choice"]); choice != nil {
		if choice["type"] == "allowed_tools" {
			if array(choice["tools"]) == nil {
				return nil, errors.New("allowed_tools needs a tools array")
			}
			allowed := []any{}
			for _, v := range array(choice["tools"]) {
				mapped, err := mapChoice(obj(v))
				if err != nil {
					return nil, err
				}
				allowed = append(allowed, mapped)
			}
			choice = cloneToolObject(choice)
			choice["tools"] = allowed
			src["tool_choice"] = choice
		} else {
			mapped, err := mapChoice(choice)
			if err != nil {
				return nil, err
			}
			src["tool_choice"] = mapped
		}
	}
	return c, nil
}

func cloneToolObject(src object) object {
	dst := object{}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
