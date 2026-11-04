package doubao

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/sse"
)

func isSSE(header http.Header) bool {
	return strings.Contains(strings.ToLower(header.Get("Content-Type")), "text/event-stream")
}

func newSSEReader(reader io.Reader) *bufio.Reader {
	buffered := bufio.NewReaderSize(reader, 64<<10)
	prefix, _ := buffered.Peek(3)
	if string(prefix) == string([]byte{0xef, 0xbb, 0xbf}) {
		buffered.Discard(3)
	}
	for i := 0; i < 4096; i++ {
		prefix, _ := buffered.Peek(1)
		if len(prefix) == 0 || !strings.ContainsRune(" \t\r\n", rune(prefix[0])) {
			break
		}
		buffered.Discard(1)
	}
	return buffered
}

var endSSE = errors.New("end of Doubao events")

// The private API also completes by clean EOF (as in the reference client).
// Never turn an empty or malformed stream into a successful Engine completion.
func convertSSE(ctx context.Context, reader io.Reader, writer io.Writer, conversationID string, complete func(conversationState)) {
	state := streamState{writer: writer, markers: map[string]streamPhase{}, answerHash: sha256.New()}
	err := sse.Read(reader, maxJSON, func(event, data string) error {
		if data == "[DONE]" {
			return endSSE
		}
		obj := decodeObject([]byte(data))
		if obj == nil {
			return protocolError()
		}
		if event == "" {
			event, _ = obj["_event"].(string)
		}
		if event == "gateway-error" || event == "STREAM_ERROR" || objectError(obj) {
			return responseError(ctx, 200, []byte(data))
		}
		if event == "SSE_ACK" {
			id := ackConversationID(obj)
			if conversationID != "" && id != "" && id != conversationID {
				return protocolError()
			}
			if id != "" {
				conversationID = id
			}
		}
		return state.event(event, obj)
	})
	if errors.Is(err, endSSE) {
		err = nil
	}
	if ctx.Err() != nil {
		return
	}
	if err == nil && state.lanes[answerChannel].outputBytes == 0 {
		err = protocolError()
	}
	if err != nil {
		var detail *provider.HTTPError
		if !errors.As(err, &detail) {
			detail = protocolError().HTTPError
		}
		writeEvent(writer, map[string]any{"error": map[string]any{"message": detail.Message}})
		return
	}
	if complete != nil && conversationID != "" {
		complete(conversationState{ID: conversationID, AnswerHash: hex.EncodeToString(state.answerHash.Sum(nil))})
	}
	if ctx.Err() != nil {
		return
	}
	if writeEvent(writer, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}) == nil {
		io.WriteString(writer, "data: [DONE]\n\n")
	}
}

func writeEvent(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = io.WriteString(writer, "data: "+string(data)+"\n\n")
	return err
}

type streamPhase int

const (
	phaseInitial streamPhase = iota
	phaseThinking
	phaseAnswer
)

type streamChannel int

const (
	answerChannel streamChannel = iota
	reasoningChannel
	channelCount
)

type streamLane struct {
	outputBytes int
	compact     bool
	snapshots   map[string]string
}

type streamState struct {
	answerHash hash.Hash
	writer     io.Writer
	markers    map[string]streamPhase
	phase      streamPhase
	lanes      [channelCount]streamLane
}

func (s *streamState) channel() streamChannel {
	if s.phase == phaseThinking {
		return reasoningChannel
	}
	return answerChannel
}

func (s *streamState) lane() *streamLane { return &s.lanes[s.channel()] }

func (s *streamState) emit(text string) error {
	if text == "" {
		return nil
	}
	key := "content"
	if s.channel() == reasoningChannel {
		key = "reasoning_text"
	}
	if err := writeEvent(s.writer, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{key: text}}}}); err != nil {
		return err
	}
	s.lane().outputBytes += len(text)
	if s.channel() == answerChannel {
		_, _ = io.WriteString(s.answerHash, text)
	}
	return nil
}

