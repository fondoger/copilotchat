package localproxy

import (
	"errors"
	"fmt"
)

// Cache lifetimes are short/long tier approximations, not equivalent retention
// or billing guarantees. Provider support is deliberately not inferred by ID.
func cacheTTL(value any, anthropic bool) (string, error) {
	ttl, ok := value.(string)
	if value != nil && !ok {
		return "", errors.New("cache TTL must be a string")
	}
	if anthropic {
		switch ttl {
		case "", "5m":
			return "30m", nil
		case "1h":
			return "12h", nil
		}
	} else {
		switch ttl {
		case "", "30m":
			return "5m", nil
		case "12h":
			return "1h", nil
		}
	}
	return "", fmt.Errorf("unsupported cache TTL %q", ttl)
}

func cacheMarker(value any, anthropic bool, inherited string) (object, error) {
	marker := obj(value)
	if marker == nil {
		return nil, errors.New("cache marker must be an object")
	}
	kind, want := "mode", "explicit"
	if anthropic {
		kind, want = "type", "ephemeral"
	}
	if marker[kind] != want {
		return nil, fmt.Errorf("cache marker requires %s %q", kind, want)
	}
	for key := range marker {
		if key != kind && key != "ttl" {
			return nil, fmt.Errorf("unsupported cache marker field %q", key)
		}
	}
	ttl := inherited
	if _, exists := marker["ttl"]; exists || ttl == "" {
		var err error
		ttl, err = cacheTTL(marker["ttl"], anthropic)
		if err != nil {
			return nil, err
		}
	}
	if anthropic {
		out := object{"mode": "explicit"}
		if ttl != "30m" {
			out["ttl"] = ttl
		}
		return out, nil
	}
	out := object{"type": "ephemeral"}
	if ttl != "5m" {
		out["ttl"] = ttl
	}
	return out, nil
}

type bridgeCache struct {
	source     object
	anthropic  bool
	automatic  bool
	configured bool
	count      int
	shortSeen  bool
	ttl        string // Lifetime in the destination protocol.
}

func newBridgeCache(src object, anthropic bool) (*bridgeCache, error) {
	c := &bridgeCache{source: src, anthropic: anthropic}
	if anthropic {
		if marker, exists := src["cache_control"]; exists {
			converted, err := cacheMarker(marker, true, "")
			if err != nil {
				return nil, err
			}
			c.automatic, c.configured = true, true
			c.ttl = text(converted["ttl"])
			if c.ttl == "" {
				c.ttl = "30m"
			}
		}
	} else if value, exists := src["prompt_cache_options"]; exists {
		options := obj(value)
		if options == nil {
			return nil, errors.New("prompt_cache_options must be an object")
		}
		for key := range options {
			if key != "mode" && key != "ttl" && key != "prewarm" {
				return nil, fmt.Errorf("unsupported prompt_cache_options field %q", key)
			}
		}
		if v, exists := options["prewarm"]; exists && v != false {
			return nil, errors.New("cache prewarming is not supported by the Messages bridge")
		}
		mode := options["mode"]
		if mode != nil && mode != "explicit" && mode != "implicit" {
			return nil, errors.New("unsupported prompt cache mode")
		}
		ttl, err := cacheTTL(options["ttl"], false)
		if err != nil {
			return nil, err
		}
		c.automatic, c.configured, c.ttl = mode != "explicit", true, ttl
	}
	return c, nil
}

func (c *bridgeCache) marker(block object, allowed bool, path string) (object, error) {
	key := "prompt_cache_breakpoint"
	if c.anthropic {
		key = "cache_control"
	}
	value, exists := block[key]
	if !exists {
		return nil, nil
	}
	if !allowed {
		return nil, fmt.Errorf("%s: %s cannot be represented at this position by the protocol bridge", path, key)
	}
	inherited := c.ttl
	if c.anthropic {
		inherited = ""
	}
	marker, err := cacheMarker(value, c.anthropic, inherited)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.count++
	// Anthropic requires long-lived cache boundaries before short-lived ones.
	long := marker["ttl"] == "1h" || marker["ttl"] == "12h"
	if long && c.shortSeen {
		return nil, errors.New("long cache TTL breakpoints must precede short TTL breakpoints")
	}
	c.shortSeen = c.shortSeen || !long
	if !c.anthropic && !c.configured {
		c.automatic = true
	}
	return marker, nil
}

func (c *bridgeCache) apply(dst object) error {
	if c.count == 0 && !c.configured {
		return nil
	}
	writes := c.count
	if c.automatic {
		native, ttl := dst, c.ttl
		if c.anthropic {
			native, ttl = c.source, "5m"
			if c.ttl == "12h" {
				ttl = "1h"
			}
		}
		if ttl == "" {
			ttl = "5m"
		}
		if marker := finalAnthropicCacheMarker(native); marker != nil {
			lastTTL := text(obj(marker)["ttl"])
			if lastTTL == "" {
				lastTTL = "5m"
			}
			if lastTTL != ttl {
				return errors.New("automatic cache TTL conflicts with the final explicit breakpoint")
			}
		} else {
			writes++
		}
	}
	if writes > 4 {
		return errors.New("protocol bridge supports at most four cache breakpoints including automatic caching")
	}
	if c.automatic && (c.ttl == "1h" || c.ttl == "12h") && c.shortSeen {
		return errors.New("automatic long cache TTL cannot follow a short TTL breakpoint")
	}
	if c.anthropic {
		mode := "explicit"
		if c.automatic {
			mode = "implicit"
		}
		options := object{"mode": mode}
		if c.ttl == "12h" {
			options["ttl"] = "12h"
		}
		dst["prompt_cache_options"] = options
	} else if c.automatic {
		marker := object{"type": "ephemeral"}
		if c.ttl == "1h" {
			marker["ttl"] = "1h"
		}
		dst["cache_control"] = marker
	}
	return nil
}

