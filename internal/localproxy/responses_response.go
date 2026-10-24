package localproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf16"

	"github.com/fondoger/copilotchat/internal/provider"
)

func responsesID(message object) string {
	if id := text(message["id"]); id != "" {
		return "resp_" + id
	}
	return "resp_" + provider.RequestID()
}

func responsesUsage(message object) object {
	u := obj(message["usage"])
	cached := max(0, number(u["cache_read_input_tokens"]))
	input := max(0, number(u["input_tokens"])) + max(0, number(u["cache_creation_input_tokens"])) + cached
	output := max(0, number(u["output_tokens"]))
	return object{"input_tokens": input, "output_tokens": output, "total_tokens": input + output,
		"input_tokens_details": object{"cached_tokens": cached}, "output_tokens_details": object{"reasoning_tokens": 0}}
}

func responsesStatus(message object) (string, any, error) {
	switch text(message["stop_reason"]) {
	case "end_turn", "stop_sequence", "tool_use", "refusal":
		return "completed", nil, nil
	case "max_tokens":
		return "incomplete", object{"reason": "max_output_tokens"}, nil
	case "pause_turn":
		// Compatibility policy: return the available output without auto-resuming.
		return "completed", nil, nil
	default:
		return "", nil, fmt.Errorf("Anthropic reply has invalid or missing stop_reason %q", text(message["stop_reason"]))
	}
}

func responsesTextPart(block object, refusal bool) object {
	value := text(block["text"])
	if text(block["type"]) == "refusal" {
		value = text(block["refusal"])
		refusal = true
	}
	if refusal {
		return object{"type": "refusal", "refusal": value}
	}
	annotations := []any{}
	var sources responseSources
	for _, v := range array(block["citations"]) {
		c := obj(v)
		if text(c["type"]) == "web_search_result_location" {
			sources.add(object{"type": "url_citation", "url": c["url"], "title": c["title"]})
		}
	}
	for _, source := range sources {
		// Anthropic attaches a citation to the whole block, not a character span.
		// Approximate that range in the UTF-16 units used by JavaScript clients.
		annotations = append(annotations, object{"type": "url_citation", "url": source["url"], "title": source["title"],
			"start_index": 0, "end_index": len(utf16.Encode([]rune(value)))})
	}
	return object{"type": "output_text", "text": value, "annotations": annotations}
}

