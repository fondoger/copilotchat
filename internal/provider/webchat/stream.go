package webchat

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/sse"
)

var streamDone = errors.New("web stream completed")

func streamFailure(kind string, raw json.RawMessage) error {
	var e struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	t := e.Error.Type
	if t == "" {
		t = e.Type
	}
	switch t {
	case "authentication_error", "invalid_api_key", "not_authenticated":
		return Failure(kind, 401)
	case "permission_error", "access_denied", "cloudflare_challenge":
		return Failure(kind, 403)
	case "rate_limit_error", "rate_limit_exceeded":
		return Failure(kind, 429)
	}
	return Failure(kind, 502)
}

func readClaude(reader io.Reader, id string, emit func(string, string)) (conversationState, error) {
	state := conversationState{ID: id}
	complete := false
	err := sse.Read(reader, 16<<20, func(event, data string) error {
		var chunk struct {
			Type    string `json:"type"`
			Message struct {
				UUID string `json:"uuid"`
				Role string `json:"role"`
			} `json:"message"`
			ContentBlock struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"content_block"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				Thinking   string `json:"thinking"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return Failure(Claude, 502)
		}
		if chunk.Type == "" {
			chunk.Type = event
		}
		if chunk.Type == "error" || len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return streamFailure(Claude, []byte(data))
		}
		switch chunk.Type {
		case "message_start":
			if chunk.Message.Role != "assistant" || !validID(chunk.Message.UUID) {
				return Failure(Claude, 502)
			}
			state.Parent = chunk.Message.UUID
		case "content_block_start":
			if chunk.ContentBlock.Type == "text" {
				emit(provider.EventText, chunk.ContentBlock.Text)
			}
			if chunk.ContentBlock.Type == "thinking" {
				emit(provider.EventReasoning, chunk.ContentBlock.Thinking)
			}
		case "content_block_delta":
			if chunk.Delta.Type == "text_delta" {
				emit(provider.EventText, chunk.Delta.Text)
			}
			if chunk.Delta.Type == "thinking_delta" {
				emit(provider.EventReasoning, chunk.Delta.Thinking)
			}
		case "message_delta":
			if chunk.Delta.StopReason == "max_tokens" {
				return Failure(Claude, 502)
			}
		case "message_stop":
			complete = true
			return streamDone
		}
		return nil
	})
	if err != nil && !errors.Is(err, streamDone) {
		return state, err
	}
	if !complete || !validID(state.ID) || !validID(state.Parent) {
		return state, Failure(Claude, 502)
	}
	return state, nil
}

type chatGPTMessage struct {
	ID     string `json:"id"`
	Author struct {
		Role string `json:"role"`
	} `json:"author"`
	Recipient string `json:"recipient"`
	Channel   string `json:"channel"`
	Content   struct {
		Type  string            `json:"content_type"`
		Parts []json.RawMessage `json:"parts"`
	} `json:"content"`
	Status   string `json:"status"`
	EndTurn  bool   `json:"end_turn"`
	Metadata struct {
		Hidden bool `json:"is_visually_hidden_from_conversation"`
	} `json:"metadata"`
	emitted string
}

func (m *chatGPTMessage) visibleText() (string, string) {
	if m.Author.Role != "assistant" || m.Recipient != "all" || m.Metadata.Hidden || m.Content.Type != "text" {
		return "", ""
	}
	kind := provider.EventText
	if m.Channel == "analysis" {
		kind = provider.EventReasoning
	} else if m.Channel != "" && m.Channel != "final" {
		return "", ""
	}
	var text strings.Builder
	for _, part := range m.Content.Parts {
		var value string
		if json.Unmarshal(part, &value) == nil {
			text.WriteString(value)
		}
	}
	return kind, text.String()
}

func (m *chatGPTMessage) patch(op, path string, value json.RawMessage) error {
	switch path {
	case "/message":
		return json.Unmarshal(value, m)
	case "/message/content":
		return json.Unmarshal(value, &m.Content)
	case "/message/content/parts":
		var parts []json.RawMessage
		if err := json.Unmarshal(value, &parts); err != nil {
			return err
		}
		if op == "append" {
			m.Content.Parts = append(m.Content.Parts, parts...)
		} else {
			m.Content.Parts = parts
		}
	case "/message/content/parts/0":
		var part string
		if err := json.Unmarshal(value, &part); err != nil {
			return err
		}
		if op == "append" && len(m.Content.Parts) > 0 {
			var before string
			if err := json.Unmarshal(m.Content.Parts[0], &before); err != nil {
				return err
			}
			part = before + part
		}
		encoded, _ := json.Marshal(part)
		if len(m.Content.Parts) == 0 {
			m.Content.Parts = []json.RawMessage{encoded}
		} else {
			m.Content.Parts[0] = encoded
		}
	case "/message/status":
		return json.Unmarshal(value, &m.Status)
	case "/message/end_turn":
		if string(value) == "null" {
			m.EndTurn = false
			return nil
		}
		return json.Unmarshal(value, &m.EndTurn)
	case "/message/id":
		return json.Unmarshal(value, &m.ID)
	case "/message/channel":
		return json.Unmarshal(value, &m.Channel)
	case "/message/recipient":
		return json.Unmarshal(value, &m.Recipient)
	case "/message/author":
		return json.Unmarshal(value, &m.Author)
	case "/message/metadata":
		var metadata struct {
			Hidden *bool `json:"is_visually_hidden_from_conversation"`
		}
		if err := json.Unmarshal(value, &metadata); err != nil {
			return err
		}
		if op == "replace" {
			m.Metadata.Hidden = false
		}
		if metadata.Hidden != nil {
			m.Metadata.Hidden = *metadata.Hidden
		}
	case "/message/metadata/is_visually_hidden_from_conversation":
		if op == "remove" {
			m.Metadata.Hidden = false
			return nil
		}
		return json.Unmarshal(value, &m.Metadata.Hidden)
	}
	return nil
}

func readChatGPT(reader io.Reader, id string, emit func(string, string)) (conversationState, error) {
	state := conversationState{ID: id}
	messages := map[int]*chatGPTMessage{}
	channel, op, path := 0, "", ""
	encoded, complete := false, false
	err := sse.Read(reader, 16<<20, func(event, data string) error {
		if data == "[DONE]" {
			if !complete {
				return Failure(ChatGPT, 502)
			}
			return streamDone
		}
		if event == "delta_encoding" {
			var version string
			if json.Unmarshal([]byte(data), &version) != nil || version != "v1" {
				return Failure(ChatGPT, 502)
			}
			encoded = true
			return nil
		}
		var chunk struct {
			Type           string          `json:"type"`
			ConversationID string          `json:"conversation_id"`
			Error          json.RawMessage `json:"error"`
			C              *int            `json:"c"`
			O              *string         `json:"o"`
			P              *string         `json:"p"`
			V              json.RawMessage `json:"v"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return Failure(ChatGPT, 502)
		}
		if chunk.Type == "error" || len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return streamFailure(ChatGPT, []byte(data))
		}
		if chunk.ConversationID != "" {
			if !validID(chunk.ConversationID) || state.ID != "" && state.ID != chunk.ConversationID {
				return Failure(ChatGPT, 502)
			}
			state.ID = chunk.ConversationID
		}
		if chunk.Type == "message_stream_complete" {
			complete = true
		}
		if event != "delta" {
			return nil
		}
		if !encoded {
			return Failure(ChatGPT, 502)
		}
		if chunk.C != nil {
			channel = *chunk.C
		}
		if channel < 0 || channel > 1024 {
			return Failure(ChatGPT, 502)
		}
		if chunk.O != nil {
			op = *chunk.O
		}
		if chunk.P != nil {
			path = *chunk.P
		}
		m := messages[channel]
		if m == nil {
			m = &chatGPTMessage{}
			messages[channel] = m
		}
		if (op == "add" || op == "replace") && path == "" {
			var snapshot struct {
				Message        json.RawMessage `json:"message"`
				ConversationID string          `json:"conversation_id"`
				Error          json.RawMessage `json:"error"`
			}
			if json.Unmarshal(chunk.V, &snapshot) != nil {
				return Failure(ChatGPT, 502)
			}
			if len(snapshot.Error) > 0 && string(snapshot.Error) != "null" {
				return streamFailure(ChatGPT, snapshot.Error)
			}
			if snapshot.ConversationID != "" {
				if !validID(snapshot.ConversationID) || state.ID != "" && state.ID != snapshot.ConversationID {
					return Failure(ChatGPT, 502)
				}
				state.ID = snapshot.ConversationID
			}
			if len(snapshot.Message) > 0 && json.Unmarshal(snapshot.Message, m) != nil {
				return Failure(ChatGPT, 502)
			}
		} else if op == "patch" {
			var patches []struct {
				Op    string          `json:"o"`
				Path  string          `json:"p"`
				Value json.RawMessage `json:"v"`
			}
			if json.Unmarshal(chunk.V, &patches) != nil {
				return Failure(ChatGPT, 502)
			}
			for _, p := range patches {
				if err := m.patch(p.Op, p.Path, p.Value); err != nil {
					return Failure(ChatGPT, 502)
				}
			}
		} else if err := m.patch(op, path, chunk.V); err != nil {
			return Failure(ChatGPT, 502)
		}
		kind, text := m.visibleText()
		if kind == "" {
			return nil
		}
		if !strings.HasPrefix(text, m.emitted) {
			return Failure(ChatGPT, 502)
		}
		emit(kind, strings.TrimPrefix(text, m.emitted))
		m.emitted = text
		if kind == provider.EventText && m.EndTurn && m.Status == "finished_successfully" {
			state.Parent = m.ID
		}
		return nil
	})
	if !errors.Is(err, streamDone) {
		if err != nil {
			return state, err
		}
		return state, Failure(ChatGPT, 502)
	}
	if !validID(state.ID) || !validID(state.Parent) {
		return state, Failure(ChatGPT, 502)
	}
	return state, nil
}
