package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const maxFrameSize = 16 << 20

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type connection struct {
	cmd       *exec.Cmd
	input     io.WriteCloser
	output    io.ReadCloser
	mu        sync.Mutex
	writeMu   sync.Mutex
	next      int64
	pending   map[int64]chan message
	done      chan struct{}
	waited    chan struct{}
	err       error
	closeOnce sync.Once
}

func startConnection(cmd *exec.Cmd) (*connection, error) {
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	// Authentication diagnostics can contain account or credential data. Only
	// sanitized structured RPC errors are allowed to leave this package.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, err
	}
	c := &connection{cmd: cmd, input: input, output: output, pending: map[int64]chan message{}, done: make(chan struct{}), waited: make(chan struct{})}
	go c.read()
	go func() {
		_ = cmd.Wait()
		close(c.waited)
		c.fail(errors.New("Copilot CLI stopped before Microsoft account sign-in finished"))
	}()
	return c, nil
}

func (c *connection) call(ctx context.Context, method string, params map[string]any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.next++
	id := c.next
	response := make(chan message, 1)
	c.pending[id] = response
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.send(message{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		if err := ctx.Err(); err != nil {
			return err
		}
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		return err
	case reply := <-response:
		if reply.Error != nil {
			return fmt.Errorf("Copilot CLI: %s", safeMessage(reply.Error.Message))
		}
		if result == nil {
			return nil
		}
		if len(reply.Result) == 0 || json.Unmarshal(reply.Result, result) != nil {
			return errors.New("Copilot CLI returned an invalid Microsoft account response")
		}
		return nil
	}
}

func (c *connection) read() {
	reader := bufio.NewReader(c.output)
	for {
		data, err := readFrame(reader)
		if err != nil {
			c.fail(errors.New("Copilot CLI Microsoft account connection closed"))
			return
		}
		var reply message
		if json.Unmarshal(data, &reply) != nil || reply.JSONRPC != "2.0" {
			c.fail(errors.New("Copilot CLI returned an unsupported Microsoft account protocol"))
			return
		}
		if reply.Method != "" {
			if len(reply.ID) != 0 {
				// The auth-only session has no tools or host capabilities. Reject
				// all callbacks instead of allowing ambient filesystem actions.
				_ = c.send(message{JSONRPC: "2.0", ID: reply.ID, Error: &rpcError{Code: -32601, Message: "This authentication client does not support host actions"}})
			}
			continue
		}
		var id int64
		if json.Unmarshal(reply.ID, &id) != nil {
			continue
		}
		c.mu.Lock()
		waiting := c.pending[id]
		c.mu.Unlock()
		if waiting != nil {
			select {
			case waiting <- reply:
			default:
			}
		}
	}
}

func (c *connection) send(value message) error {
	data, err := json.Marshal(value)
	if err != nil {
		return errors.New("could not encode Microsoft account request")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err = fmt.Fprintf(c.input, "Content-Length: %d\r\n\r\n", len(data)); err == nil {
		_, err = c.input.Write(data)
	}
	if err != nil {
		failure := errors.New("could not send Microsoft account request to Copilot CLI")
		c.fail(failure)
		return failure
	}
	return nil
}

func (c *connection) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
		close(c.done)
	}
}

func (c *connection) close() {
	c.closeOnce.Do(func() {
		c.input.Close()
		// EOF asks the dedicated headless runtime to shut down gracefully.
		select {
		case <-c.waited:
		case <-time.After(500 * time.Millisecond):
			_ = c.cmd.Process.Kill()
		}
		c.output.Close()
		c.fail(errors.New("Microsoft account connection closed"))
	})
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	length := -1
	headerBytes := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		headerBytes += len(line)
		if headerBytes > 8192 {
			return nil, errors.New("RPC header exceeds limit")
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errors.New("invalid RPC header")
		}
		if strings.EqualFold(key, "Content-Length") {
			if length != -1 {
				return nil, errors.New("duplicate RPC length")
			}
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 || length > maxFrameSize {
				return nil, errors.New("invalid RPC length")
			}
		}
	}
	if length < 0 {
		return nil, errors.New("missing RPC length")
	}
	data := make([]byte, length)
	_, err := io.ReadFull(reader, data)
	return data, err
}

var credentialWords = regexp.MustCompile(`(?i)(bearer\s+|(?:access_token|refresh_token|id_token|client_secret|authorization)["']?\s*[:=]\s*["']?(?:Bearer\s+)?)[^\s"',}]+`)
var opaqueCredential = regexp.MustCompile(`[A-Za-z0-9_+/=-]{80,}(?:\.[A-Za-z0-9_+/=-]+)*`)

func safeMessage(message string) string {
	message = credentialWords.ReplaceAllString(message, "$1[redacted]")
	message = opaqueCredential.ReplaceAllString(message, "[redacted]")
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	if len(message) > 600 {
		message = message[:600] + "…"
	}
	if strings.TrimSpace(message) == "" {
		return "Microsoft sign-in did not complete; try again"
	}
	return message
}