func responsesBlockItem(block object, responseID string, index int, bridge *responsesBridge, refusal bool) (object, error) {
	switch kind := text(block["type"]); kind {
	case "text", "refusal":
		return object{"type": "message", "id": fmt.Sprintf("%s_msg_%d", responseID, index), "role": "assistant", "status": "completed",
			"content": []any{responsesTextPart(block, refusal)}}, nil
	case "tool_use":
		id, name := text(block["id"]), text(block["name"])
		if id == "" || name == "" {
			return nil, errors.New("Anthropic tool call is missing its ID or name")
		}
		input := obj(block["input"])
		if input == nil {
			return nil, errors.New("Anthropic tool call input is not an object")
		}
		if bridge != nil && bridge.custom[name] {
			value, ok := input["input"].(string)
			if !ok || len(input) != 1 {
				return nil, errors.New("Anthropic custom tool input must contain only a string input")
			}
			return object{"type": "custom_tool_call", "id": "ctc_" + id, "call_id": id, "name": name, "input": value, "status": "completed"}, nil
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		return object{"type": "function_call", "id": "fc_" + id, "call_id": id, "name": name, "arguments": string(encoded), "status": "completed"}, nil
	case "server_tool_use":
		if text(block["name"]) != "web_search" {
			return nil, fmt.Errorf("Anthropic server tool %q cannot be converted", text(block["name"]))
		}
		if text(block["id"]) == "" {
			return nil, errors.New("Anthropic search call is missing its ID")
		}
		query, ok := obj(block["input"])["query"].(string)
		if !ok {
			return nil, errors.New("Anthropic search call is missing its query")
		}
		return object{"type": "web_search_call", "id": block["id"], "status": "completed", "action": object{"type": "search", "query": query}}, nil
	case "thinking", "redacted_thinking", "web_search_tool_result":
		return nil, nil
	default:
		return nil, fmt.Errorf("Anthropic content block %q cannot be converted", kind)
	}
}

func responsesSearchFailed(block object) bool {
	result := obj(block["content"])
	return text(result["type"]) == "web_search_tool_result_error" || result["is_error"] == true || block["is_error"] == true
}

func messagesToResponse(message object, model string, bridge *responsesBridge) (object, error) {
	if text(message["type"]) != "message" || text(message["role"]) != "assistant" || array(message["content"]) == nil {
		return nil, errors.New("Provider returned an invalid Anthropic message")
	}
	status, details, err := responsesStatus(message)
	if err != nil {
		return nil, err
	}
	id := responsesID(message)
	output := []any{}
	searches := map[string]object{}
	pendingSearches := map[string]bool{}
	if bridge != nil {
		for id := range bridge.pendingSearches {
			pendingSearches[id] = true
		}
	}
	for i, v := range array(message["content"]) {
		block := obj(v)
		if err := updatePendingSearches(pendingSearches, block); err != nil {
			return nil, err
		}
		item, err := responsesBlockItem(block, id, i, bridge, text(message["stop_reason"]) == "refusal")
		if err != nil {
			return nil, err
		}
		if item != nil {
			if status == "incomplete" && text(item["type"]) == "message" {
				item["status"] = "incomplete"
			}
			output = append(output, item)
			if text(item["type"]) == "web_search_call" {
				item["status"] = "in_progress"
				searches[text(block["id"])] = item
			}
		}
		if text(block["type"]) == "web_search_tool_result" {
			search := searches[text(block["tool_use_id"])]
			// A result for a historical call belongs in replay state, not a new
			// visible search call. Only finish items created in this response.
			if search != nil {
				search["status"] = "completed"
				if responsesSearchFailed(block) {
					search["status"] = "failed"
				}
			}
		}
	}
	// completed is also used for tool_use and pause_turn. Only a genuine end
	// of the Anthropic turn requires every server search to have its result.
	if reason := text(message["stop_reason"]); (reason == "end_turn" || reason == "stop_sequence") && len(pendingSearches) > 0 {
		return nil, errors.New("Anthropic completed a turn with an unfinished search")
	}
	// Replay must follow all visible items. The request translator uses those
	// items as a match key and restores the original ordered content wholesale.
	replay, err := anthropicReplay(array(message["content"]), output)
	if err != nil {
		return nil, err
	}
	output = append(output, object{"type": "reasoning", "id": id + "_replay", "summary": []any{}, "encrypted_content": replay})
	return object{"id": id, "object": "response", "created_at": 0, "model": model, "status": status,
		"output": output, "error": nil, "incomplete_details": details, "usage": responsesUsage(message)}, nil
}

func writeResponsesError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(object{"error": object{"type": "api_error", "message": message, "param": nil, "code": nil}})
}

func writeResponsesResponse(w http.ResponseWriter, response *http.Response, model string, stream bool, bridge *responsesBridge) {
	w.Header().Set("Cache-Control", "no-store")
	if response.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, MaxBody))
		body, _ := decodeObject(data)
		message := text(obj(body["error"])["message"])
		if message == "" {
			message = fmt.Sprintf("Upstream Anthropic request failed (%d)", response.StatusCode)
		}
		writeResponsesError(w, response.StatusCode, message)
		return
	}
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		s := newResponsesStream(w, model, bridge, stream)
		err := readResponseEvents(response.Body, s.event)
		if err != nil && !errors.Is(err, errResponseFinished) {
			s.fail(err)
			return
		}
		if !s.finished {
			s.fail(errors.New("Anthropic stream ended before message_stop"))
		}
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxBody+1))
	if err != nil || len(data) > MaxBody {
		writeResponsesError(w, 502, "Could not read Anthropic reply")
		return
	}
	message, err := decodeObject(data)
	if err != nil {
		writeResponsesError(w, 502, "Provider returned invalid Anthropic JSON")
		return
	}
	converted, err := messagesToResponse(message, model, bridge)
	if err != nil {
		writeResponsesError(w, 502, err.Error())
		return
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(converted)
		return
	}
	s := newResponsesStream(w, model, bridge, true)
	if err := s.start(object{"id": strings.TrimPrefix(text(converted["id"]), "resp_")}); err != nil {
		s.fail(err)
		return
	}
	if err := s.complete(converted); err != nil {
		s.fail(err)
	}
}

