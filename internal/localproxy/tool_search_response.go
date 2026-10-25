package localproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// writeToolSearchResponse keeps the native Responses transport: an upstream SSE
// reply stays SSE, regardless of the request's stream flag.
func writeToolSearchResponse(w http.ResponseWriter, response *http.Response, c *toolSearchContext) {
	w.Header().Set("Cache-Control", "no-store")
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if response.StatusCode >= 400 {
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(response.Body, MaxBody))
		return
	}
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(response.StatusCode)
		s := toolSearchResponseStream{w: w, context: c, ids: map[string]string{}, indexes: map[string]string{}}
		if err := s.write(response.Body); err != nil && !s.writeFailed {
			// The headers may already have been sent. Report invalid arguments or
			// malformed upstream frames as a diagnostic SSE event, not HTTP JSON.
			data, _ := json.Marshal(object{"type": "error", "code": "tool_search_response_error", "message": err.Error()})
			_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
			_ = http.NewResponseController(w).Flush()
		}
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxBody+1))
	if err != nil || len(data) > MaxBody {
		writeResponsesError(w, http.StatusBadGateway, "Could not read Responses reply")
		return
	}
	body, err := decodeObject(data)
	if err != nil {
		writeResponsesError(w, http.StatusBadGateway, "Provider returned invalid Responses JSON")
		return
	}
	if err := restoreToolSearchResponse(body, c); err != nil {
		writeResponsesError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_ = json.NewEncoder(w).Encode(body)
}

// Restore response and compact-response output arrays, including responses
// nested in event/envelope objects. Output item payloads (arguments, text, etc.)
// are opaque: they are not response envelopes themselves.
func restoreToolSearchResponse(value any, c *toolSearchContext) error {
	if c == nil {
		return nil
	}
	switch value := value.(type) {
	case map[string]any:
		for _, item := range array(value["output"]) {
			if item := obj(item); item != nil {
				if err := c.restoreOutputItem(item); err != nil {
					return err
				}
			}
		}
		for key, child := range value {
			if key != "output" {
				if err := restoreToolSearchResponse(child, c); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, child := range value {
			if err := restoreToolSearchResponse(child, c); err != nil {
				return err
			}
		}
	}
	return nil
}

type toolSearchResponseStream struct {
	w           http.ResponseWriter
	context     *toolSearchContext
	ids         map[string]string
	indexes     map[string]string
	writeFailed bool
}

func (s *toolSearchResponseStream) restoreEvent(event object) (bool, error) {
	kind := text(event["type"])
	if kind == "response.output_item.added" || kind == "response.output_item.done" {
		item := obj(event["item"])
		if item != nil && s.context != nil {
			originalID := text(item["id"])
			if err := s.context.restoreOutputItem(item); err != nil {
				return false, err
			}
			if text(item["type"]) == "tool_search_call" {
				id := text(item["id"])
				if originalID != "" {
					s.ids[originalID] = id
				}
				if index, ok := event["output_index"]; ok {
					s.indexes[fmt.Sprint(index)] = id
				}
			}
		}
	}
	id := s.ids[text(event["item_id"])]
	if id == "" {
		if index, ok := event["output_index"]; ok {
			id = s.indexes[fmt.Sprint(index)]
		}
	}
	if id != "" {
		// Search arguments are objects, not the string deltas used by function
		// calls. Codex consumes the complete object in output_item.done.
		if kind == "response.function_call_arguments.delta" || kind == "response.function_call_arguments.done" {
			return true, nil
		}
		if _, ok := event["item_id"]; ok {
			event["item_id"] = id
		}
	}
	if err := restoreToolSearchResponse(event["response"], s.context); err != nil {
		return false, err
	}
	return false, nil
}

func (s *toolSearchResponseStream) write(r io.Reader) error {
	reader := bufio.NewReader(r)
	for {
		frame, readErr := readToolSearchFrame(reader)
		if len(frame) != 0 {
			var replacement []byte
			suppressed := false
			// The shared parser owns data-line joining, JSON decoding, [DONE],
			// and final-frame handling. Keep raw framing for untouched events.
			if err := readResponseEvents(bytes.NewReader(frame), func(event object) error {
				before, err := json.Marshal(event)
				if err != nil {
					return err
				}
				suppressed, err = s.restoreEvent(event)
				if err != nil || suppressed {
					return err
				}
				after, err := json.Marshal(event)
				if err == nil && !bytes.Equal(before, after) {
					replacement = after
				}
				return err
			}); err != nil {
				return err
			}
			if !suppressed {
				if replacement != nil {
					frame = replaceToolSearchFrameData(frame, replacement)
				}
				if _, err := s.w.Write(frame); err != nil {
					s.writeFailed = true
					return err
				}
				if err := http.NewResponseController(s.w).Flush(); err != nil {
					s.writeFailed = true
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// Segment one bounded frame only, rather than buffering the stream. This lets
// readResponseEvents parse each frame without losing comments, IDs, event names,
// original JSON formatting, or [DONE] on passthrough frames.
func readToolSearchFrame(r *bufio.Reader) ([]byte, error) {
	var frame []byte
	lineStart := 0
	for {
		line, err := r.ReadSlice('\n')
		if len(frame)+len(line) > MaxBody {
			return nil, errors.New("Responses SSE event exceeds 32 MiB")
		}
		frame = append(frame, line...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return frame, err
		}
		if len(bytes.TrimRight(frame[lineStart:], "\r\n")) == 0 {
			return frame, nil
		}
		lineStart = len(frame)
	}
}

func replaceToolSearchFrameData(frame, data []byte) []byte {
	var out bytes.Buffer
	written := false
	for _, line := range bytes.SplitAfter(frame, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			out.Write(line)
			continue
		}
		if written {
			continue
		}
		written = true
		out.WriteString("data: ")
		out.Write(data)
		if bytes.HasSuffix(line, []byte("\r\n")) {
			out.WriteString("\r\n")
		} else if bytes.HasSuffix(line, []byte("\n")) {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}
