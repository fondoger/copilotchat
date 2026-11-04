// Package doubao adapts the native chat engine to a caller-owned web session.
// It does not own cookies, browser state, or an HTTP transport.
package doubao

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/fondoger/copilotchat/internal/provider"
)

const (
	Host     = "www.doubao.com"
	ModelID  = "doubao-web"
	chatPath = "/chat/completion"
)

type OpenSession func(context.Context) (Session, error)

type Session interface {
	Do(ctx context.Context, path, contentType string, body []byte) (*http.Response, error)
	Close()
}

type Client struct {
	*provider.Engine
	open OpenSession
}

var _ provider.Client = (*Client)(nil)
var _ provider.InferenceBackend = (*Client)(nil)
var _ provider.RequestPreparer = (*Client)(nil)

func NewClient(open OpenSession) *Client {
	c := &Client{open: open}
	c.Engine = provider.NewEngine(c)
	return c
}

func Model() provider.Model {
	return provider.Model{ID: ModelID, Name: "Doubao Web", Endpoint: "/chat/completions",
		VisionKnown: true, Vision: true, PDFKnown: true, ToolsKnown: true,
		ReasoningKnown: true, Reasoning: true, ThinkingEfforts: []string{"medium", "high"},
		ThinkingLabels: map[string]string{"default": "Off", "medium": "Medium", "high": "High"}}
}

func (c *Client) Host() string { return Host }

func (c *Client) Account(ctx context.Context) (provider.AccountInfo, error) {
	return provider.AccountInfo{ID: ModelID, Login: "web", Host: Host}, ctx.Err()
}

func (c *Client) Models(ctx context.Context) ([]provider.Model, error) {
	return []provider.Model{Model()}, ctx.Err()
}

func (c *Client) LookupModel(ctx context.Context, id string) (provider.Model, error) {
	if err := ctx.Err(); err != nil {
		return provider.Model{}, err
	}
	if id != ModelID {
		return provider.Model{}, errors.New("unknown Doubao model")
	}
	return Model(), nil
}

func (c *Client) PrepareRequest(req provider.Request) (provider.Request, error) {
	if err := validateRequest(req); err != nil {
		return req, err
	}
	if req.ReasoningEffort == "off" {
		req.ReasoningEffort = ""
	}
	return req, nil
}

type inputKey struct{}

type inferenceInput struct {
	*chatInput
	historyHash string
	wantTitle   bool
}

func (c *Client) PrepareInference(ctx context.Context, req provider.Request, model provider.Model, encoded []byte, _ bool) (*http.Request, error) {
	if model.ID != ModelID || model.Endpoint != "/chat/completions" {
		return nil, protocolError()
	}
	input, err := prepareInput(encoded, req.ReasoningEffort, resumeConversation(req).ID)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, inputKey{}, &inferenceInput{chatInput: input, historyHash: historyHash(req.ConversationID, req.Messages), wantTitle: req.WantConversationTitle})
	return http.NewRequestWithContext(ctx, http.MethodPost, "https://"+Host+chatPath, nil)
}

func (c *Client) Do(req *http.Request) (_ *http.Response, err error) {
	input, ok := req.Context().Value(inputKey{}).(*inferenceInput)
	if !ok || req.Method != http.MethodPost || req.URL.String() != "https://"+Host+chatPath {
		return nil, protocolError()
	}
	ctx := req.Context()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.open == nil {
		return nil, LoginRequired()
	}
	session, openErr := c.open(ctx)
	if openErr != nil || session == nil {
		if session != nil {
			session.Close()
		}
		return nil, transportError(ctx, openErr)
	}
	life := &sessionLife{session: session, ctx: ctx}
	stop := context.AfterFunc(ctx, life.close)
	transferred := false
	defer func() {
		if !transferred {
			stop()
			life.close()
		}
	}()

	var attachments []map[string]any
	for _, image := range input.images {
		attachment, err := uploadImage(ctx, life, image)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	body, err := input.payload(attachments)
	if err != nil {
		return nil, protocolError()
	}
	raw, err := life.do(ctx, chatPath, "application/json", body)
	if err != nil {
		return nil, err
	}
	if raw.StatusCode < 200 || raw.StatusCode >= 300 || !isSSE(raw.Header) {
		data, _ := io.ReadAll(io.LimitReader(raw.Body, maxJSON+1))
		return nil, responseError(ctx, raw.StatusCode, data)
	}

	// Some upstream failures are JSON despite an SSE content type.
	reader := newSSEReader(raw.Body)
	if first, _ := reader.Peek(1); bytes.Equal(first, []byte{'{'}) || bytes.Equal(first, []byte{'['}) {
		data, _ := io.ReadAll(io.LimitReader(reader, maxJSON+1))
		return nil, responseError(ctx, raw.StatusCode, data)
	}
	pr, pw := io.Pipe()
	life.mu.Lock()
	life.pipe = pw
	closed := life.closed
	life.mu.Unlock()
	if closed {
		pr.Close()
		pw.Close()
		return nil, transportError(ctx, nil)
	}
	transferred = true
	go func() {
		defer stop()
		defer life.close()
		defer pw.Close()
		convertSSE(ctx, reader, pw, input.conversationID, func(state conversationState) {
			var title string
			if input.wantTitle {
				life.closeBody()
				title = queryConversationTitle(ctx, life, state.ID)
			}
			if ctx.Err() == nil {
				state.HistoryHash = input.historyHash
				writeEvent(pw, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"conversation_state": state, "conversation_title": title}}}})
			}
		})
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: &streamBody{PipeReader: pr, life: life, stop: stop}}, nil
}

// A session spans uploads and the raw chat body. Cancellation must also unblock
// a consumer that stopped reading the converted pipe.
type sessionLife struct {
	session Session
	ctx     context.Context
	mu      sync.Mutex
	body    io.ReadCloser
	pipe    *io.PipeWriter
	closed  bool
}

func (l *sessionLife) do(ctx context.Context, path, contentType string, body []byte) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := l.session.Do(ctx, path, contentType, body)
	if resp != nil && resp.Body != nil {
		l.mu.Lock()
		closed := l.closed
		if !closed {
			l.body = resp.Body
		}
		l.mu.Unlock()
		if closed {
			resp.Body.Close()
			return nil, transportError(l.ctx, err)
		}
	}
	if err != nil || resp == nil || resp.Body == nil {
		return nil, transportError(l.ctx, err)
	}
	return resp, nil
}

func (l *sessionLife) closeBody() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.body != nil {
		// Serialize with cancellation: the session must not close while an
		// upload response is still finishing its Body.Close.
		l.body.Close()
		l.body = nil
	}
}

func (l *sessionLife) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	body, pipe := l.body, l.pipe
	l.body = nil
	l.mu.Unlock()
	if body != nil {
		body.Close()
	}
	if pipe != nil {
		pipe.CloseWithError(l.ctx.Err())
	}
	l.session.Close()
}

type streamBody struct {
	*io.PipeReader
	life *sessionLife
	stop func() bool
}

func (b *streamBody) Close() error {
	b.stop()
	err := b.PipeReader.Close()
	b.life.close()
	return err
}