type responsesContentBlock struct {
	source        object
	outputIndex   int
	item          object
	value         strings.Builder
	arguments     strings.Builder
	stopped, done bool
}

type responsesStream struct {
	w                 http.ResponseWriter
	model             string
	bridge            *responsesBridge
	streaming         bool
	started, finished bool
	sequence          int
	message           object
	id                string
	blocks            []*responsesContentBlock
	output            []any
	bytes             int
}

func newResponsesStream(w http.ResponseWriter, model string, bridge *responsesBridge, streaming bool) *responsesStream {
	return &responsesStream{w: w, model: model, bridge: bridge, streaming: streaming, output: []any{}}
}

func (s *responsesStream) send(kind string, value object) error {
	if !s.streaming {
		return nil
	}
	s.w.Header().Set("Content-Type", "text/event-stream")
	value["type"], value["sequence_number"] = kind, s.sequence
	s.sequence++
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
		return err
	}
	return http.NewResponseController(s.w).Flush()
}

func (s *responsesStream) snapshot(status string) object {
	return object{"id": s.id, "object": "response", "created_at": 0, "model": s.model, "status": status,
		"output": []any{}, "error": nil, "incomplete_details": nil, "usage": nil}
}

func (s *responsesStream) start(message object) error {
	if s.started {
		return errors.New("Anthropic stream sent duplicate message_start")
	}
	s.started = true
	s.id = responsesID(message)
	// This is our own accumulator; never alter caller-owned blocks.
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	s.message, err = decodeObject(data)
	if err != nil {
		return err
	}
	if s.message == nil {
		s.message = object{}
	}
	s.message["id"] = strings.TrimPrefix(s.id, "resp_")
	if err := s.send("response.created", object{"response": s.snapshot("in_progress")}); err != nil {
		return err
	}
	return s.send("response.in_progress", object{"response": s.snapshot("in_progress")})
}

func (s *responsesStream) addItem(b *responsesContentBlock, item object) error {
	b.outputIndex, b.item = len(s.output), item
	s.output = append(s.output, item)
	return s.send("response.output_item.added", object{"output_index": b.outputIndex, "item": item})
}

func (s *responsesStream) startBlock(index int, source object) error {
	if !s.started || index != len(s.blocks) || source == nil {
		return errors.New("Anthropic stream sent invalid content_block_start")
	}
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	block, err := decodeObject(data)
	if err != nil {
		return err
	}
	b := &responsesContentBlock{source: block, outputIndex: -1}
	s.blocks = append(s.blocks, b)
	switch kind := text(block["type"]); kind {
	case "text", "refusal":
		initial := text(block["text"])
		if kind == "refusal" {
			initial = text(block["refusal"])
		}
		item, _ := responsesBlockItem(block, s.id, index, s.bridge, false)
		item["status"] = "in_progress"
		part := responsesTextPart(object{"type": kind}, false)
		item["content"] = []any{part}
		if err := s.addItem(b, item); err != nil {
			return err
		}
		if err := s.send("response.content_part.added", object{"item_id": item["id"], "output_index": b.outputIndex, "content_index": 0, "part": part}); err != nil {
			return err
		}
		return s.textDelta(b, initial)
	case "tool_use":
		// Empty start inputs are placeholders; partial_json frames are the actual
		// JSON document and must not be prefixed by the placeholder '{}'.
		if text(block["id"]) == "" || text(block["name"]) == "" {
			return errors.New("Anthropic tool call is missing its ID or name")
		}
		custom := s.bridge != nil && s.bridge.custom[text(block["name"])]
		kind, field, prefix := "function_call", "arguments", "fc_"
		if custom {
			kind, field, prefix = "custom_tool_call", "input", "ctc_"
		}
		return s.addItem(b, object{"type": kind, "id": prefix + text(block["id"]), "call_id": block["id"], "name": block["name"], field: "", "status": "in_progress"})
	case "server_tool_use":
		if text(block["name"]) != "web_search" || text(block["id"]) == "" {
			return errors.New("Anthropic stream sent unsupported server tool")
		}
		return s.addItem(b, object{"type": "web_search_call", "id": block["id"], "status": "in_progress", "action": object{"type": "search", "query": text(obj(block["input"])["query"])}})
	case "thinking", "redacted_thinking", "web_search_tool_result":
		return nil
	default:
		return fmt.Errorf("Anthropic content block %q cannot be converted", kind)
	}
}

