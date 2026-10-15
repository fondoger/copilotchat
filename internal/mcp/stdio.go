package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type rpcResponse struct {
	message rpcMessage
	err     error
}

type stdioWrite struct {
	data []byte
	done chan error
}

type stdioTransport struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   *tailWriter
	sequence atomic.Uint64
	mu       sync.Mutex
	pending  map[string]chan rpcResponse
	err      error
	writes   chan stdioWrite
	done     chan struct{}
	exited   chan struct{}
	stopOnce sync.Once
}

func newStdioTransport(config Config) (*stdioTransport, error) {
	if strings.TrimSpace(config.Command) == "" {
		return nil, errors.New("MCP stdio command must not be empty")
	}
	cmd := exec.Command(config.Command, config.Args...)
	configureProcess(cmd)
	env, err := mergeEnv(config.Env)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	t := &stdioTransport{cmd: cmd, pending: map[string]chan rpcResponse{}, writes: make(chan stdioWrite), done: make(chan struct{}), exited: make(chan struct{}), stderr: &tailWriter{}}
	cmd.Stderr = t.stderr
	if t.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, fmt.Errorf("open MCP stdin: %w", err)
	}
	if t.stdout, err = cmd.StdoutPipe(); err != nil {
		_ = t.stdin.Close()
		return nil, fmt.Errorf("open MCP stdout: %w", err)
	}
	if err = cmd.Start(); err != nil {
		_ = t.stdin.Close()
		_ = t.stdout.Close()
		return nil, fmt.Errorf("start MCP server: %w", err)
	}
	go t.writeLoop()
	go func() {
		t.readLoop()
		_ = cmd.Wait()
		close(t.exited)
	}()
	return t, nil
}

func mergeEnv(overrides map[string]string) ([]string, error) {
	env := os.Environ()
	for key, value := range overrides {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("invalid MCP environment variable name or value for %q", key)
		}
		filtered := env[:0]
		for _, entry := range env {
			existing, _, _ := strings.Cut(entry, "=")
			matches := existing == key
			if runtime.GOOS == "windows" {
				matches = strings.EqualFold(existing, key)
			}
			if !matches {
				filtered = append(filtered, entry)
			}
		}
		env = append(filtered, key+"="+value)
	}
	return env, nil
}

func (t *stdioTransport) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := strconv.FormatUint(t.sequence.Add(1), 10)
	response := make(chan rpcResponse, 1)
	t.mu.Lock()
	if t.err != nil {
		err := t.err
		t.mu.Unlock()
		return nil, err
	}
	t.pending[id] = response
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.pending, id); t.mu.Unlock() }()
	if err := t.send(ctx, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case r := <-response:
		if r.err != nil {
			return nil, r.err
		}
		return responseResult(r.message)
	case <-ctx.Done():
		// Cancellation is best effort and uses a fresh, short-lived context.
		go func() {
			cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = t.notify(cancelCtx, "notifications/cancelled", map[string]any{"requestId": json.RawMessage(id), "reason": "client request cancelled"})
		}()
		return nil, ctx.Err()
	case <-t.done:
		return nil, t.terminalError()
	}
}

func (t *stdioTransport) notify(ctx context.Context, method string, params any) error {
	return t.send(ctx, rpcMessage{JSONRPC: "2.0", Method: method, Params: params})
}

func (t *stdioTransport) send(ctx context.Context, message rpcMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > maxMessageBytes {
		return errors.New("MCP request exceeds 32 MiB")
	}
	w := stdioWrite{data: append(data, '\n'), done: make(chan error, 1)}
	select {
	case t.writes <- w:
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		return t.terminalError()
	}
	select {
	case err := <-w.done:
		return err
	case <-ctx.Done():
		// A blocked pipe write cannot be cancelled independently. Release the
		// pipe/process so no write goroutine survives a cancelled request.
		t.stop(ctx.Err())
		return ctx.Err()
	case <-t.done:
		return t.terminalError()
	}
}

func (t *stdioTransport) writeLoop() {
	for {
		select {
		case w := <-t.writes:
			_, err := t.stdin.Write(w.data)
			w.done <- err
			if err != nil {
				t.stop(fmt.Errorf("write MCP request: %w", err))
				return
			}
		case <-t.done:
			return
		}
	}
}

func (t *stdioTransport) readLoop() {
	scanner := bufio.NewScanner(t.stdout)
	scanner.Buffer(make([]byte, 4096), maxMessageBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var message rpcMessage
		if err := json.Unmarshal(line, &message); err != nil {
			t.stop(fmt.Errorf("invalid MCP stdout JSON: %w (server logs belong on stderr)", err))
			return
		}
		if message.Method != "" {
			if len(message.ID) != 0 && string(message.ID) != "null" {
				// This client advertises no sampling, elicitation, or roots
				// capabilities. Reply explicitly rather than leaving the server
				// waiting for an unsupported client feature.
				go t.answerServerRequest(message)
			}
			continue
		}
		t.mu.Lock()
		pending := t.pending[string(message.ID)]
		t.mu.Unlock()
		if pending != nil {
			select {
			case pending <- rpcResponse{message: message}:
			default:
			}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	detail := t.stderr.String()
	if detail != "" {
		err = fmt.Errorf("%w; stderr: %s", err, detail)
	}
	t.stop(fmt.Errorf("MCP server disconnected: %w", err))
}

func (t *stdioTransport) answerServerRequest(message rpcMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response := rpcMessage{JSONRPC: "2.0", ID: message.ID}
	if message.Method == "ping" {
		response.Result = json.RawMessage(`{}`)
	} else {
		response.Error = &RPCError{Code: -32601, Message: "Client method not supported"}
	}
	_ = t.send(ctx, response)
}

func (t *stdioTransport) terminalError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	return errors.New("MCP connection closed")
}

func (t *stdioTransport) stop(err error) {
	t.stopOnce.Do(func() {
		t.mu.Lock()
		t.err = err
		t.mu.Unlock()
		close(t.done)
		_ = t.stdin.Close()
		_ = t.stdout.Close()
		terminateProcess(t.cmd)
	})
}

func (t *stdioTransport) close() error {
	t.stop(errors.New("MCP connection closed"))
	select {
	case <-t.exited:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("timed out waiting for MCP process to exit")
	}
}

// tailWriter bounds diagnostic storage for noisy servers.
type tailWriter struct {
	mu   sync.Mutex
	data []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if len(p) > 8192 {
		p = p[len(p)-8192:]
	}
	w.data = append(w.data, p...)
	if len(w.data) > 8192 {
		w.data = append([]byte(nil), w.data[len(w.data)-8192:]...)
	}
	return n, nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(string(w.data))
}
