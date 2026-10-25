package provider

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var searchGrokID = regexp.MustCompile(`^grok-4\.[67](?:-\d{4}-\d{2}-\d{2}|-\d{8})?$`)

// SupportsNativeSearch is shared by request validation and capability pickers.
// Messages requires an explicit server-tool advertisement. Responses defaults
// GPT names to supported, plus the two known Grok releases (not other vendors).
func SupportsNativeSearch(model Model) bool {
	if model.Endpoint != "/v1/messages" && model.Endpoint != "/responses" {
		return false
	}
	if model.NativeSearchKnown {
		return model.NativeSearch
	}
	if model.Endpoint == "/v1/messages" {
		return false
	}
	return strings.Contains(strings.ToLower(model.ID), "gpt") ||
		strings.Contains(strings.ToLower(model.Name), "gpt") || searchGrokID.MatchString(strings.ToLower(model.ID))
}

// replayParts accepts only native assistant content; Responses output contains
// output-only fields which must not leak into the next request's input schema.
func replayParts(replay *Replay, endpoint string) ([]any, error) {
	parts := make([]any, 0, len(replay.Blocks))
	for _, raw := range replay.Blocks {
		var block map[string]any
		if err := json.Unmarshal(raw, &block); err != nil || block == nil {
			return nil, fmt.Errorf("invalid %s assistant replay", endpoint)
		}
		kind, _ := block["type"].(string)
		if endpoint == "/v1/messages" {
			switch kind {
			case "text", "thinking", "redacted_thinking", "tool_use", "server_tool_use", "web_search_tool_result":
				parts = append(parts, block)
			default:
				return nil, fmt.Errorf("unsupported Messages replay block %q", kind)
			}
			continue
		}
		keys := []string{}
		switch kind {
		case "message":
			keys = []string{"type", "id", "role", "content", "status"}
			content, ok := block["content"].([]any)
			if !ok || block["role"] != "assistant" {
				return nil, fmt.Errorf("invalid Responses replay message")
			}
			clean := make([]any, 0, len(content))
			for _, value := range content {
				part, ok := value.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid Responses replay content")
				}
				switch part["type"] {
				case "output_text":
					out := map[string]any{"type": "output_text", "text": part["text"], "annotations": []any{}}
					if annotations, ok := part["annotations"].([]any); ok {
						// Only url_citation is supported here; output-only logprobs
						// and vendor annotation extensions are not input fields.
						for _, value := range annotations {
							a, ok := value.(map[string]any)
							if ok && a["type"] == "url_citation" {
								out["annotations"] = append(out["annotations"].([]any), selectFields(a, []string{"type", "url", "title", "start_index", "end_index"}))
							}
						}
					}
					clean = append(clean, out)
				case "refusal":
					clean = append(clean, selectFields(part, []string{"type", "refusal"}))
				default:
					return nil, fmt.Errorf("unsupported Responses replay content %q", part["type"])
				}
			}
			block["content"] = clean
		case "reasoning":
			keys = []string{"type", "id", "summary", "encrypted_content", "status"}
		case "function_call":
			keys = []string{"type", "id", "call_id", "name", "arguments", "status"}
		case "web_search_call":
			keys = []string{"type", "id", "status", "action"}
			if action, ok := block["action"].(map[string]any); ok {
				clean := selectFields(action, []string{"type", "query", "queries", "url", "pattern"})
				if sources, ok := action["sources"].([]any); ok {
					// Responses input accepts URL source records, not vendor-only
					// source fields (Grok can also emit bare URL strings).
					cleanSources := make([]any, 0, len(sources))
					for _, source := range sources {
						switch source := source.(type) {
						case string:
							cleanSources = append(cleanSources, map[string]any{"type": "url", "url": source})
						case map[string]any:
							if source["type"] == "url" {
								cleanSources = append(cleanSources, selectFields(source, []string{"type", "url"}))
							}
						}
					}
					clean["sources"] = cleanSources
				}
				block["action"] = clean
			}
		default:
			return nil, fmt.Errorf("unsupported Responses replay item %q", kind)
		}
		parts = append(parts, selectFields(block, keys))
	}
	return parts, nil
}

func selectFields(src map[string]any, keys []string) map[string]any {
	dst := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := src[key]; ok {
			dst[key] = value
		}
	}
	return dst
}

func (p *streamParser) citation(rawURL, title string) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	normalized := u.String()
	for i := range p.completion.Citations {
		if p.completion.Citations[i].URL == normalized {
			if p.completion.Citations[i].Title == "" {
				p.completion.Citations[i].Title = strings.TrimSpace(title)
			}
			return
		}
	}
	p.completion.Citations = append(p.completion.Citations, Citation{URL: normalized, Title: strings.TrimSpace(title)})
}
