// Package sse reads event-stream framing without interpreting event payloads.
package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

var ErrTooLarge = errors.New("SSE event exceeds size limit")

// Read supports CRLF, comments, multiline data, a UTF-8 BOM, and EOF dispatch.
// maxBytes bounds both a single line and the joined data of one event.
func Read(reader io.Reader, maxBytes int, handle func(event, data string) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(4096, maxBytes)), maxBytes)
	var event string
	var data []string
	size, first := 0, true
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
			if len(data) > 0 {
				size++
			}
			size += len(value)
			if size > maxBytes {
				return ErrTooLarge
			}
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return ErrTooLarge
		}
		return err
	}
	return dispatch()
}