func (s *responsesStream) textDelta(b *responsesContentBlock, value string) error {
	if value == "" {
		return nil
	}
	if b.done || b.stopped {
		return errors.New("Anthropic sent text after content_block_stop")
	}
	b.value.WriteString(value)
	kind, field := "response.output_text.delta", "delta"
	if text(b.source["type"]) == "refusal" {
		kind = "response.refusal.delta"
	}
	return s.send(kind, object{"item_id": b.item["id"], "output_index": b.outputIndex, "content_index": 0, field: value})
}

func (s *responsesStream) delta(b *responsesContentBlock, delta object) error {
	if b.stopped {
		return errors.New("Anthropic sent a delta after content_block_stop")
	}
	switch kind := text(delta["type"]); kind {
	case "text_delta":
		if text(b.source["type"]) != "text" {
			return errors.New("Anthropic text delta has no text block")
		}
		return s.textDelta(b, text(delta["text"]))
	case "refusal_delta":
		if text(b.source["type"]) != "refusal" {
			return errors.New("Anthropic refusal delta has no refusal block")
		}
		return s.textDelta(b, text(delta["refusal"]))
	case "input_json_delta":
		blockKind := text(b.source["type"])
		if blockKind != "tool_use" && blockKind != "server_tool_use" {
			return errors.New("Anthropic argument delta has no tool block")
		}
		value := text(delta["partial_json"])
		b.arguments.WriteString(value)
		if b.item != nil && text(b.item["type"]) == "function_call" {
			b.value.WriteString(value)
			return s.send("response.function_call_arguments.delta", object{"item_id": b.item["id"], "output_index": b.outputIndex, "delta": value})
		}
	case "thinking_delta", "signature_delta":
		if text(b.source["type"]) != "thinking" {
			return errors.New("Anthropic thinking delta has no thinking block")
		}
		field := "thinking"
		if kind == "signature_delta" {
			field = "signature"
		}
		b.source[field] = text(b.source[field]) + text(delta[field])
	case "citations_delta":
		if text(b.source["type"]) != "text" || obj(delta["citation"]) == nil {
			return errors.New("Anthropic citation delta has no text block")
		}
		b.source["citations"] = append(array(b.source["citations"]), delta["citation"])
	default:
		return fmt.Errorf("Unsupported Anthropic delta %q", kind)
	}
	return nil
}

func (s *responsesStream) stopBlock(b *responsesContentBlock) error {
	if b.stopped {
		return errors.New("Anthropic stream sent duplicate content_block_stop")
	}
	switch text(b.source["type"]) {
	case "text":
		b.source["text"] = b.value.String()
	case "refusal":
		b.source["refusal"] = b.value.String()
	case "tool_use", "server_tool_use":
		if b.arguments.Len() > 0 {
			input, err := decodeObject([]byte(b.arguments.String()))
			if err != nil {
				return fmt.Errorf("Anthropic returned invalid tool arguments: %w", err)
			}
			b.source["input"] = input
		}
	}
	b.stopped = true
	if text(b.source["type"]) == "tool_use" {
		item, err := responsesBlockItem(b.source, s.id, 0, s.bridge, false)
		if err != nil {
			return err
		}
		return s.finishItem(b, item)
	}
	// Text completion waits for message_delta: stop_reason can mark a refusal.
	// Searches wait for their result block, which determines success/failure.
	return nil
}

