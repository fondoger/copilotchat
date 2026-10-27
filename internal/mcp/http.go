package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type httpTransport struct {
	url      string
	client   *http.Client
	sequence atomic.Uint64
	mu       sync.Mutex
	session  string
	protocol string
	closed   bool
	lifetime context.Context
	cancel   context.CancelFunc
}

func newHTTPTransport(config Config) (*httpTransport, error) {
	u, err := url.Parse(config.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return nil, errors.New("MCP HTTP URL must be an http(s) URL without embedded credentials or a fragment")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &httpTransport{url: u.String(), lifetime: ctx, cancel: cancel}
	t.client = &http.Client{
		Transport: http.DefaultTransport.(*http.Transport).Clone(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Session headers must never be forwarded to another origin.
			if req.URL.Scheme != u.Scheme || req.URL.Host != u.Host {
				return errors.New("MCP HTTP redirect changed origin")
			}
			if len(via) >= 5 {
				return errors.New("too many MCP HTTP redirects")
			}
			return nil
		},
	}
	return t, nil
}

func (t *httpTransport) setProtocolVersion(version string) {
	t.mu.Lock()
	t.protocol = version
	t.mu.Unlock()
}

func (t *httpTransport) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := strconv.FormatUint(t.sequence.Add(1), 10)
	message, err := t.post(ctx, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: params}, true)
	if err != nil {
		if ctx.Err() != nil && method != "initialize" {
			go func() {
				cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = t.notify(cancelCtx, "notifications/cancelled", map[string]any{"requestId": json.RawMessage(id), "reason": "client request cancelled"})
			}()
		}
		return nil, err
	}
	return responseResult(message)
}

func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	_, err := t.post(ctx, rpcMessage{JSONRPC: "2.0", Method: method, Params: params}, false)
	return err
}

func (t *httpTransport) post(ctx context.Context, message rpcMessage, wantResponse bool) (rpcMessage, error) {
	data, err := json.Marshal(message)
	if err != nil {
		return rpcMessage{}, err
	}
	if len(data) > maxMessageBytes {
		return rpcMessage{}, errors.New("MCP request exceeds 32 MiB")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.lifetime, cancel)
	defer stop()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(data))
	if err != nil {
		return rpcMessage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return rpcMessage{}, errors.New("MCP connection closed")
	}
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	if t.protocol != "" {
		req.Header.Set("MCP-Protocol-Version", t.protocol)
	}
	t.mu.Unlock()
	response, err := t.client.Do(req)
	if err != nil {
		return rpcMessage{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Response bodies can contain server secrets or HTML. Status alone is
		// sufficient here; do not expose arbitrary error pages in the UI.
		if response.StatusCode == http.StatusNotFound {
			return rpcMessage{}, errors.New("MCP HTTP endpoint or session was not found; reconnect the server")
		}
		return rpcMessage{}, fmt.Errorf("MCP HTTP returned %s", response.Status)
	}
	if message.Method == "initialize" {
		if session := response.Header.Get("Mcp-Session-Id"); session != "" {
			for _, c := range session {
				if c < 0x21 || c > 0x7e {
					return rpcMessage{}, errors.New("MCP server returned an invalid session ID")
				}
			}
			t.mu.Lock()
			t.session = session
			t.mu.Unlock()
		}
	}
	if !wantResponse {
		// Notifications and responses normally receive 202 with no body. Do
		// not wait on an unexpected event stream for a notification.
		return rpcMessage{}, nil
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType == "text/event-stream" {
		return t.readEventStream(ctx, response.Body, message.ID)
	}
	if contentType != "application/json" {
		return rpcMessage{}, fmt.Errorf("MCP HTTP expected JSON or SSE, received %q", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMessageBytes+1))
	if err != nil {
		return rpcMessage{}, err
	}
	if len(body) > maxMessageBytes {
		return rpcMessage{}, errors.New("MCP response exceeds 32 MiB")
	}
	var result rpcMessage
	if err := json.Unmarshal(body, &result); err != nil {
		return rpcMessage{}, fmt.Errorf("decode MCP HTTP response: %w", err)
	}
	if !bytes.Equal(result.ID, message.ID) {
		return rpcMessage{}, errors.New("MCP response ID does not match request")
	}
	return result, nil
}

func (t *httpTransport) readEventStream(ctx context.Context, reader io.Reader, id json.RawMessage) (rpcMessage, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxMessageBytes)
	var data strings.Builder
	consume := func() (rpcMessage, bool, error) {
		if data.Len() == 0 {
			return rpcMessage{}, false, nil
		}
		payload := data.String()
		data.Reset()
		var message rpcMessage
		if err := json.Unmarshal([]byte(payload), &message); err != nil {
			return rpcMessage{}, false, fmt.Errorf("decode MCP event: %w", err)
		}
		if message.Method != "" {
			if len(message.ID) > 0 && string(message.ID) != "null" {
				reply := rpcMessage{JSONRPC: "2.0", ID: message.ID}
				if message.Method == "ping" {
					reply.Result = json.RawMessage(`{}`)
				} else {
					reply.Error = &RPCError{Code: -32601, Message: "Client method not supported"}
				}
				if _, err := t.post(ctx, reply, false); err != nil {
					return rpcMessage{}, false, err
				}
			}
			return rpcMessage{}, false, nil
		}
		return message, bytes.Equal(message.ID, id), nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			message, matched, err := consume()
			if err != nil || matched {
				return message, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			if data.Len()+len(value)+1 > maxMessageBytes {
				return rpcMessage{}, errors.New("MCP event exceeds 32 MiB")
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return rpcMessage{}, err
	}
	if message, matched, err := consume(); err != nil || matched {
		return message, err
	}
	return rpcMessage{}, errors.New("MCP event stream ended before a matching response")
}

func (t *httpTransport) close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	session, version := t.session, t.protocol
	t.mu.Unlock()
	t.cancel()
	defer t.client.CloseIdleConnections()
	if session == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Mcp-Session-Id", session)
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	response, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("close MCP HTTP session: %w", err)
	}
	defer response.Body.Close()
	// Session deletion is optional in Streamable HTTP. 404 also means the
	// session is already gone, so both are successful cleanup outcomes.
	if response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotFound || (response.StatusCode >= 200 && response.StatusCode < 300) {
		return nil
	}
	return fmt.Errorf("close MCP HTTP session: %s", response.Status)
}
