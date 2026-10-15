package copilot

import (
	"crypto/sha256"
	"encoding/hex"
)

// A routing hint stays constant across follow-ups and tool rounds. It is not a
// cache-hit guarantee, and does not replace sending the complete conversation.
func conversationCacheKey(id string) string {
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:])
}

func applyPromptCache(body map[string]any, request Request, model Model) {
	switch model.Endpoint {
	case "/responses":
		if request.ConversationID != "" {
			body["prompt_cache_key"] = conversationCacheKey(request.ConversationID)
		}
	case "/v1/messages":
		applyAnthropicCache(body)
		// Copilot's Chat Completions route does not advertise support for a
		// prompt_cache_key body field or Anthropic cache_control extensions.
		// It receives the stable session-affinity headers instead.
	}
}

// Anthropic's explicit cache has four breakpoints. Reserve stable boundaries
// for tools and system instructions, then advance the conversation boundary
// after each request. Thinking/signature blocks are never modified.
func applyAnthropicCache(body map[string]any) {
	remaining := 4
	mark := func(block map[string]any) {
		if remaining == 0 || block == nil {
			return
		}
		if _, exists := block["cache_control"]; exists {
			return
		}
		block["cache_control"] = map[string]any{"type": "ephemeral"}
		remaining--
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		mark(tools[len(tools)-1].(map[string]any))
	}
	if systems, ok := body["system"].([]any); ok && len(systems) > 0 {
		mark(systems[0].(map[string]any))
		if len(systems) > 1 {
			mark(systems[len(systems)-1].(map[string]any))
		}
	}
	messages, _ := body["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i].(map[string]any)
		parts, _ := message["content"].([]any)
		for j := len(parts) - 1; j >= 0; j-- {
			part := parts[j].(map[string]any)
			switch part["type"] {
			case "text", "image", "document", "tool_use", "tool_result":
				mark(part)
				return
			}
		}
	}
}
