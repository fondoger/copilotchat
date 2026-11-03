package sse

import (
	"errors"
	"strings"
	"testing"
)

func TestFramingAndLargeChunks(t *testing.T) {
	longText := strings.Repeat("x", 100000)
	input := "\ufeff: comment\r\nevent: text\r\ndata: first\r\ndata: second\r\n\r\ndata: " + longText
	var got []string
	err := Read(strings.NewReader(input), 16<<20, func(event, data string) error {
		got = append(got, event+":"+data)
		return nil
	})
	if err != nil || len(got) != 2 || got[0] != "text:first\nsecond" || got[1] != ":"+longText {
		t.Fatalf("incorrect SSE framing: events=%d, error=%v", len(got), err)
	}
}

func TestEventLimitAndCallbackStop(t *testing.T) {
	for _, input := range []string{
		"data: " + strings.Repeat("x", 100),
		strings.Repeat("data: "+strings.Repeat("x", 20)+"\n", 4) + "\n",
	} {
		if err := Read(strings.NewReader(input), 64, func(string, string) error {
			t.Fatal("oversized event dispatched")
			return nil
		}); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("size error = %v", err)
		}
	}
	stop := errors.New("stop")
	calls := 0
	err := Read(strings.NewReader("data: first\n\ndata: unread\n\n"), 64, func(string, string) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("callback stop = %v, calls=%d", err, calls)
	}
}
