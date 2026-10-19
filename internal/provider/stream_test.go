package provider

import (
	"strings"
	"testing"
)

func TestSSEFramingAndLargeChunks(t *testing.T) {
	longText := strings.Repeat("x", 100000)
	input := "\ufeff: comment\r\nevent: text\r\ndata: first\r\ndata: second\r\n\r\ndata: " + longText
	var got []string
	err := readSSE(strings.NewReader(input), func(event, data string) error {
		got = append(got, event+":"+data)
		return nil
	})
	if err != nil || len(got) != 2 || got[0] != "text:first\nsecond" || got[1] != ":"+longText {
		t.Fatalf("incorrect SSE framing: events=%d, error=%v", len(got), err)
	}
}
