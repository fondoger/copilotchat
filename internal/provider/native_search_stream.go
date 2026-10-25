package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func (p *streamParser) appendBlockString(index int, key, delta string) {
	if block := p.blocks[index]; block != nil {
		prior, _ := block[key].(string)
		block[key] = prior + delta
	} else {
		p.replayInvalid = true
	}
}

func (p *streamParser) closeBlock(index int) error {
	block := p.blocks[index]
	if block == nil {
		return errors.New("Copilot stopped an unknown content block")
	}
	if p.closedBlocks == nil {
		p.closedBlocks = make(map[int]bool)
	}
	if p.closedBlocks[index] {
		return errors.New("Copilot stopped a content block twice")
	}
	if partial, ok := p.blockJSON[index]; ok {
		var input map[string]any
		if json.Unmarshal([]byte(partial), &input) != nil || input == nil {
			p.replayInvalid = true
			return errors.New("Copilot returned invalid tool input JSON")
		}
		block["input"] = input
	}
	if block["type"] == "tool_use" || block["type"] == "server_tool_use" {
		id, _ := block["id"].(string)
		name, _ := block["name"].(string)
		if _, ok := block["input"].(map[string]any); !ok || id == "" || name == "" {
			p.replayInvalid = true
			return errors.New("Copilot returned an incomplete tool block")
		}
	}
	p.closedBlocks[index] = true
	if block["type"] == "tool_use" {
		encoded, _ := json.Marshal(block["input"])
		p.call(index).Arguments = string(encoded)
	}
	if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
		encoded, _ := json.Marshal(block)
		p.completion.ProviderData = append(p.completion.ProviderData, encoded)
	}
	p.searchMetadata(block)
	return nil
}

// searchMetadata reads citations only from actual citation/result blocks, never
// model-authored markdown. Server errors remain errors, not fabricated results.
func (p *streamParser) searchMetadata(block map[string]any) {
	kind, _ := block["type"].(string)
	switch kind {
	case "url_citation", "web_search_result_location", "web_search_result":
		u, _ := block["url"].(string)
		title, _ := block["title"].(string)
		p.citation(u, title)
	case "web_search_tool_result_error":
		code, _ := block["error_code"].(string)
		p.nativeError = fmt.Errorf("Copilot native web search failed: %s", code)
	case "web_search_call":
		if block["status"] == "failed" {
			p.nativeError = errors.New("Copilot native web search failed")
			break
		}
		// Grok may return sources on the hosted action instead of attaching
		// url_citation annotations to its final message. These are tool output,
		// not URLs inferred from the model's answer.
		if action, ok := block["action"].(map[string]any); ok {
			if sources, ok := action["sources"].([]any); ok {
				for _, source := range sources {
					switch source := source.(type) {
					case string:
						p.citation(source, "")
					case map[string]any:
						if source["type"] == "url" {
							u, _ := source["url"].(string)
							title, _ := source["title"].(string)
							p.citation(u, title)
						}
					}
				}
			}
		}
	}
	for _, key := range []string{"content", "annotations", "citations"} {
		switch value := block[key].(type) {
		case []any:
			for _, item := range value {
				if child, ok := item.(map[string]any); ok {
					p.searchMetadata(child)
				}
			}
		case map[string]any:
			p.searchMetadata(value)
		}
	}
}

func (p *streamParser) responseItem(index int, raw json.RawMessage) error {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil || item == nil {
		return errors.New("invalid Copilot Responses output item")
	}
	if p.outputItems == nil {
		p.outputItems = make(map[int]json.RawMessage)
	}
	_, existed := p.outputItems[index]
	p.outputItems[index] = append(json.RawMessage(nil), raw...)
	switch item["type"] {
	case "function_call":
		id, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		args, _ := item["arguments"].(string)
		*p.call(index) = ToolCall{ID: id, Name: name, Arguments: args}
	case "reasoning":
		if !existed {
			p.completion.ProviderData = append(p.completion.ProviderData, append(json.RawMessage(nil), raw...))
		}
	}
	p.searchMetadata(item)
	return nil
}

// Terminal snapshots also cover proxies which omit the corresponding deltas.
func (p *streamParser) responseVisibleFallback() {
	text, reasoning := p.responseVisible()
	if p.completion.Content == "" {
		p.text(text)
	}
	if p.completion.Reasoning == "" {
		p.reasoning(reasoning)
	}
}

func (p *streamParser) responseVisible() (string, string) {
	var text, reasoning strings.Builder
	for _, index := range sortedIndices(p.outputItems) {
		var item struct {
			Type    string                        `json:"type"`
			Content []struct{ Type, Text string } `json:"content"`
			Summary []struct{ Text string }       `json:"summary"`
		}
		_ = json.Unmarshal(p.outputItems[index], &item)
		if item.Type == "message" {
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		}
		if item.Type == "reasoning" {
			for _, part := range item.Summary {
				reasoning.WriteString(part.Text)
			}
		}
	}
	return text.String(), reasoning.String()
}

func sortedIndices[T any](items map[int]T) []int {
	indices := make([]int, 0, len(items))
	for index := range items {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

func (p *streamParser) replay() *Replay {
	if !p.done || p.replayInvalid {
		return nil
	}
	replay := &Replay{Protocol: p.protocol}
	switch p.protocol {
	case "/v1/messages":
		for expected, index := range sortedIndices(p.blocks) {
			if index != expected || !p.closedBlocks[index] {
				return nil
			}
			raw, err := json.Marshal(p.blocks[index])
			if err != nil {
				return nil
			}
			replay.Blocks = append(replay.Blocks, raw)
		}
	case "/responses":
		// Some proxies omit output snapshots. Keep their streamed text, but
		// don't claim the remaining tool/reasoning items are a complete replay.
		if text, _ := p.responseVisible(); text != p.completion.Content {
			return nil
		}
		for index := range p.outputAdded {
			if _, ok := p.outputItems[index]; !ok {
				return nil
			}
		}
		for expected, index := range sortedIndices(p.outputItems) {
			if index != expected {
				return nil
			}
			replay.Blocks = append(replay.Blocks, append(json.RawMessage(nil), p.outputItems[index]...))
		}
	default:
		return nil
	}
	if len(replay.Blocks) == 0 {
		return nil
	}
	if _, err := replayParts(replay, p.protocol); err != nil {
		return nil
	}
	// Never declare partial or malformed function calls authoritative.
	for _, call := range p.calls {
		var args map[string]any
		if call.ID == "" || call.Name == "" || json.Unmarshal([]byte(call.Arguments), &args) != nil || args == nil {
			return nil
		}
	}
	return replay
}
