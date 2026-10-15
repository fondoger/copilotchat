// Package mcp implements the client side of MCP over stdio and Streamable HTTP.
// Calling a tool can have external effects. Callers must obtain the user's
// approval before invoking CallTool; discovery does not invoke tools.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const protocolVersion = "2025-06-18"

type Config struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Enabled   bool              `json:"enabled"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type Content struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	MIMEType string          `json:"mimeType,omitempty"`
	Data     string          `json:"data,omitempty"`
	Resource json.RawMessage `json:"resource,omitempty"`
	URI      string          `json:"uri,omitempty"`
	Name     string          `json:"name,omitempty"`
}

type ToolResult struct {
	Content           []Content      `json:"content"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

// Text returns text blocks and, when no text blocks are present, structured
// output. Image/audio bytes remain available in Content.
func (r ToolResult) Text() string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		} else if c.Type == "resource" && len(c.Resource) > 0 {
			var resource struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(c.Resource, &resource) == nil && resource.Text != "" {
				parts = append(parts, resource.Text)
			}
		}
	}
	if len(parts) == 0 && r.StructuredContent != nil {
		if b, err := json.Marshal(r.StructuredContent); err == nil {
			return string(b)
		}
	}
	return strings.Join(parts, "\n")
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Client struct {
	ServerInfo      ServerInfo
	ProtocolVersion string
	transport       transport
	closeOnce       sync.Once
	closeErr        error
}

type transport interface {
	request(context.Context, string, any) (json.RawMessage, error)
	notify(context.Context, string, any) error
	close() error
}

// Connect starts a local server or initializes a remote session. The context
// bounds initialization only; Close owns the resulting session's lifetime.
// Enabled is a UI preference and is intentionally not enforced here.
func Connect(ctx context.Context, config Config) (*Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var t transport
	var err error
	switch strings.ToLower(strings.TrimSpace(config.Transport)) {
	case "", "stdio":
		t, err = newStdioTransport(config)
	case "http", "streamable-http":
		t, err = newHTTPTransport(config)
	default:
		err = fmt.Errorf("unsupported MCP transport %q", config.Transport)
	}
	if err != nil {
		return nil, err
	}
	c := &Client{transport: t}
	result, err := t.request(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      ServerInfo{Name: "copilot-chat", Version: "0.1.0"},
	})
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize MCP server: %w", err)
	}
	var init struct {
		ProtocolVersion string     `json:"protocolVersion"`
		ServerInfo      ServerInfo `json:"serverInfo"`
	}
	if err := json.Unmarshal(result, &init); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("decode MCP initialization: %w", err)
	}
	switch init.ProtocolVersion {
	case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
	default:
		_ = c.Close()
		return nil, fmt.Errorf("unsupported MCP protocol version %q", init.ProtocolVersion)
	}
	c.ProtocolVersion, c.ServerInfo = init.ProtocolVersion, init.ServerInfo
	if h, ok := t.(*httpTransport); ok {
		h.setProtocolVersion(init.ProtocolVersion)
	}
	if err = t.notify(ctx, "notifications/initialized", nil); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("complete MCP initialization: %w", err)
	}
	return c, nil
}

// Discover connects, lists all tool pages, and releases the connection.
func Discover(ctx context.Context, config Config) ([]Tool, error) {
	c, err := Connect(ctx, config)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.ListTools(ctx)
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tools := []Tool{}
	params := map[string]any{}
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		data, err := c.transport.request(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		var result struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("decode MCP tools: %w", err)
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			return tools, nil
		}
		if seen[result.NextCursor] {
			return nil, errors.New("MCP tool pagination repeated a cursor")
		}
		seen[result.NextCursor] = true
		params["cursor"] = result.NextCursor
	}
	return nil, errors.New("MCP tool pagination exceeded 100 pages")
}

// CallTool executes an approved tool invocation. The caller's deadline is
// honored, with a two-minute upper bound. A tool's IsError is distinct from a
// transport/protocol error and should be returned to the model as tool output.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	if strings.TrimSpace(name) == "" {
		return ToolResult{}, errors.New("MCP tool name must not be empty")
	}
	if args == nil {
		args = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	data, err := c.transport.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return ToolResult{}, fmt.Errorf("call MCP tool %q: %w", name, err)
	}
	var result ToolResult
	if err := json.Unmarshal(data, &result); err != nil {
		return ToolResult{}, fmt.Errorf("decode MCP tool result: %w", err)
	}
	return result, nil
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.transport.close() })
	return c.closeErr
}
