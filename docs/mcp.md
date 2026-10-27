# MCP servers

The MCP settings page accepts local servers over **stdio** and remote servers over **Streamable HTTP**. A connection initializes the server, negotiates the protocol version, sends `notifications/initialized`, and discovers tools through `tools/list`, including paginated results. Tool execution uses `tools/call`. The application asks for approval before a tool executes.

## Gateway HTTP inspection

Enable **Local forwarding** and **HTTP request inspection (GoVisual)** in LLM Gateway, then use **Copy MCP URL** to connect your agent. With the default port, register it in Claude Code:

```bash
claude mcp add govisual --transport http http://127.0.0.1:39321/__viz/mcp
```

The local endpoint shares the dashboard's captures across Gateway profiles. Use `get_last_error`, `list_requests`, `get_request`, `get_debug_context`, and `await_request` to inspect inbound and outbound traffic. `replay_request`, `diff_replay`, and `copy_as_curl` target the same local Gateway. Credential headers are redacted; supply deliberate header overrides for authenticated replays. Bodies may contain sensitive application data, and replaying inference requests may incur token charges. Disabling inspection disconnects access, and re-enabling starts an empty capture session.

## Local server

Configure the executable and each argument separately. The client launches the executable directly; command text is never evaluated by a shell. The server must write newline-delimited JSON-RPC on stdout and send diagnostic logs to stderr.

Example for a Node-based server on Windows:

```json
[
  {
    "id": "filesystem",
    "name": "Local files",
    "transport": "stdio",
    "command": "cmd.exe",
    "args": ["/d", "/s", "/c", "npx -y @modelcontextprotocol/server-filesystem C:\\Shared\\MCP"],
    "enabled": true
  }
]
```

Replace `C:\Shared\MCP` with an existing directory you intend to share. The explicit `cmd.exe` wrapper is needed for `.cmd` package runners on Windows. If Node and the server JavaScript file are already installed, prefer `node.exe` with the script path and server arguments as separate entries. On macOS, use the installed executable's path and pass the server's arguments separately. Configure a trusted server and restrict its allowed directories to those you intend to share.

Servers inherit the app's environment. The optional `env` object overrides individual variables. These user-configured values are saved with the server configuration; use inherited environment variables for secrets. GitHub Copilot credentials are managed separately and are not part of the conversation/settings JSON.

## Remote server

```json
[
  {
    "id": "remote",
    "name": "Remote tools",
    "transport": "http",
    "url": "https://example.com/mcp",
    "enabled": true
  }
]
```

`streamable-http` is also accepted as the transport name. Both JSON and SSE responses are supported. Sessions use `Mcp-Session-Id` and `MCP-Protocol-Version`; disconnecting sends a best-effort HTTP DELETE to release a session. Redirects to a different origin are rejected. The legacy HTTP+SSE transport, MCP OAuth, and custom authorization headers are not implemented, so use an endpoint that is accessible without a separate MCP login.

## Lifecycle and limits

- Initialization has a 20-second timeout, discovery 30 seconds, and individual tool calls 2 minutes. A caller's shorter deadline takes precedence.
- Cancelling a tool call cancels the pending request and sends a best-effort `notifications/cancelled`. This cannot undo work a server already performed.
- Closing a local connection closes pipes, terminates the server process tree, and waits for the process to exit. On Windows, the client uses `taskkill /T /F` with the child PID; Unix servers run in their own process group.
- The client bounds an individual JSON request/response or SSE event to 32 MiB and retains only the last 8 KiB of stderr for connection diagnostics.
- The client does not advertise sampling, elicitation, or roots. Server pings are answered; unsupported server requests receive JSON-RPC `-32601`.
- Server tool errors (`isError: true`) remain tool results. Protocol or transport failures are returned as errors. Expired HTTP sessions require reconnecting; calls are not automatically retried, avoiding accidental duplicate tool execution.

## Go API

```go
client, err := mcp.Connect(ctx, config)
if err != nil {
    return err
}
defer client.Close()

tools, err := client.ListTools(ctx)
// Obtain explicit user approval for a selected tool and its arguments.
result, err := client.CallTool(ctx, toolName, arguments)
text := result.Text()
```

`mcp.Discover(ctx, config)` is a convenience for connecting, listing all tools, and closing the connection. `Config.Enabled` is a UI preference: callers decide which servers should be connected. `Tool.InputSchema` is a JSON Schema object. `ToolResult.Content` retains text, image, audio, and resource blocks; `StructuredContent` retains structured results. `Text()` joins text blocks or serializes structured content when there are no text blocks.

Protocol tests use an actual local helper subprocess and in-process HTTP servers; they require no external MCP packages or credentials.
