package localproxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
)

var errResponseFinished = errors.New("Responses terminal event received")

func messagesUsage(response object) object {
	u := obj(response["usage"])
	cached := max(0, number(obj(u["input_tokens_details"])["cached_tokens"]))
	input := max(0, number(u["input_tokens"]))
	cached = min(cached, input)
	// Responses input_tokens includes cached input. Anthropic reports them in
	// separate buckets; adding them must reproduce the original total.
	return object{"input_tokens": input - cached, "output_tokens": max(0, number(u["output_tokens"])),
		"cache_read_input_tokens": cached, "cache_creation_input_tokens": 0}
}

func terminalError(response object) error {
	status := text(response["status"])
	if status == "failed" || status == "cancelled" || response["error"] != nil {
		message := text(obj(response["error"])["message"])
		if message == "" {
			message = "Responses request " + status
		}
		return errors.New(message)
	}
	if status == "incomplete" && text(obj(response["incomplete_details"])["reason"]) != "max_output_tokens" {
		return fmt.Errorf("Responses request incomplete: %s", text(obj(response["incomplete_details"])["reason"]))
	}
	return nil
}

func stopReason(response object, tools bool) string {
	if text(response["status"]) == "incomplete" {
		return "max_tokens"
	}
	if tools {
		return "tool_use"
	}
	return "end_turn"
}

func reasoningText(item object) string {
	var parts []string
	for _, value := range array(item["summary"]) {
		if s := text(obj(value)["text"]); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

func responseText(part object) (string, error) {
	switch text(part["type"]) {
	case "output_text":
		return text(part["text"]), nil
	case "refusal":
		return text(part["refusal"]), nil
	default:
		return "", fmt.Errorf("Responses content part %q cannot be converted", text(part["type"]))
	}
}

func responseTool(item object) (object, error) {
	if text(item["call_id"]) == "" || text(item["name"]) == "" {
		return nil, errors.New("Responses tool call is missing its call ID or name")
	}
	args := text(item["arguments"])
	if args == "" {
		args = "{}"
	}
	input, err := decodeObject([]byte(args))
	if err != nil {
		return nil, fmt.Errorf("Responses returned invalid tool arguments: %w", err)
	}
	return object{"type": "tool_use", "id": item["call_id"], "name": item["name"], "input": input}, nil
}

func responsesToMessage(response object, model string) (object, error) {
	if err := terminalError(response); err != nil {
		return nil, err
	}
	if array(response["output"]) == nil {
		return nil, errors.New("Responses reply has no output array")
	}
	content := []any{}
	tools := false
	for _, value := range array(response["output"]) {
		item := obj(value)
		switch text(item["type"]) {
		case "message":
			for _, v := range array(item["content"]) {
				value, err := responseText(obj(v))
				if err != nil {
					return nil, err
				}
				content = append(content, object{"type": "text", "text": value})
			}
		case "function_call":
			block, err := responseTool(item)
			if err != nil {
				return nil, err
			}
			content, tools = append(content, block), true
		case "reasoning":
			// Preserve the complete encrypted reasoning item through Anthropic's
			// signature so a stateless tool continuation can replay it unchanged.
			content = append(content, object{"type": "thinking", "thinking": reasoningText(item), "signature": reasoningSignature(item)})
		default:
			return nil, fmt.Errorf("Responses output item %q cannot be converted", text(item["type"]))
		}
	}
	id := text(response["id"])
	if id == "" {
		id = "msg_" + provider.RequestID()
	}
	return object{"id": id, "type": "message", "role": "assistant", "model": model, "content": content,
		"stop_reason": stopReason(response, tools), "stop_sequence": nil, "usage": messagesUsage(response)}, nil
}

func writeMessagesResponse(w http.ResponseWriter, response *http.Response, model string, stream bool) {
	w.Header().Set("Cache-Control", "no-store")
	if response.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, MaxBody))
		parsed, _ := decodeObject(data)
		message := text(obj(parsed["error"])["message"])
		if message == "" {
			message = fmt.Sprintf("Upstream Responses request failed (%d)", response.StatusCode)
		}
		apiError(w, response.StatusCode, message)
		return
	}
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		if stream {
			s := newMessagesStream(w, model)
			if err := readResponseEvents(response.Body, s.event); err != nil && !s.finished {
				s.fail(err)
			}
			if !s.finished {
				s.fail(errors.New("Responses stream ended before a terminal event"))
			}
			return
		}
		// Some compatible services always stream. Honor a non-streaming client
		// by consuming the terminal response object, never returning raw SSE.
		var terminal object
		err := readResponseEvents(response.Body, func(e object) error {
			switch text(e["type"]) {
			case "response.completed", "response.incomplete":
				terminal = obj(e["response"])
				return errResponseFinished
			case "error", "response.failed":
				return streamError(e)
			}
			return nil
		})
		if (err != nil && !errors.Is(err, errResponseFinished)) || terminal == nil {
			apiError(w, 502, "Responses stream did not return a complete response")
			return
		}
		writeMessageJSON(w, terminal, model, false)
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxBody+1))
	if err != nil || len(data) > MaxBody {
		apiError(w, 502, "Could not read Responses reply")
		return
	}
	parsed, err := decodeObject(data)
	if err != nil {
		apiError(w, 502, "Provider returned invalid Responses JSON")
		return
	}
	writeMessageJSON(w, parsed, model, stream)
}

