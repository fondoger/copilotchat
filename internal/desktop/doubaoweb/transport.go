// Package doubaoweb transports Doubao requests through a caller-owned page.
// The desktop lease owns creation, navigation policy, serialization and teardown.
package doubaoweb

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/doubao"
	"github.com/fondoger/copilotchat/internal/store"
)

//go:embed transport.js
var pageScript string

// Transport is scoped to one session. Its caller serializes Do calls and closes
// each response body before the next request; it never creates or destroys pages.
type Transport struct {
	ctx  context.Context
	page *mygo.Page
	http *http.Client
}

func NewTransport(ctx context.Context, page *mygo.Page) *Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 5 * time.Minute
	return &Transport{ctx: ctx, page: page, http: &http.Client{Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func unavailable(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return doubao.SessionUnavailable()
}

type readyResult struct {
	Ready bool `json:"ready"`
}

type signInput struct {
	Path string `json:"path"`
	Tab  string `json:"tab"`
}

type signResult struct {
	URL  string `json:"url"`
	UA   string `json:"ua"`
	CSRF string `json:"csrf"`
}

type fetchInput struct {
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	CSRF        string `json:"csrf"`
	Body        string `json:"body"`
}

type fetchResult struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
}

type readResult struct {
	Done bool   `json:"done"`
	Data string `json:"data"`
}

func (t *Transport) eval(ctx context.Context, operation string, input, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	args, err := json.Marshal(input)
	if err != nil {
		return unavailable(ctx)
	}
	value, err := t.page.EvalContext(ctx, "("+pageScript+")("+strconv.Quote(operation)+","+string(args)+")")
	if err != nil {
		return unavailable(ctx)
	}
	if target == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || json.Unmarshal(encoded, target) != nil {
		return unavailable(ctx)
	}
	return nil
}

// WaitReady bounds page startup without turning its local timeout into the
// request's deadline. A canceled caller still wins over the safe session error.
func (t *Transport) WaitReady(ctx context.Context) error {
	return waitReady(ctx, t.ctx, func(ctx context.Context) (bool, error) {
		var state readyResult
		err := t.eval(ctx, "ready", nil, &state)
		return state.Ready, err
	})
}

func waitReady(ctx, session context.Context, probe func(context.Context) (bool, error)) error {
	ready, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ok, err := probe(ready); err == nil && ok {
			return nil
		}
		select {
		case <-ready.Done():
			return unavailable(ctx)
		case <-ticker.C:
		}
	}
}

// Abort uses an independent teardown context: the session may already be canceled.
func (t *Transport) Abort(ctx context.Context) error {
	return t.eval(ctx, "abort", nil, nil)
}

func (t *Transport) CloseIdleConnections() { t.http.CloseIdleConnections() }

func requestPath(path string) bool {
	return path == "/chat/completion" || path == "/samantha/pages/upload_image" || path == "/alice/message/get_file_url"
}

func (t *Transport) Do(ctx context.Context, path, contentType string, body []byte) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !requestPath(path) {
		return nil, unavailable(ctx)
	}
	var signed signResult
	if err := t.eval(ctx, "sign", signInput{Path: path, Tab: provider.RequestID()}, &signed); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(t.ctx, http.MethodPost, signed.URL, bytes.NewReader(body))
	if err != nil {
		return nil, unavailable(ctx)
	}
	// Never accept a destination supplied by a page or a response.
	if req.URL.Scheme != "https" || req.URL.Host != store.DoubaoHost || req.URL.Path != path || req.URL.User != nil {
		return nil, unavailable(ctx)
	}
	// Chat always needs browser-bound fetch signing. Replaying its cookies and
	// frontierSign result in Go was rejected even while website chat worked.
	if path == "/chat/completion" {
		return t.browserRequest(ctx, signed.URL, contentType, signed.CSRF, body)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "https://"+store.DoubaoHost)
	req.Header.Set("Referer", store.DoubaoChatURL)
	req.Header.Set("User-Agent", signed.UA)
	req.Header.Set("agw-js-conv", "str")
	// Loading/signing and fetch can rotate cookies after the initial login check.
	jar, csrf, err := loadCookies(ctx, signed.URL)
	if err != nil {
		return nil, err
	}
	t.http.Jar = jar
	if csrf == "" {
		csrf = signed.CSRF
	}
	if csrf != "" {
		req.Header.Set("x-tt-passport-csrf-token", csrf)
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, unavailable(ctx)
	}
	for _, cookie := range resp.Cookies() {
		if value, ok := responseCookie(req.URL, cookie, time.Now()); ok {
			_ = mygo.Cookies.Set(value)
		}
	}
	if resp.StatusCode == http.StatusForbidden && !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		// Only an explicit HTTP rejection is safe to retry. Fetch hooks can supply
		// browser-bound signing state that Go cannot transfer; chat is never retried.
		_ = resp.Body.Close()
		return t.browserRequest(ctx, signed.URL, contentType, csrf, body)
	}
	return resp, nil
}

func (t *Transport) browserRequest(ctx context.Context, url, contentType, csrf string, body []byte) (*http.Response, error) {
	args := fetchInput{URL: url, ContentType: contentType, CSRF: csrf, Body: base64.StdEncoding.EncodeToString(body)}
	var header fetchResult
	if err := t.eval(ctx, "fetch", args, &header); err != nil {
		return nil, err
	}
	stream := newBrowserBody(t.ctx, func(ctx context.Context) (readResult, error) {
		var part readResult
		err := t.eval(ctx, "read", nil, &part)
		return part, err
	})
	return &http.Response{StatusCode: header.Status, Header: http.Header{"Content-Type": {header.ContentType}}, Body: stream}, nil
}

type browserBody struct {
	*io.PipeReader
	cancel context.CancelFunc
	once   sync.Once
}

func (b *browserBody) Close() error { b.once.Do(b.cancel); return b.PipeReader.Close() }

func newBrowserBody(ctx context.Context, read func(context.Context) (readResult, error)) *browserBody {
	life, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	go func() {
		defer cancel()
		// Close the writer first: a successful EOF must not race with cancellation.
		defer writer.Close()
		for {
			part, err := read(life)
			if err != nil {
				_ = writer.CloseWithError(unavailable(life))
				return
			}
			data, err := base64.StdEncoding.DecodeString(part.Data)
			if err != nil {
				_ = writer.CloseWithError(unavailable(life))
				return
			}
			if len(data) > 0 {
				if _, err = writer.Write(data); err != nil {
					return
				}
			}
			if part.Done {
				return
			}
		}
	}()
	go func() { <-life.Done(); _ = writer.CloseWithError(life.Err()) }()
	return &browserBody{PipeReader: reader, cancel: cancel}
}
