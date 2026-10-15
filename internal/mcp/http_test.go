package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func replyJSON(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func TestHTTPInitializationPaginationSSEAndCleanup(t *testing.T) {
	var mu sync.Mutex
	initialized, deleted, pingAnswered := false, false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") != "session-123" {
				t.Error("DELETE missing session")
			}
			mu.Lock()
			deleted = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var message rpcMessage
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if message.Method != "initialize" {
			if r.Header.Get("Mcp-Session-Id") != "session-123" {
				t.Error("missing session ID")
			}
			if r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
				t.Error("missing negotiated protocol version")
			}
		}
		switch message.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-123")
			replyJSON(w, message.ID, map[string]any{"protocolVersion": "2025-06-18", "serverInfo": ServerInfo{Name: "test-server", Version: "1"}, "capabilities": map[string]any{"tools": map[string]any{}}})
		case "notifications/initialized":
			mu.Lock()
			initialized = true
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			mu.Lock()
			ready := initialized
			mu.Unlock()
			if !ready {
				t.Error("listed before initialization notification")
			}
			params, _ := message.Params.(map[string]any)
			if params["cursor"] == "page-2" {
				replyJSON(w, message.ID, map[string]any{"tools": []Tool{{Name: "second", InputSchema: map[string]any{"type": "object"}}}})
			} else {
				replyJSON(w, message.ID, map[string]any{"tools": []Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}, "nextCursor": "page-2"})
			}
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": keepalive\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"ping\",\"id\":\"server-ping\"}\n\n")
			w.(http.Flusher).Flush()
			payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": ToolResult{Content: []Content{{Type: "text", Text: "hello"}}, StructuredContent: map[string]any{"ok": true}}})
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
		case "":
			if string(message.ID) == `"server-ping"` && string(message.Result) == "{}" {
				mu.Lock()
				pingAnswered = true
				mu.Unlock()
			} else {
				t.Errorf("unexpected client reply: %#v", message)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected method: %s", message.Method)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	c, err := Connect(context.Background(), Config{Transport: "http", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.ServerInfo.Name != "test-server" {
		t.Fatalf("server info: %#v", c.ServerInfo)
	}
	tools, err := c.ListTools(context.Background())
	if err != nil || len(tools) != 2 || tools[1].Name != "second" {
		t.Fatalf("tools: %#v, %v", tools, err)
	}
	result, err := c.CallTool(context.Background(), "echo", map[string]any{"value": "hello"})
	if err != nil || result.Text() != "hello" || result.StructuredContent["ok"] != true {
		t.Fatalf("result: %#v, %v", result, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !deleted || !pingAnswered {
		t.Fatalf("cleanup/ping: deleted=%v ping=%v", deleted, pingAnswered)
	}
}

func TestHTTPCancellationNotifiesServer(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message rpcMessage
		_ = json.NewDecoder(r.Body).Decode(&message)
		switch message.Method {
		case "initialize":
			replyJSON(w, message.ID, map[string]any{"protocolVersion": protocolVersion})
		case "tools/call":
			<-r.Context().Done()
		case "notifications/cancelled":
			cancelled <- struct{}{}
			w.WriteHeader(202)
		default:
			w.WriteHeader(202)
		}
	}))
	defer server.Close()
	c, err := Connect(context.Background(), Config{Transport: "streamable-http", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = c.CallTool(ctx, "slow", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive cancellation")
	}
}

func TestHTTPRPCErrorAndToolErrorAreDistinct(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message rpcMessage
		_ = json.NewDecoder(r.Body).Decode(&message)
		switch message.Method {
		case "initialize":
			replyJSON(w, message.ID, map[string]any{"protocolVersion": protocolVersion})
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rpcMessage{JSONRPC: "2.0", ID: message.ID, Error: &RPCError{Code: -32601, Message: "unsupported"}})
		case "tools/call":
			replyJSON(w, message.ID, ToolResult{IsError: true, Content: []Content{{Type: "text", Text: "file not found"}}})
		default:
			w.WriteHeader(202)
		}
	}))
	defer server.Close()
	c, err := Connect(context.Background(), Config{Transport: "http", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.ListTools(context.Background())
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32601 {
		t.Fatalf("expected RPCError, got %v", err)
	}
	result, err := c.CallTool(context.Background(), "read", nil)
	if err != nil || !result.IsError || result.Text() != "file not found" {
		t.Fatalf("tool error result: %#v, %v", result, err)
	}
}

func TestRejectCrossOriginRedirect(t *testing.T) {
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := Connect(context.Background(), Config{Transport: "http", URL: server.URL})
	if err == nil || leaked {
		t.Fatalf("cross-origin redirect was followed: leaked=%v err=%v", leaked, err)
	}
}

func TestInvalidConfigurations(t *testing.T) {
	for _, config := range []Config{{Transport: "sse"}, {Transport: "stdio"}, {Transport: "http", URL: "file:///tmp/test"}, {Transport: "http", URL: "http://secret@example.com/mcp"}} {
		if c, err := Connect(context.Background(), config); err == nil {
			c.Close()
			t.Errorf("accepted invalid config %#v", config)
		}
	}
}
