package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// RequestBody encodes a model request without modifying stored history.
func RequestBody(request Request, model Model) (map[string]any, bool, error) {
	if strings.TrimSpace(request.Model) == "" {
		return nil, false, errors.New("select a Copilot model first")
	}
	if len(request.Messages) == 0 {
		return nil, false, errors.New("the conversation is empty")
	}
	body := map[string]any{"model": request.Model, "stream": true}
	var messages []any
	var systems []any
	vision := false
	for _, message := range request.Messages {
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
			return nil, false, fmt.Errorf("unsupported message role %q", message.Role)
		}
		if message.Role == "tool" && message.ToolCallID == "" {
			return nil, false, errors.New("tool result is missing its call ID")
		}
		text, files, err := prepareAttachments(message.Content, message.Attachments, model)
		if err != nil {
			return nil, false, err
		}
		for _, file := range files {
			vision = vision || strings.HasPrefix(file.MIME, "image/")
		}
		switch model.Endpoint {
		case "/responses":
			if message.Role == "tool" {
				messages = append(messages, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": text})
				continue
			}
			for _, raw := range message.ProviderData {
				var part map[string]any
				if json.Unmarshal(raw, &part) == nil && part["type"] == "reasoning" {
					messages = append(messages, part)
				}
			}
			parts := make([]any, 0)
			if text != "" {
				typeName := "input_text"
				if message.Role == "assistant" {
					typeName = "output_text"
				}
				parts = append(parts, map[string]any{"type": typeName, "text": text})
			}
			for _, file := range files {
				if strings.HasPrefix(file.MIME, "image/") {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": dataURL(file)})
				} else {
					parts = append(parts, map[string]any{"type": "input_file", "filename": file.Name, "file_data": dataURL(file)})
				}
			}
			if len(parts) > 0 {
				messages = append(messages, map[string]any{"role": message.Role, "content": parts})
			}
			for _, call := range message.ToolCalls {
				messages = append(messages, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
			}
		case "/v1/messages":
			if message.Role == "system" {
				if text != "" {
					systems = append(systems, map[string]any{"type": "text", "text": text})
				}
				continue
			}
			parts := make([]any, 0)
			role := message.Role
			if role == "tool" {
				role = "user"
				parts = append(parts, map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": text})
			} else {
				for _, raw := range message.ProviderData {
					var part map[string]any
					if json.Unmarshal(raw, &part) == nil && (part["type"] == "thinking" || part["type"] == "redacted_thinking") {
						parts = append(parts, part)
					}
				}
				if text != "" {
					parts = append(parts, map[string]any{"type": "text", "text": text})
				}
				for _, file := range files {
					typeName := "image"
					if file.MIME == "application/pdf" {
						typeName = "document"
					}
					parts = append(parts, map[string]any{"type": typeName, "source": map[string]any{"type": "base64", "media_type": file.MIME, "data": file.Data}})
				}
				for _, call := range message.ToolCalls {
					var input map[string]any
					if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil || input == nil {
						return nil, false, fmt.Errorf("invalid arguments for tool %s", call.Name)
					}
					parts = append(parts, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input})
				}
			}
			if len(parts) == 0 {
				continue
			}
			// Consecutive tool results share a single user message in Anthropic's protocol.
			if len(messages) > 0 {
				last := messages[len(messages)-1].(map[string]any)
				if last["role"] == role {
					last["content"] = append(last["content"].([]any), parts...)
					continue
				}
			}
			messages = append(messages, map[string]any{"role": role, "content": parts})
		default:
			if text == "" && len(files) == 0 && len(message.ToolCalls) == 0 && message.ReasoningOpaque == "" && message.Role != "tool" {
				continue
			}
			item := map[string]any{"role": message.Role, "content": text}
			if len(files) > 0 {
				parts := []any{map[string]any{"type": "text", "text": text}}
				for _, file := range files {
					if !strings.HasPrefix(file.MIME, "image/") {
						return nil, false, errors.New("this model's chat endpoint cannot accept PDF attachments")
					}
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL(file)}})
				}
				item["content"] = parts
			}
			if message.Role == "tool" {
				item["tool_call_id"] = message.ToolCallID
			}
			if message.ReasoningOpaque != "" {
				item["reasoning_opaque"] = message.ReasoningOpaque
				if message.Reasoning != "" {
					item["reasoning_text"] = message.Reasoning
				}
			}
			if len(message.ToolCalls) > 0 {
				calls := make([]any, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": call.Arguments}})
				}
				item["tool_calls"] = calls
				if text == "" {
					item["content"] = nil
				}
			}
			messages = append(messages, item)
		}
	}
	if model.Endpoint == "/responses" {
		body["input"] = messages
		body["store"] = false
		body["include"] = []string{"reasoning.encrypted_content"}
	} else {
		body["messages"] = messages
	}
	if model.Endpoint == "/v1/messages" {
		if len(systems) > 0 {
			body["system"] = systems
		}
		maxTokens := 8192
		if model.MaxOutputTokens > 0 && model.MaxOutputTokens < maxTokens {
			maxTokens = model.MaxOutputTokens
		}
		body["max_tokens"] = maxTokens
	}
	if len(request.Tools) > 0 {
		if model.ToolsKnown && !model.Tools {
			return nil, false, fmt.Errorf("%s does not support tool calls", model.Name)
		}
		tools := make([]any, 0, len(request.Tools))
		// MCP servers may enumerate the same definitions in a different order.
		// Canonical ordering keeps the tool prefix stable without mutating callers.
		ordered := append([]Tool(nil), request.Tools...)
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
		for _, tool := range ordered {
			if tool.Name == "" {
				return nil, false, errors.New("tool name is missing")
			}
			schema := tool.InputSchema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			switch model.Endpoint {
			case "/responses":
				tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": schema, "strict": false})
			case "/v1/messages":
				tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": schema})
			default:
				tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": schema}})
			}
		}
		body["tools"] = tools
	}
	if err := applyThinking(body, request, model); err != nil {
		return nil, false, err
	}
	applyPromptCache(body, request, model)
	return body, vision, nil
}

