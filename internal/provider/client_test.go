package provider

import (
	"context"
	"errors"
	"testing"
)

type preparingBackend struct {
	searchBackend
	calls  int
	reject bool
}

func (b *preparingBackend) PrepareRequest(req Request) (Request, error) {
	b.calls++
	if b.reject {
		return req, errors.New("request rejected before encoding")
	}
	req.ReasoningEffort = ""
	return req, nil
}

func TestEnginePreparesEveryStreamEntryBeforeEncoding(t *testing.T) {
	for _, stream := range []struct {
		name string
		run  func(*Engine, Request) error
	}{
		{"text", func(e *Engine, r Request) error { return e.Stream(context.Background(), r, nil) }},
		{"tools", func(e *Engine, r Request) error {
			_, err := e.StreamWithTools(context.Background(), r, nil)
			return err
		}},
		{"events", func(e *Engine, r Request) error {
			_, err := e.StreamWithEvents(context.Background(), r, nil)
			return err
		}},
	} {
		t.Run(stream.name, func(t *testing.T) {
			backend := &preparingBackend{searchBackend: searchBackend{
				model:       Model{ID: "fixture", Endpoint: "/chat/completions", ThinkingEfforts: []string{"medium"}},
				contentType: "text/event-stream", response: "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n",
			}}
			req := Request{Model: "fixture", ReasoningEffort: "off", Messages: []Message{{Role: "user", Content: "hello"}}}
			if err := stream.run(NewEngine(backend), req); err != nil {
				t.Fatal(err)
			}
			if backend.calls != 1 || req.ReasoningEffort != "off" {
				t.Fatal("preparation repeated or modified the caller's request")
			}
			backend.reject = true
			req.Messages[0].Attachments = []Attachment{{MIME: "image/png", Data: "invalid-base64"}}
			if err := stream.run(NewEngine(backend), req); err == nil || err.Error() != "request rejected before encoding" {
				t.Fatalf("encoder ran before request preparation: %v", err)
			}
		})
	}
}
