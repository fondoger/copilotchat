package doubao

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

func validateRequest(req provider.Request) error {
	if resumeConversation(req).ID != "" {
		req.Messages = req.Messages[len(req.Messages)-1:]
	}
	if len(req.Tools) > 0 {
		return errors.New("Doubao does not support tool calls")
	}
	count, total := 0, 0
	for _, msg := range req.Messages {
		// Check limits before the shared encoder decodes attachment base64.
		for _, file := range msg.Attachments {
			if !strings.HasPrefix(file.MIME, "image/") {
				continue
			}
			count++
			if count > 10 || len(file.Data) > (maxImage+2)/3*4 {
				return errors.New("Doubao image count or size limit exceeded")
			}
			padding := len(file.Data) - len(strings.TrimRight(file.Data, "="))
			size := base64.StdEncoding.DecodedLen(len(file.Data)) - padding
			total += size
			if size > maxImage || total > 20<<20 {
				return errors.New("Doubao image size limit exceeded")
			}
		}
		if msg.Role == "tool" || len(msg.ToolCalls) > 0 || msg.ToolCallID != "" {
			return errors.New("Doubao does not support tool-call history")
		}
		// Native replay is not a second, hidden route for tool history.
		if msg.Replay != nil {
			for _, raw := range msg.Replay.Blocks {
				var block map[string]any
				if json.Unmarshal(raw, &block) != nil || hasToolContent(block) {
					return errors.New("Doubao cannot replay this assistant history")
				}
			}
		}
	}
	return nil
}

func hasToolContent(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if key == "tool_calls" || key == "tool_call_id" || key == "function_call" ||
				(key == "type" && (item == "tool_use" || item == "tool_result" || item == "server_tool_use" || item == "web_search_tool_result" || item == "web_search_call" || item == "function_call" || item == "function_call_output")) {
				return true
			}
			if hasToolContent(item) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if hasToolContent(item) {
				return true
			}
		}
	}
	return false
}

type chatInput struct {
	conversationID string
	text           string
	images         []inputImage
	mode           int
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func prepareInput(encoded []byte, effort, conversationID string) (*chatInput, error) {
	input := &chatInput{conversationID: conversationID}
	switch effort {
	case "", "off":
	case "medium":
		input.mode = 1
	case "high":
		input.mode = 3
	default:
		return nil, errors.New("unsupported Doubao thinking mode")
	}
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(encoded, &body) != nil || len(body.Messages) == 0 {
		return nil, protocolError()
	}
	if conversationID != "" {
		body.Messages = body.Messages[len(body.Messages)-1:]
	}
	var history []chatMessage
	total := 0
	for _, msg := range body.Messages {
		if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "system" {
			return nil, errors.New("unsupported Doubao history role")
		}
		var text string
		if json.Unmarshal(msg.Content, &text) == nil {
			history = append(history, chatMessage{Role: msg.Role, Content: text})
			continue
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if json.Unmarshal(msg.Content, &parts) != nil || len(parts) == 0 {
			return nil, protocolError()
		}
		var content []any
		for _, part := range parts {
			switch part.Type {
			case "text":
				content = append(content, map[string]any{"type": "text", "text": part.Text})
			case "image_url":
				if len(input.images) >= 10 {
					return nil, errors.New("Doubao accepts at most 10 images per request")
				}
				image, err := parseImage(part.ImageURL.URL, len(input.images)+1)
				if err != nil {
					return nil, err
				}
				total += len(image.data)
				if total > 20<<20 {
					return nil, errors.New("Doubao images exceed 20 MiB in total")
				}
				input.images = append(input.images, image)
				content = append(content, map[string]any{"type": "image", "attachment": len(input.images)})
			default:
				return nil, errors.New("unsupported Doubao message content")
			}
		}
		history = append(history, chatMessage{Role: msg.Role, Content: content})
	}
	text, singleText := history[0].Content.(string)
	if len(history) == 1 && history[0].Role == "user" && singleText {
		input.text = text
	} else {
		// JSON keeps roles and image positions unambiguous even if user text
		// contains quotes, fake delimiters, or instruction-looking role tags.
		data, err := json.Marshal(history)
		if err != nil {
			return nil, protocolError()
		}
		input.text = string(data)
	}
	return input, nil
}

func (input *chatInput) payload(attachments []map[string]any) ([]byte, error) {
	blocks := make([]any, 0, len(attachments)+1)
	for _, attachment := range attachments {
		blocks = append(blocks, map[string]any{
			"block_type": 10052, "block_id": provider.RequestID(), "parent_id": "", "meta_info": []any{}, "append_fields": []any{},
			"is_finish": true, "patch_type": 2,
			"content": map[string]any{"attachment_block": map[string]any{"attachments": []any{attachment}}, "pc_event_block": ""},
		})
	}
	blocks = append(blocks, map[string]any{
		"block_type": 10000, "block_id": provider.RequestID(), "parent_id": "", "meta_info": []any{}, "append_fields": []any{},
		"content": map[string]any{"text_block": map[string]any{"text": input.text, "icon_url": "", "icon_url_dark": "", "summary": ""}, "pc_event_block": ""},
	})
	mode := strconv.Itoa(input.mode)
	localID := ""
	if input.conversationID == "" {
		localID = "local_" + provider.RequestID()
	}
	return json.Marshal(map[string]any{
		"client_meta": map[string]any{
			"local_conversation_id": localID, "conversation_id": input.conversationID, "bot_id": "7338286299411103781",
			"last_section_id": "", "last_message_index": nil,
			"local_permissions": []any{
				map[string]any{"permission_name": "ACCESS_COARSE_LOCATION", "status": 3},
				map[string]any{"permission_name": "ACCESS_FINE_LOCATION", "status": 3},
				map[string]any{"permission_name": "ACCESS_BACKGROUND_LOCATION", "status": 3},
			},
		},
		"messages": []any{map[string]any{"local_message_id": provider.RequestID(), "content_block": blocks, "message_status": 0}},
		"option": map[string]any{
			"create_time_ms": time.Now().UnixMilli(), "agent_mode": 2, "need_deep_think": input.mode,
			"need_create_conversation": input.conversationID == "", "conversation_init_option": map[string]any{"need_ack_conversation": true},
			"conversation_init_ext":  map[string]any{"model_item_key": "0", "reasoning_effort": mode, "mode_id": "1"},
			"sse_recv_event_options": map[string]any{"support_chunk_delta": true},
			"model_config":           map[string]any{"model_item_key": "0", "model_extra_params": map[string]any{}, "reasoning_effort": input.mode},
			"aggregate_params":       map[string]any{"conversation_mode": "1", "mode_id": "1", "model_item_key": "0", "agent_mode": "2", "reasoning_effort": mode, "provider_id": ""},
			"conversation_mode":      1,
			"recovery_option":        map[string]any{"is_recovery": false, "req_create_time_sec": time.Now().Unix(), "append_sse_event_scene": 0},
		},
		"user_context": []any{},
		"ext": map[string]any{"agent_mode": "2", "use_deep_think": mode, "sub_conv_firstmet_type": "1", "is_finish": "1",
			"conversation_init_option": `{"need_ack_conversation":true}`, "commerce_credit_config_enable": "0"},
	})
}