func (s *streamState) blockText(text, id string, snapshot bool) error {
	lane := s.lane()
	if lane.compact {
		// Compact deltas are authoritative for this lane. Full and partial
		// notification snapshots must not be accumulated again.
		return nil
	}
	if snapshot {
		old := lane.snapshots[id]
		if strings.HasPrefix(old, text) {
			return nil
		}
		if lane.snapshots == nil {
			lane.snapshots = map[string]string{}
		}
		lane.snapshots[id] = text
		if strings.HasPrefix(text, old) {
			text = strings.TrimPrefix(text, old)
		}
	}
	return s.emit(text)
}

func (s *streamState) marker(block map[string]any) {
	id, _ := block["block_id"].(string)
	// Current streams finish the same thinking block instead of emitting a
	// second marker. Historical snapshots may still replay its unfinished form.
	if finished, ok := block["is_finish"].(bool); ok {
		s.phase = phaseThinking
		if finished {
			s.phase = phaseAnswer
		}
		if id != "" {
			s.markers[id] = max(s.markers[id], s.phase)
		}
		return
	}
	if phase, known := s.markers[id]; id != "" && known {
		s.phase = phase
		return
	}
	s.phase++
	if id != "" {
		s.markers[id] = s.phase
	}
}

func streamBlocks(obj map[string]any) []map[string]any {
	var blocks []map[string]any
	add := func(value any) {
		if items, ok := value.([]any); ok {
			for _, item := range items {
				if block, ok := item.(map[string]any); ok {
					blocks = append(blocks, block)
				}
			}
		}
	}
	patches, _ := obj["patch_op"].([]any)
	for _, patch := range patches {
		if patch, ok := patch.(map[string]any); ok {
			if value, ok := patch["patch_value"].(map[string]any); ok {
				add(value["content_block"])
			}
		}
	}
	if content, ok := obj["content"].(map[string]any); ok {
		add(content["content_block"])
	}
	add(obj["content_block"])
	return blocks
}

func (s *streamState) event(event string, obj map[string]any) error {
	// A notification can replay earlier markers around full text snapshots.
	// Use their original lanes locally without rewinding the live delta lane.
	phase := s.phase
	defer func() { s.phase = max(phase, s.phase) }()
	blocks := streamBlocks(obj)
	if text, ok := obj["text"].(string); event == "CHUNK_DELTA" && ok && text != "" {
		for _, block := range blocks {
			if number(block["block_type"]) == 10040 {
				s.marker(block)
			}
		}
		s.lane().compact = true
		return s.emit(text)
	}
	structuredText := false
	for index, block := range blocks {
		switch number(block["block_type"]) {
		case 10040:
			s.marker(block)
		case 10000:
			content, _ := block["content"].(map[string]any)
			tb, _ := content["text_block"].(map[string]any)
			text, _ := tb["text"].(string)
			structuredText = structuredText || text != ""
			id, _ := block["block_id"].(string)
			if id == "" {
				id = "index:" + strconv.Itoa(index)
			}
			lane := s.lane()
			if event == "STREAM_MSG_NOTIFY" && text != "" && !lane.compact {
				old := lane.snapshots[id]
				if old != "" && !strings.HasPrefix(text, old) && !strings.HasPrefix(old, text) {
					// Engine deltas cannot retract an already displayed replacement.
					return protocolError()
				}
			}
			if err := s.blockText(text, id, true); err != nil {
				return err
			}
		}
	}
	if structuredText {
		return nil // Prefer structured text over its legacy string copy.
	}
	patches, _ := obj["patch_op"].([]any)
	for _, patch := range patches {
		patch, _ := patch.(map[string]any)
		value, _ := patch["patch_value"].(map[string]any)
		raw, _ := value["content"].(string)
		if raw == "" {
			continue
		}
		content := decodeObject([]byte(raw))
		if content == nil {
			return protocolError()
		}
		text, _ := content["text"].(string)
		if err := s.blockText(text, "", false); err != nil {
			return err
		}
	}
	return nil
}