func (s *responsesStream) finishItem(b *responsesContentBlock, item object) error {
	if b.done {
		return nil
	}
	if b.item == nil {
		initial := object{}
		for key, value := range item {
			initial[key] = value
		}
		switch text(item["type"]) {
		case "message":
			part := obj(array(item["content"])[0])
			empty := responsesTextPart(object{"type": "text"}, text(part["type"]) == "refusal")
			initial["content"] = []any{empty}
			initial["status"] = "in_progress"
		case "function_call":
			initial["arguments"] = ""
			initial["status"] = "in_progress"
		case "custom_tool_call":
			initial["input"] = ""
			initial["status"] = "in_progress"
		}
		if err := s.addItem(b, initial); err != nil {
			return err
		}
		if text(item["type"]) == "message" {
			if err := s.send("response.content_part.added", object{"item_id": item["id"], "output_index": b.outputIndex, "content_index": 0, "part": array(initial["content"])[0]}); err != nil {
				return err
			}
		}
	}
	fields := object{"item_id": item["id"], "output_index": b.outputIndex}
	switch text(item["type"]) {
	case "message":
		part := obj(array(item["content"])[0])
		value, kind, field := text(part["text"]), "response.output_text", "text"
		if text(part["type"]) == "refusal" {
			value, kind, field = text(part["refusal"]), "response.refusal", "refusal"
		}
		current := b.value.String()
		if !strings.HasPrefix(value, current) {
			return errors.New("Anthropic final text differs from streamed text")
		}
		if suffix := strings.TrimPrefix(value, current); suffix != "" {
			if err := s.send(kind+".delta", object{"item_id": item["id"], "output_index": b.outputIndex, "content_index": 0, "delta": suffix}); err != nil {
				return err
			}
		}
		for i, annotation := range array(part["annotations"]) {
			if err := s.send("response.output_text.annotation.added", object{"item_id": item["id"], "output_index": b.outputIndex, "content_index": 0, "annotation_index": i, "annotation": annotation}); err != nil {
				return err
			}
		}
		fields["content_index"], fields[field] = 0, value
		if field == "text" {
			fields["annotations"] = part["annotations"]
		}
		if err := s.send(kind+".done", fields); err != nil {
			return err
		}
		if err := s.send("response.content_part.done", object{"item_id": item["id"], "output_index": b.outputIndex, "content_index": 0, "part": part}); err != nil {
			return err
		}
	case "function_call", "custom_tool_call":
		field, kind := "arguments", "response.function_call_arguments"
		if text(item["type"]) == "custom_tool_call" {
			field, kind = "input", "response.custom_tool_call_input"
		}
		full, current := text(item[field]), b.value.String()
		// JSON whitespace/key ordering can differ after parsing. Preserve the
		// original streamed JSON spelling once it has been validated.
		if field == "arguments" && current != "" {
			item[field] = current
			full = current
		}
		if !strings.HasPrefix(full, current) {
			return errors.New("Anthropic final tool input differs from streamed input")
		}
		if suffix := strings.TrimPrefix(full, current); suffix != "" {
			if err := s.send(kind+".delta", object{"item_id": item["id"], "output_index": b.outputIndex, "delta": suffix}); err != nil {
				return err
			}
		}
		fields[field] = full
		if err := s.send(kind+".done", fields); err != nil {
			return err
		}
	}
	b.done, b.item = true, item
	s.output[b.outputIndex] = item
	return s.send("response.output_item.done", object{"output_index": b.outputIndex, "item": item})
}