func writeMessageJSON(w http.ResponseWriter, response object, model string, stream bool) {
	message, err := responsesToMessage(response, model)
	if err != nil {
		apiError(w, 502, err.Error())
		return
	}
	if stream {
		s := newMessagesStream(w, model)
		if err := s.complete(response); err != nil {
			s.fail(err)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(message)
}

// Read complete SSE frames (including multi-line data), not transport chunks.
func readResponseEvents(r io.Reader, event func(object) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), MaxBody)
	var data []string
	size := 0
	dispatch := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data, size = nil, 0
		if payload == "[DONE]" {
			return nil
		}
		e, err := decodeObject([]byte(payload))
		if err != nil {
			return errors.New("Provider returned invalid Responses SSE JSON")
		}
		return event(e)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimPrefix(line, "data:")
			line = strings.TrimPrefix(line, " ")
			size += len(line)
			if size > MaxBody {
				return errors.New("Responses SSE event exceeds 32 MiB")
			}
			data = append(data, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}

type messageBlock struct {
	index        int
	kind         string
	value        strings.Builder
	stopped      bool
	summaryIndex int
}

type messagesStream struct {
	w                        http.ResponseWriter
	model                    string
	started, finished, tools bool
	blocks                   []*messageBlock
	byKey                    map[string]*messageBlock
}

func newMessagesStream(w http.ResponseWriter, model string) *messagesStream {
	return &messagesStream{w: w, model: model, byKey: map[string]*messageBlock{}}
}

func (s *messagesStream) send(kind string, value object) error {
	s.w.Header().Set("Content-Type", "text/event-stream")
	value["type"] = kind
	data, _ := json.Marshal(value)
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
		return err
	}
	return http.NewResponseController(s.w).Flush()
}

