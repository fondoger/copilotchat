package mcp

import (
	"encoding/json"
	"fmt"
)

const maxMessageBytes = 32 << 20

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is an error response returned by the MCP server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message) }

func responseResult(message rpcMessage) (json.RawMessage, error) {
	if message.JSONRPC != "2.0" {
		return nil, fmt.Errorf("invalid MCP JSON-RPC version %q", message.JSONRPC)
	}
	if message.Error != nil {
		return nil, message.Error
	}
	if len(message.Result) == 0 {
		return nil, fmt.Errorf("MCP response has no result")
	}
	return message.Result, nil
}
