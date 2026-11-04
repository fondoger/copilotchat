// Package webapi calls fixed operations in a caller-owned ChatGPT or Claude page.
package webapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/desktop/webtransport"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/webchat"
)

//go:embed transport.js
var pageScript string

type Transport struct {
	ctx    context.Context
	cancel context.CancelFunc
	kind   string
	call   func(context.Context, string, any) (any, error)
}

func NewTransport(ctx context.Context, page *mygo.Page, kind string) *Transport {
	life, cancel := context.WithCancel(ctx)
	return &Transport{ctx: life, cancel: cancel, kind: kind, call: func(ctx context.Context, op string, input any) (any, error) {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		return page.EvalContext(ctx, "("+pageScript+")("+strconv.Quote(kind)+","+strconv.Quote(op)+","+string(data)+")")
	}}
}

func (t *Transport) failure(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return webchat.Failure(t.kind, 502)
}

func (t *Transport) eval(ctx context.Context, op string, input, target any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	value, err := t.call(ctx, op, input)
	if err != nil {
		return t.failure(ctx)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return t.failure(ctx)
	}
	var status struct {
		Failure int `json:"failure"`
	}
	if json.Unmarshal(data, &status) != nil {
		return t.failure(ctx)
	}
	if status.Failure != 0 {
		return webchat.Failure(t.kind, status.Failure)
	}
	if target != nil && json.Unmarshal(data, target) != nil {
		return t.failure(ctx)
	}
	return nil
}

func (t *Transport) WaitReady(ctx context.Context) error {
	ready, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state struct {
			Ready bool `json:"ready"`
		}
		err := t.eval(ready, "ready", nil, &state)
		if err == nil && state.Ready {
			return nil
		}
		var status *provider.HTTPError
		if errors.As(err, &status) && status.StatusCode != 502 {
			return err
		}
		select {
		case <-ready.Done():
			return t.failure(ctx)
		case <-t.ctx.Done():
			return t.failure(t.ctx)
		case <-ticker.C:
		}
	}
}

func (t *Transport) Info(ctx context.Context) (webchat.Info, error) {
	var info webchat.Info
	err := t.eval(ctx, "info", nil, &info)
	return info, err
}

func (t *Transport) Start(ctx context.Context, input webchat.Input) (*http.Response, error) {
	var header struct {
		Status       int    `json:"status"`
		ContentType  string `json:"contentType"`
		Conversation string `json:"conversation"`
	}
	if err := t.eval(ctx, "start", input, &header); err != nil {
		return nil, err
	}
	body := webtransport.NewBody(ctx, func(ctx context.Context) (webtransport.Chunk, error) {
		var chunk webtransport.Chunk
		err := t.eval(ctx, "read", nil, &chunk)
		return chunk, err
	}, t.failure)
	return &http.Response{StatusCode: header.Status, Header: http.Header{"Content-Type": {header.ContentType}, "X-Web-Conversation": {header.Conversation}}, Body: body}, nil
}

func (t *Transport) Title(ctx context.Context, id string) (string, error) {
	var title struct {
		Title string `json:"title"`
	}
	err := t.eval(ctx, "title", map[string]string{"id": id}, &title)
	return title.Title, err
}

func (t *Transport) Abort(ctx context.Context) error { return t.eval(ctx, "abort", nil, nil) }
func (t *Transport) Close()                          { t.cancel() }