func (s *messagesStream) start(response object) error {
	if s.started {
		return nil
	}
	s.started = true
	id := text(response["id"])
	if id == "" {
		id = "msg_" + provider.RequestID()
	}
	return s.send("message_start", object{"message": object{"id": id, "type": "message", "role": "assistant", "model": s.model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": messagesUsage(response)}})
}

func (s *messagesStream) block(key, kind string, initial object) (*messageBlock, error) {
	if b := s.byKey[key]; b != nil {
		return b, nil
	}
	if err := s.start(nil); err != nil {
		return nil, err
	}
	b := &messageBlock{index: len(s.blocks), kind: kind}
	s.blocks, s.byKey[key] = append(s.blocks, b), b
	return b, s.send("content_block_start", object{"index": b.index, "content_block": initial})
}

func (s *messagesStream) delta(b *messageBlock, value string) error {
	if value == "" {
		return nil
	}
	if b.stopped {
		return errors.New("Responses sent a delta after closing its content block")
	}
	b.value.WriteString(value)
	field, kind := "text", "text_delta"
	if b.kind == "thinking" {
		field, kind = "thinking", "thinking_delta"
	}
	if b.kind == "tool_use" {
		field, kind = "partial_json", "input_json_delta"
	}
	return s.send("content_block_delta", object{"index": b.index, "delta": object{"type": kind, field: value}})
}

func (s *messagesStream) fill(b *messageBlock, full string) error {
	current := b.value.String()
	if current == full {
		return nil
	}
	if !strings.HasPrefix(full, current) {
		return errors.New("Responses terminal content differs from streamed content")
	}
	return s.delta(b, strings.TrimPrefix(full, current))
}

func (s *messagesStream) closeBlock(b *messageBlock) error {
	if b.stopped {
		return nil
	}
	b.stopped = true
	return s.send("content_block_stop", object{"index": b.index})
}

func blockKey(output, part int) string { return fmt.Sprintf("text:%d:%d", output, part) }

func (s *messagesStream) item(index int, item object, done bool) error {
	switch text(item["type"]) {
	case "function_call":
		if !done && (text(item["call_id"]) == "" || text(item["name"]) == "") {
			return nil
		}
		if done {
			if _, err := responseTool(item); err != nil {
				return err
			}
		}
		b, err := s.block(fmt.Sprintf("tool:%d", index), "tool_use", object{"type": "tool_use", "id": item["call_id"], "name": item["name"], "input": object{}})
		if err != nil {
			return err
		}
		s.tools = true
		if done {
			args := text(item["arguments"])
			if args == "" {
				args = "{}"
			}
			if err := s.fill(b, args); err != nil {
				return err
			}
			return s.closeBlock(b)
		}
	case "reasoning":
		if !done {
			return nil
		}
		b, err := s.block(fmt.Sprintf("thinking:%d", index), "thinking", object{"type": "thinking", "thinking": ""})
		if err != nil {
			return err
		}
		if b.stopped {
			return nil
		}
		if err := s.fill(b, reasoningText(item)); err != nil {
			return err
		}
		if err := s.send("content_block_delta", object{"index": b.index, "delta": object{"type": "signature_delta", "signature": reasoningSignature(item)}}); err != nil {
			return err
		}
		return s.closeBlock(b)
	case "message":
		if !done {
			return nil
		}
		for i, value := range array(item["content"]) {
			value, err := responseText(obj(value))
			if err != nil {
				return err
			}
			b, err := s.block(blockKey(index, i), "text", object{"type": "text", "text": ""})
			if err != nil {
				return err
			}
			if err := s.fill(b, value); err != nil {
				return err
			}
			if err := s.closeBlock(b); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("Responses output item %q cannot be converted", text(item["type"]))
	}
	return nil
}

func (s *messagesStream) complete(response object) error {
	if err := terminalError(response); err != nil {
		return err
	}
	if array(response["output"]) == nil {
		return errors.New("Responses terminal event has no output array")
	}
	if err := s.start(response); err != nil {
		return err
	}
	for i, value := range array(response["output"]) {
		if err := s.item(i, obj(value), true); err != nil {
			return err
		}
	}
	for _, b := range s.blocks {
		if err := s.closeBlock(b); err != nil {
			return err
		}
	}
	if err := s.send("message_delta", object{"delta": object{"stop_reason": stopReason(response, s.tools), "stop_sequence": nil}, "usage": messagesUsage(response)}); err != nil {
		return err
	}
	s.finished = true
	return s.send("message_stop", object{})
}

func streamError(e object) error {
	message := text(e["message"])
	if message == "" {
		message = text(obj(e["error"])["message"])
	}
	if response := obj(e["response"]); response != nil {
		if err := terminalError(response); err != nil {
			return err
		}
	}
	if message == "" {
		message = "Responses stream failed"
	}
	return errors.New(message)
}

func (s *messagesStream) fail(err error) {
	if s.finished {
		return
	}
	s.finished = true
	_ = s.send("error", object{"error": object{"type": "api_error", "message": err.Error()}})
}

func (s *messagesStream) event(e object) error {
	if s.finished {
		return nil
	}
	index := number(e["output_index"])
	switch text(e["type"]) {
	case "response.created":
		return s.start(obj(e["response"]))
	case "response.output_item.added":
		return s.item(index, obj(e["item"]), false)
	case "response.output_item.done":
		return s.item(index, obj(e["item"]), true)
	case "response.output_text.delta", "response.refusal.delta":
		b, err := s.block(blockKey(index, number(e["content_index"])), "text", object{"type": "text", "text": ""})
		if err != nil {
			return err
		}
		return s.delta(b, text(e["delta"]))
	case "response.function_call_arguments.delta":
		b := s.byKey[fmt.Sprintf("tool:%d", index)]
		if b == nil {
			return nil
		} // Late call ID/name: output_item.done supplies complete arguments.
		return s.delta(b, text(e["delta"]))
	case "response.reasoning_summary_text.delta":
		b, err := s.block(fmt.Sprintf("thinking:%d", index), "thinking", object{"type": "thinking", "thinking": ""})
		if err != nil {
			return err
		}
		if part := number(e["summary_index"]); part > b.summaryIndex {
			b.summaryIndex = part
			if err := s.delta(b, "\n"); err != nil {
				return err
			}
		}
		return s.delta(b, text(e["delta"]))
	case "response.completed", "response.incomplete":
		if err := s.complete(obj(e["response"])); err != nil {
			return err
		}
		return errResponseFinished
	case "error", "response.failed":
		return streamError(e)
	}
	return nil
}