// Only text inputs have an established Responses breakpoint representation.
// In particular, never invent breakpoint fields on tools or function calls.
func (c *bridgeCache) validateMessages(src object) error {
	for i, v := range array(src["tools"]) {
		if _, err := c.marker(obj(v), false, fmt.Sprintf("tools[%d]", i)); err != nil {
			return err
		}
	}
	for i, v := range array(src["system"]) {
		b := obj(v)
		if _, err := c.marker(b, text(b["type"]) == "text" && text(b["text"]) != "", fmt.Sprintf("system[%d]", i)); err != nil {
			return err
		}
	}
	for i, v := range array(src["messages"]) {
		m := obj(v)
		for j, v := range array(m["content"]) {
			b := obj(v)
			path := fmt.Sprintf("messages[%d].content[%d]", i, j)
			kind := text(b["type"])
			allowed := kind == "tool_result" || (kind == "text" && m["role"] != "assistant" && text(b["text"]) != "")
			if _, err := c.marker(b, allowed, path); err != nil {
				return err
			}
			if kind == "tool_result" {
				for k, v := range array(b["content"]) {
					if _, err := c.marker(obj(v), false, fmt.Sprintf("%s.content[%d]", path, k)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// Automatic caching shares a slot with an explicit marker on the final
// eligible block, but conflicting lifetimes at that position are invalid.
func finalAnthropicCacheMarker(body object) any {
	messages := array(body["messages"])
	for i := len(messages) - 1; i >= 0; i-- {
		content := obj(messages[i])["content"]
		if _, ok := content.(string); ok {
			return nil
		}
		parts := array(content)
		for j := len(parts) - 1; j >= 0; j-- {
			part := obj(parts[j])
			switch part["type"] {
			case "text", "image", "document", "tool_use", "tool_result":
				return part["cache_control"]
			}
		}
	}
	if blocks := array(body["system"]); len(blocks) > 0 {
		return obj(blocks[len(blocks)-1])["cache_control"]
	}
	if _, ok := body["system"].(string); ok {
		return nil
	}
	if tools := array(body["tools"]); len(tools) > 0 {
		return obj(tools[len(tools)-1])["cache_control"]
	}
	return nil
}

func hasCacheMarker(blocks []any, key string) bool {
	for _, v := range blocks {
		if _, exists := obj(v)[key]; exists {
			return true
		}
	}
	return false
}

// Ordinary Responses conversations use implicit caching. Remove source
// markers before conversion so a moving tail marker cannot change wire shapes.
// Validation runs first, including unsupported markers on omitted tools.
func removeMessagesCacheMarkers(src object) {
	delete(src, "cache_control")
	for _, value := range array(src["system"]) {
		delete(obj(value), "cache_control")
	}
	for _, value := range array(src["messages"]) {
		for _, value := range array(obj(value)["content"]) {
			block := obj(value)
			delete(block, "cache_control")
			if block["type"] == "tool_result" {
				for _, value := range array(block["content"]) {
					delete(obj(value), "cache_control")
				}
			}
		}
	}
}

func responseCachePart(src, dst object) {
	if marker, exists := src["cache_control"]; exists {
		// The request-level pass already validated placement, TTL and write limits.
		dst["prompt_cache_breakpoint"], _ = cacheMarker(marker, true, "")
	}
}

// Validate before replay restoration, which intentionally replaces visible
// assistant items with opaque, signed Anthropic blocks.
func (c *bridgeCache) validateResponses(src object) error {
	for i, v := range array(src["tools"]) {
		if _, err := c.marker(obj(v), false, fmt.Sprintf("tools[%d]", i)); err != nil {
			return err
		}
	}
	for i, v := range array(src["input"]) {
		item := obj(v)
		path := fmt.Sprintf("input[%d]", i)
		if _, err := c.marker(item, false, path); err != nil {
			return err
		}
		kind := text(item["type"])
		output := kind == "function_call_output" || kind == "custom_tool_call_output"
		parts := array(item["content"])
		field := "content"
		if output {
			parts = array(item["output"])
			field = "output"
		}
		for j, v := range parts {
			part := obj(v)
			message := kind == "" || kind == "message"
			role := text(item["role"])
			allowed := text(part["type"]) == "input_text" && text(part["text"]) != "" && ((message && (role == "user" || role == "developer" || role == "system")) || (output && j == len(parts)-1))
			if _, err := c.marker(part, allowed, fmt.Sprintf("%s.%s[%d]", path, field, j)); err != nil {
				return err
			}
		}
	}
	return nil
}
