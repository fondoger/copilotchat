package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestReadFrame(t *testing.T) {
	const body = `{"jsonrpc":"2.0","id":1,"result":{"中文":"有效"}}`
	reader := bufio.NewReader(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)))
	data, err := readFrame(reader)
	if err != nil || string(data) != body {
		t.Fatalf("got %q, %v", data, err)
	}
	for _, invalid := range []string{
		"\r\n", "Content-Length: -1\r\n\r\n", "Content-Length: 16777217\r\n\r\n", "Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}", "Content-Length: 3\r\n\r\n{}",
	} {
		if _, err := readFrame(bufio.NewReader(strings.NewReader(invalid))); err == nil {
			t.Errorf("accepted invalid frame %q", invalid)
		}
	}
}

func TestRPCRejectsHostActionsAndRoutesReply(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	c := &connection{input: local, output: local, pending: map[int64]chan message{}, done: make(chan struct{})}
	go c.read()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		var reply struct {
			OK bool `json:"ok"`
		}
		err := c.call(ctx, "session.accounts.enumerate", map[string]any{}, &reply)
		if err == nil && !reply.OK {
			err = errors.New("missing result")
		}
		result <- err
	}()
	reader := bufio.NewReader(remote)
	data, err := readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	var request message
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if _, err := fmt.Fprintf(remote, "Content-Length: %d\r\n\r\n%s", len(body), body); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"jsonrpc":"2.0","id":"host-1","method":"shell.run","params":{"command":"must not run"}}`)
	data, err = readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	var denied message
	if err := json.Unmarshal(data, &denied); err != nil || denied.Error == nil || denied.Error.Code != -32601 {
		t.Fatalf("host action not rejected: %s", data)
	}
	write(`{"jsonrpc":"2.0","method":"session.event","params":{}}`)
	write(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{"ok":true}}`)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestSafeMessageDoesNotExposeCredentialFields(t *testing.T) {
	for _, raw := range []string{
		"AADSTS53003 Authorization: Bearer secret-token-value",
		`{"access_token":"secret-token-value","refresh_token":"another-secret"}`,
		"token " + strings.Repeat("x", 100) + "." + strings.Repeat("y", 90) + "." + strings.Repeat("z", 90),
	} {
		got := safeMessage(raw)
		if strings.Contains(got, "secret-token-value") || strings.Contains(got, "another-secret") || strings.Contains(got, strings.Repeat("x", 100)) {
			t.Fatalf("credential was not redacted: %q", got)
		}
	}
	if got := safeMessage("AADSTS53003: blocked by your organization"); !strings.Contains(got, "AADSTS53003") {
		t.Fatal("lost actionable policy error")
	}
}