func (s *responsesStream) complete(response object) error {
	if !s.streaming {
		s.w.Header().Set("Content-Type", "application/json")
		s.finished = true
		return json.NewEncoder(s.w).Encode(response)
	}
	visible := array(response["output"])
	// Match by stable ID, not Anthropic block index: invisible blocks do not
	// occupy Responses output indices.
	byID := map[string]*responsesContentBlock{}
	for _, b := range s.blocks {
		if b.item != nil {
			byID[text(b.item["id"])] = b
		}
	}
	reconciled := []any{}
	for _, v := range visible {
		item := obj(v)
		b := byID[text(item["id"])]
		if b == nil {
			b = &responsesContentBlock{outputIndex: -1}
		}
		if err := s.finishItem(b, item); err != nil {
			return err
		}
		reconciled = append(reconciled, b.item)
	}
	response["output"] = reconciled
	s.finished = true
	return s.send("response."+text(response["status"]), object{"response": response})
}

func (s *responsesStream) fail(err error) {
	if s.finished {
		return
	}
	if !s.streaming {
		s.finished = true
		writeResponsesError(s.w, 502, err.Error())
		return
	}
	if !s.started {
		_ = s.start(object{})
	}
	response := s.snapshot("failed")
	response["output"] = s.output
	response["error"] = object{"code": "server_error", "message": err.Error()}
	s.finished = true
	_ = s.send("response.failed", object{"response": response})
}

func (s *responsesStream) event(e object) error {
	if s.finished {
		return errResponseFinished
	}
	// Bound the whole stream, not only text/argument deltas: encrypted search
	// results and redacted thinking arrive inside content_block_start payloads.
	encoded, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.bytes += len(encoded)
	if s.bytes > MaxBody {
		return errors.New("Anthropic stream exceeds response size limit")
	}
	switch kind := text(e["type"]); kind {
	case "ping":
		return nil
	case "error":
		message := text(obj(e["error"])["message"])
		if message == "" {
			message = "Anthropic stream failed"
		}
		return errors.New(message)
	case "message_start":
		message := obj(e["message"])
		if text(message["type"]) != "message" || text(message["role"]) != "assistant" {
			return errors.New("Invalid Anthropic message_start")
		}
		if len(array(message["content"])) != 0 {
			return errors.New("Anthropic message_start contains unexpected content")
		}
		return s.start(message)
	case "content_block_start":
		return s.startBlock(number(e["index"]), obj(e["content_block"]))
	case "content_block_delta", "content_block_stop":
		index := number(e["index"])
		if !s.started || index < 0 || index >= len(s.blocks) {
			return errors.New("Anthropic stream referenced an unknown content block")
		}
		if kind == "content_block_delta" {
			return s.delta(s.blocks[index], obj(e["delta"]))
		}
		return s.stopBlock(s.blocks[index])
	case "message_delta":
		if !s.started {
			return errors.New("Anthropic message_delta preceded message_start")
		}
		for key, value := range obj(e["delta"]) {
			s.message[key] = value
		}
		usage := obj(s.message["usage"])
		if usage == nil {
			usage = object{}
			s.message["usage"] = usage
		}
		for key, value := range obj(e["usage"]) {
			usage[key] = value
		}
	case "message_stop":
		if !s.started {
			return errors.New("Anthropic message_stop preceded message_start")
		}
		content := []any{}
		for _, b := range s.blocks {
			if !b.stopped {
				return errors.New("Anthropic message stopped with an unfinished content block")
			}
			content = append(content, b.source)
		}
		s.message["content"] = content
		message := s.message
		filtered := s.streaming && text(message["stop_reason"]) == "refusal"
		if filtered {
			// Refusal arrives after text deltas. Do not retrospectively change
			// their content type or re-emit them as refusal deltas.
			message = object{}
			for key, value := range s.message {
				message[key] = value
			}
			message["stop_reason"] = "end_turn"
		}
		response, err := messagesToResponse(message, s.model, s.bridge)
		if err != nil {
			return err
		}
		if filtered {
			response["status"] = "incomplete"
			response["incomplete_details"] = object{"reason": "content_filter"}
			for _, value := range array(response["output"]) {
				if item := obj(value); text(item["type"]) == "message" {
					item["status"] = "incomplete"
				}
			}
		}
		response["id"] = s.id
		if err := s.complete(response); err != nil {
			return err
		}
		return errResponseFinished
	default:
		return fmt.Errorf("Unsupported Anthropic stream event %q", kind)
	}
	return nil
}
