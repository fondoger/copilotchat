package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestMCPHelper runs only in a subprocess. This exercises actual OS pipes,
// framing, initialization, cancellation, and process cleanup without installing
// a third-party MCP server or using network access.
func TestMCPHelper(t *testing.T) {
	if os.Getenv("COPILOT_CHAT_MCP_TEST_HELPER") != "1" {
		return
	}
	cancelled := false
	scanner := bufio.NewScanner(os.Stdin)
	write := func(message rpcMessage, result any) {
		data, _ := json.Marshal(result)
		_ = json.NewEncoder(os.Stdout).Encode(rpcMessage{JSONRPC: "2.0", ID: message.ID, Result: data})
	}
	for scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			os.Exit(3)
		}
		switch message.Method {
		case "initialize":
			if os.Getenv("COPILOT_CHAT_MCP_TEST_HANG") == "1" {
				continue
			}
			write(message, map[string]any{"protocolVersion": protocolVersion, "serverInfo": ServerInfo{Name: "stdio-helper", Version: "1"}})
		case "tools/list":
			write(message, map[string]any{"tools": []Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}})
		case "tools/call":
			params := message.Params.(map[string]any)
			name := params["name"].(string)
			if name == "wait" {
				continue
			}
			if name == "exit" {
				fmt.Fprintln(os.Stderr, "helper exited deliberately")
				os.Exit(2)
			}
			text := "ok"
			if name == "echo" {
				text = params["arguments"].(map[string]any)["text"].(string)
			}
			if name == "cancelled" {
				text = fmt.Sprint(cancelled)
			}
			write(message, ToolResult{Content: []Content{{Type: "text", Text: text}}})
		case "notifications/cancelled":
			cancelled = true
		}
	}
	os.Exit(0)
}

func helperConfig(t *testing.T) Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Config{Transport: "stdio", Command: executable, Args: []string{"-test.run=^TestMCPHelper$"}, Env: map[string]string{"COPILOT_CHAT_MCP_TEST_HELPER": "1"}}
}

func TestStdioRoundTripConcurrentCallsAndCleanup(t *testing.T) {
	c, err := Connect(context.Background(), helperConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.ServerInfo.Name != "stdio-helper" {
		t.Fatalf("server info: %#v", c.ServerInfo)
	}
	tools, err := c.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools: %#v, %v", tools, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("concurrent %d: 你好", i)
			result, err := c.CallTool(context.Background(), "echo", map[string]any{"text": want})
			if err != nil || result.Text() != want {
				t.Errorf("result: %q, %v; want %q", result.Text(), err, want)
			}
		}(i)
	}
	wg.Wait()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	transport := c.transport.(*stdioTransport)
	select {
	case <-transport.exited:
	default:
		t.Fatal("server was not reaped on close")
	}
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("call after close succeeded")
	}
}

func TestStdioCancellationKeepsConnectionUsable(t *testing.T) {
	c, err := Connect(context.Background(), helperConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = c.CallTool(ctx, "wait", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		result, err := c.CallTool(context.Background(), "cancelled", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text() == "true" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not receive cancellation")
}

func TestStdioInitializationDeadlineAndUnexpectedExit(t *testing.T) {
	config := helperConfig(t)
	config.Env["COPILOT_CHAT_MCP_TEST_HANG"] = "1"
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := Connect(ctx, config)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("initialization deadline: %v", err)
	}
	c, err := Connect(context.Background(), helperConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = c.CallTool(ctx, "exit", nil)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected-exit handling: %v", err)
	}
}

func TestStructuredAndEmbeddedText(t *testing.T) {
	if got := (ToolResult{StructuredContent: map[string]any{"count": float64(3)}}).Text(); got != `{"count":3}` {
		t.Fatal(got)
	}
	if got := (ToolResult{Content: []Content{{Type: "resource", Resource: json.RawMessage(`{"uri":"file:///test","text":"resource text"}`)}}}).Text(); got != "resource text" {
		t.Fatal(got)
	}
}
