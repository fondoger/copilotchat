package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Stream calls onDelta for each visible text fragment. Use StreamWithTools when
// tools are enabled so that the caller can execute calls and append their results.
func (c *Engine) Stream(ctx context.Context, request Request, onDelta func(string)) error {
	_, err := c.StreamWithTools(ctx, request, onDelta)
	return err
}

func (c *Engine) StreamWithTools(ctx context.Context, request Request, onDelta func(string)) (Completion, error) {
	return c.StreamWithEvents(ctx, request, func(event StreamEvent) {
		if event.Kind == EventText && onDelta != nil {
			onDelta(event.Delta)
		}
	})
}

// StreamWithEvents reports only provider-returned visible text and reasoning.
// Opaque signatures and encrypted reasoning stay in ProviderData, never events.
func (c *Engine) StreamWithEvents(ctx context.Context, request Request, onEvent func(StreamEvent)) (completion Completion, streamErr error) {
	model, err := c.backend.LookupModel(ctx, request.Model)
	if err != nil {
		return Completion{}, err
	}
	body, vision, err := RequestBody(request, model)
	if err != nil {
		return Completion{}, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Completion{}, err
	}
	req, err := c.backend.PrepareInference(ctx, request, model, encoded, vision)
	if err != nil {
		return Completion{}, err
	}
	started := time.Now()
	defer func() { completion.Metrics.Duration = time.Since(started) }()
	resp, err := c.backend.Do(req)
	if err != nil {
		return Completion{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Completion{}, ResponseError(resp)
	}
	parser := streamParser{onEvent: onEvent, started: started, calls: make(map[int]*ToolCall), blocks: make(map[int]map[string]any), protocol: model.Endpoint}
	if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if readErr != nil {
			return Completion{}, readErr
		}
		err = parser.single(model.Endpoint, data)
	} else {
		err = readSSE(resp.Body, func(event, data string) error {
			if data == "[DONE]" {
				parser.done = true
				return errStreamDone
			}
			switch model.Endpoint {
			case "/responses":
				return parser.responses(event, []byte(data))
			case "/v1/messages":
				return parser.anthropic(event, []byte(data))
			default:
				return parser.chat([]byte(data))
			}
		})
		if errors.Is(err, errStreamDone) {
			err = nil
		}
		if err == nil && !parser.done {
			err = fmt.Errorf("Copilot stream ended before completion: %w", io.ErrUnexpectedEOF)
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	result := parser.result()
	if err != nil {
		result.Replay = nil
	}
	if err == nil {
		for _, call := range result.ToolCalls {
			var args map[string]any
			if call.ID == "" || call.Name == "" || json.Unmarshal([]byte(call.Arguments), &args) != nil || args == nil {
				result.Replay = nil
				return result, errors.New("Copilot returned an incomplete or invalid tool call")
			}
		}
	}
	if err == nil {
		err = parser.nativeError
	}
	return result, err
}

var errStreamDone = errors.New("stream completed")

// readSSE supports CRLF, comments, multiline data, arbitrarily split network
// chunks, a UTF-8 BOM, and a final event without a trailing blank line.
func readSSE(reader io.Reader, handle func(event, data string) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var event string
	var data []string
	size := 0
	first := true
	dispatch := func() error {
		if len(data) == 0 {
			event, size = "", 0
			return nil
		}
		err := handle(event, strings.Join(data, "\n"))
		event, data, size = "", nil, 0
		return err
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			size += len(value)
			if size > 16<<20 {
				return errors.New("Copilot stream event exceeds 16 MiB")
			}
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}

type streamParser struct {
	completion       Completion
	onEvent          func(StreamEvent)
	started          time.Time
	firstVisibleSeen bool
	usageState       wireUsage
	done             bool
	calls            map[int]*ToolCall
	blocks           map[int]map[string]any
	protocol         string
	outputItems      map[int]json.RawMessage
	outputAdded      map[int]bool
	closedBlocks     map[int]bool
	blockJSON        map[int]string
	replayInvalid    bool
	nativeError      error
}

func (p *streamParser) text(delta string) {
	if delta == "" {
		return
	}
	p.completion.Content += delta
	p.emit(EventText, delta)
}

func (p *streamParser) reasoning(delta string) {
	if delta == "" {
		return
	}
	p.completion.Reasoning += delta
	p.emit(EventReasoning, delta)
}

func (p *streamParser) emit(kind, delta string) {
	if !p.firstVisibleSeen && !p.started.IsZero() {
		p.completion.Metrics.TimeToFirstToken = time.Since(p.started)
		p.firstVisibleSeen = true
	}
	if p.onEvent != nil {
		p.onEvent(StreamEvent{Kind: kind, Delta: delta})
	}
}

func (p *streamParser) result() Completion {
	result := p.completion
	indices := make([]int, 0, len(p.calls))
	for index := range p.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := *p.calls[index]
		if call.Arguments == "" {
			call.Arguments = "{}"
		}
		result.ToolCalls = append(result.ToolCalls, call)
	}
	result.Replay = p.replay()
	return result
}

func (p *streamParser) call(index int) *ToolCall {
	if p.calls == nil {
		p.calls = make(map[int]*ToolCall)
	}
	if p.calls[index] == nil {
		p.calls[index] = &ToolCall{}
	}
	return p.calls[index]
}

func streamError(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var detail struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(raw, &detail) == nil && detail.Message != "" {
		return fmt.Errorf("Copilot: %s", detail.Message)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return fmt.Errorf("Copilot: %s", text)
	}
	return errors.New("Copilot reported a streaming error")
}

func (p *streamParser) chat(data []byte) error {
	var chunk struct {
		Error   json.RawMessage `json:"error"`
		Usage   *wireUsage      `json:"usage"`
		Choices []struct {
			Index        int     `json:"index"`
			FinishReason *string `json:"finish_reason"`
			Delta        struct {
				Content         string `json:"content"`
				ReasoningText   string `json:"reasoning_text"`
				ReasoningOpaque string `json:"reasoning_opaque"`
				ToolCalls       []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return fmt.Errorf("invalid Copilot stream JSON: %w", err)
	}
	if err := streamError(chunk.Error); err != nil {
		return err
	}
	p.usage(chunk.Usage, "/chat/completions")
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			continue
		}
		p.text(choice.Delta.Content)
		p.reasoning(choice.Delta.ReasoningText)
		if choice.Delta.ReasoningOpaque != "" {
			p.completion.ReasoningOpaque = choice.Delta.ReasoningOpaque
		}
		for _, fragment := range choice.Delta.ToolCalls {
			call := p.call(fragment.Index)
			if fragment.ID != "" {
				call.ID = fragment.ID
			}
			call.Name += fragment.Function.Name
			call.Arguments += fragment.Function.Arguments
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			p.completion.StopReason = *choice.FinishReason
			p.done = true
			if *choice.FinishReason == "content_filter" {
				return errors.New("Copilot stopped the response because of its content filter")
			}
			if *choice.FinishReason == "length" {
				return errors.New("Copilot reached the model's output limit; the response is incomplete")
			}
		}
	}
	return nil
}

func (p *streamParser) responses(event string, data []byte) error {
	var chunk struct {
		Type        string          `json:"type"`
		Delta       string          `json:"delta"`
		OutputIndex int             `json:"output_index"`
		Item        json.RawMessage `json:"item"`
		Annotation  map[string]any  `json:"annotation"`
		Error       json.RawMessage `json:"error"`
		Message     string          `json:"message"`
		Response    struct {
			Usage             *wireUsage        `json:"usage"`
			Output            []json.RawMessage `json:"output"`
			Error             json.RawMessage   `json:"error"`
			IncompleteDetails struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return fmt.Errorf("invalid Copilot Responses event: %w", err)
	}
	if chunk.Type == "" {
		chunk.Type = event
	}
	p.protocol = "/responses"
	p.usage(chunk.Response.Usage, "/responses")
	switch chunk.Type {
	case "response.output_text.delta":
		p.text(chunk.Delta)
	case "response.reasoning_summary_text.delta":
		p.reasoning(chunk.Delta)
	case "response.output_text.annotation.added":
		p.searchMetadata(chunk.Annotation)
	case "response.output_item.added", "response.output_item.done":
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if err := json.Unmarshal(chunk.Item, &item); err != nil {
			return err
		}
		if chunk.Type == "response.output_item.added" {
			if p.outputAdded == nil {
				p.outputAdded = make(map[int]bool)
			}
			p.outputAdded[chunk.OutputIndex] = true
		}
		if item.Type == "function_call" {
			call := p.call(chunk.OutputIndex)
			call.ID, call.Name = item.CallID, item.Name
			if item.Arguments != "" {
				call.Arguments = item.Arguments
			}
		}
		if chunk.Type == "response.output_item.done" {
			if err := p.responseItem(chunk.OutputIndex, chunk.Item); err != nil {
				return err
			}
		}
	case "response.function_call_arguments.delta":
		p.call(chunk.OutputIndex).Arguments += chunk.Delta
	case "response.completed":
		for index, raw := range chunk.Response.Output {
			if err := p.responseItem(index, raw); err != nil {
				return err
			}
		}
		p.responseVisibleFallback()
		p.completion.StopReason = "completed"
		p.done = true
		return errStreamDone
	case "response.incomplete":
		return fmt.Errorf("Copilot response is incomplete: %s", chunk.Response.IncompleteDetails.Reason)
	case "response.failed":
		if err := streamError(chunk.Response.Error); err != nil {
			return err
		}
		return errors.New("Copilot response failed")
	case "error":
		if err := streamError(chunk.Error); err != nil {
			return err
		}
		if chunk.Message != "" {
			return fmt.Errorf("Copilot: %s", chunk.Message)
		}
		return errors.New("Copilot reported a streaming error")
	}
	return nil
}

func (p *streamParser) anthropic(event string, data []byte) error {
	var chunk struct {
		Type         string         `json:"type"`
		Index        int            `json:"index"`
		ContentBlock map[string]any `json:"content_block"`
		Delta        struct {
			Type        string         `json:"type"`
			Text        string         `json:"text"`
			Thinking    string         `json:"thinking"`
			Signature   string         `json:"signature"`
			PartialJSON string         `json:"partial_json"`
			StopReason  string         `json:"stop_reason"`
			Citation    map[string]any `json:"citation"`
		} `json:"delta"`
		Error   json.RawMessage `json:"error"`
		Usage   *wireUsage      `json:"usage"`
		Message struct {
			Usage *wireUsage `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return fmt.Errorf("invalid Copilot Messages event: %w", err)
	}
	if chunk.Type == "" {
		chunk.Type = event
	}
	p.protocol = "/v1/messages"
	p.usage(chunk.Message.Usage, "/v1/messages")
	p.usage(chunk.Usage, "/v1/messages")
	switch chunk.Type {
	case "content_block_start":
		block := chunk.ContentBlock
		if block == nil {
			return errors.New("Copilot returned an empty content block")
		}
		if p.blocks == nil {
			p.blocks = make(map[int]map[string]any)
		}
		p.blocks[chunk.Index] = block
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			p.text(text)
		case "tool_use":
			call := p.call(chunk.Index)
			call.ID, _ = block["id"].(string)
			call.Name, _ = block["name"].(string)
			if input, ok := block["input"].(map[string]any); ok && len(input) > 0 {
				encoded, _ := json.Marshal(input)
				call.Arguments = string(encoded)
			}
		case "thinking":
			thinking, _ := block["thinking"].(string)
			p.reasoning(thinking)
		}
	case "content_block_delta":
		switch chunk.Delta.Type {
		case "text_delta":
			p.text(chunk.Delta.Text)
			p.appendBlockString(chunk.Index, "text", chunk.Delta.Text)
		case "input_json_delta":
			block := p.blocks[chunk.Index]
			if block == nil || (block["type"] != "tool_use" && block["type"] != "server_tool_use") {
				return errors.New("Copilot returned tool input without a tool block")
			}
			if p.blockJSON == nil {
				p.blockJSON = make(map[int]string)
			}
			p.blockJSON[chunk.Index] += chunk.Delta.PartialJSON
			if block["type"] == "tool_use" {
				p.call(chunk.Index).Arguments += chunk.Delta.PartialJSON
			}
		case "citations_delta":
			if block := p.blocks[chunk.Index]; block != nil && chunk.Delta.Citation != nil {
				citations, _ := block["citations"].([]any)
				block["citations"] = append(citations, chunk.Delta.Citation)
			}
		case "thinking_delta":
			p.reasoning(chunk.Delta.Thinking)
			if block := p.blocks[chunk.Index]; block != nil {
				prior, _ := block["thinking"].(string)
				block["thinking"] = prior + chunk.Delta.Thinking
			}
		case "signature_delta":
			if block := p.blocks[chunk.Index]; block != nil {
				prior, _ := block["signature"].(string)
				block["signature"] = prior + chunk.Delta.Signature
			}
		}
	case "content_block_stop":
		if err := p.closeBlock(chunk.Index); err != nil {
			return err
		}
	case "message_delta":
		p.completion.StopReason = chunk.Delta.StopReason
		if chunk.Delta.StopReason == "max_tokens" {
			return errors.New("Copilot reached the model's output limit; the response is incomplete")
		}
	case "message_stop":
		p.done = true
		return errStreamDone
	case "error":
		if err := streamError(chunk.Error); err != nil {
			return err
		}
		return errors.New("Copilot reported a streaming error")
	}
	return nil
}

// Some proxies return a normal completion despite stream:true.
func (p *streamParser) single(endpoint string, data []byte) error {
	var result struct {
		Error   json.RawMessage `json:"error"`
		Usage   *wireUsage      `json:"usage"`
		Choices []struct {
			Message struct {
				Content         string `json:"content"`
				Reasoning       string `json:"reasoning_text"`
				ReasoningOpaque string `json:"reasoning_opaque"`
				ToolCalls       []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Output            []json.RawMessage `json:"output"`
		Content           []json.RawMessage `json:"content"`
		StopReason        string            `json:"stop_reason"`
		Status            string            `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	if err := streamError(result.Error); err != nil {
		return err
	}
	p.protocol = endpoint
	p.usage(result.Usage, endpoint)
	p.completion.StopReason = result.StopReason
	if result.Status == "failed" || result.Status == "incomplete" || result.StopReason == "max_tokens" {
		return fmt.Errorf("Copilot response is incomplete: %s %s %s", result.Status, result.StopReason, result.IncompleteDetails.Reason)
	}
	if endpoint == "/chat/completions" {
		if len(result.Choices) == 0 {
			return errors.New("Copilot returned an empty completion")
		}
		message := result.Choices[0].Message
		p.text(message.Content)
		p.reasoning(message.Reasoning)
		p.completion.ReasoningOpaque = message.ReasoningOpaque
		for i, call := range message.ToolCalls {
			*p.call(i) = ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments}
		}
		return nil
	}
	parts := result.Output
	if endpoint == "/v1/messages" {
		parts = result.Content
	}
	if len(parts) == 0 {
		return errors.New("Copilot returned an empty completion")
	}
	for index, raw := range parts {
		if endpoint == "/responses" {
			if err := p.responseItem(index, raw); err != nil {
				return err
			}
			continue
		}
		var block map[string]any
		if err := json.Unmarshal(raw, &block); err != nil || block == nil {
			return errors.New("invalid Copilot Messages content block")
		}
		if p.blocks == nil {
			p.blocks = make(map[int]map[string]any)
		}
		p.blocks[index] = block
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			p.text(text)
		case "thinking":
			thinking, _ := block["thinking"].(string)
			p.reasoning(thinking)
		case "tool_use":
			input, _ := json.Marshal(block["input"])
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			*p.call(index) = ToolCall{ID: id, Name: name, Arguments: string(input)}
		}
		if err := p.closeBlock(index); err != nil {
			return err
		}
	}
	if endpoint == "/responses" {
		p.responseVisibleFallback()
		p.completion.StopReason = result.Status
	}
	p.done = true
	return nil
}