func applyThinking(body map[string]any, request Request, model Model) error {
	if request.ReasoningEffort != "" && request.ThinkingBudgetTokens != 0 {
		return errors.New("choose a reasoning effort or a thinking budget, not both")
	}
	if effort := request.ReasoningEffort; effort != "" {
		advertised := false
		for _, supported := range model.ThinkingEfforts {
			advertised = advertised || supported == effort
		}
		if !advertised {
			return fmt.Errorf("%s does not advertise reasoning effort %q", model.Name, effort)
		}
		switch model.Endpoint {
		case "/responses":
			body["reasoning"] = map[string]any{"effort": effort, "summary": "auto"}
		case "/v1/messages":
			if !model.AdaptiveThinking {
				return fmt.Errorf("%s does not advertise adaptive thinking", model.Name)
			}
			body["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
			body["output_config"] = map[string]any{"effort": effort}
		default:
			body["reasoning_effort"] = effort
		}
	}
	if budget := request.ThinkingBudgetTokens; budget != 0 {
		minimum := max(1, model.MinThinkingBudget)
		if model.Endpoint == "/v1/messages" {
			minimum = max(1024, minimum) // Anthropic's enabled-thinking protocol minimum.
		}
		if model.MaxThinkingBudget <= 0 || budget < minimum || budget > model.MaxThinkingBudget {
			return fmt.Errorf("%s does not advertise a thinking budget supporting %d tokens", model.Name, budget)
		}
		switch model.Endpoint {
		case "/v1/messages":
			if model.AdaptiveThinking {
				return errors.New("this model uses adaptive thinking; choose an advertised effort instead")
			}
			maxTokens := max(body["max_tokens"].(int), budget+1)
			if model.MaxOutputTokens > 0 && maxTokens > model.MaxOutputTokens {
				return errors.New("thinking budget must be smaller than the model's maximum output tokens")
			}
			body["max_tokens"] = maxTokens
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		case "/chat/completions":
			body["thinking_budget"] = budget
		default:
			return errors.New("the Responses endpoint does not support an explicit thinking budget")
		}
	}
	return nil
}

func prepareAttachments(text string, attachments []Attachment, model Model) (string, []Attachment, error) {
	var files []Attachment
	for _, attachment := range attachments {
		data, err := base64.StdEncoding.DecodeString(attachment.Data)
		if err != nil {
			return "", nil, fmt.Errorf("attachment %q has invalid base64 data", attachment.Name)
		}
		if strings.HasPrefix(attachment.MIME, "image/") {
			if !model.Vision {
				return "", nil, fmt.Errorf("%s does not support image attachments", model.Name)
			}
			files = append(files, attachment)
		} else if attachment.MIME == "application/pdf" {
			if !model.PDF {
				return "", nil, fmt.Errorf("%s does not advertise PDF support; choose a PDF-capable model or attach extracted text", model.Name)
			}
			files = append(files, attachment)
		} else if utf8.Valid(data) && !strings.ContainsRune(string(data), '\x00') {
			text += "\n\n--- Attachment: " + attachment.Name + " ---\n" + string(data)
		} else {
			return "", nil, fmt.Errorf("attachment %q is not readable text or a supported image/PDF", attachment.Name)
		}
	}
	return text, files, nil
}

func dataURL(file Attachment) string {
	return "data:" + file.MIME + ";base64," + file.Data
}
