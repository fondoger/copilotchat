// Package provider contains the shared model types and streaming protocols used
// by the GitHub and Microsoft providers. Authentication belongs to each provider.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Client is one account's model catalog and inference connection. Implementations
// keep provider-specific authentication and endpoint policies separate.
type Client interface {
	Host() string
	Account(context.Context) (AccountInfo, error)
	Models(context.Context) ([]Model, error)
	Stream(context.Context, Request, func(string)) error
	StreamWithTools(context.Context, Request, func(string)) (Completion, error)
	StreamWithEvents(context.Context, Request, func(StreamEvent)) (Completion, error)
}

type Model struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Vision            bool     `json:"vision"`
	PDF               bool     `json:"pdf"`
	Endpoint          string   `json:"endpoint"`
	Endpoints         []string `json:"endpoints,omitempty"` // All advertised protocols, for external clients.
	MaxOutputTokens   int      `json:"max_output_tokens"`
	Tools             bool     `json:"tools"`
	Reasoning         bool     `json:"reasoning"`
	VisionKnown       bool     `json:"vision_known"`
	PDFKnown          bool     `json:"pdf_known"`
	ToolsKnown        bool     `json:"tools_known"`
	ReasoningKnown    bool     `json:"reasoning_known"`
	ThinkingEfforts   []string `json:"thinking_efforts,omitempty"`
	AdaptiveThinking  bool     `json:"adaptive_thinking"`
	MinThinkingBudget int      `json:"min_thinking_budget,omitempty"`
	MaxThinkingBudget int      `json:"max_thinking_budget,omitempty"`
}

type AccountInfo struct {
	Login string `json:"login"`
	Host  string `json:"host"`
}

type Attachment struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data string `json:"data"` // Base64 file bytes, without a data URL prefix.
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // A complete JSON object once streaming finishes.
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type Message struct {
	Role            string            `json:"role"`
	Content         string            `json:"content"`
	Attachments     []Attachment      `json:"attachments,omitempty"`
	ToolCalls       []ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID      string            `json:"tool_call_id,omitempty"`
	Reasoning       string            `json:"reasoning,omitempty"`
	ReasoningOpaque string            `json:"reasoning_opaque,omitempty"`
	ProviderData    []json.RawMessage `json:"provider_data,omitempty"`
}

type Request struct {
	Model                string    `json:"model"`
	Messages             []Message `json:"messages"`
	Tools                []Tool    `json:"tools,omitempty"`
	ConversationID       string    `json:"conversation_id,omitempty"`
	Background           bool      `json:"background,omitempty"`
	ReasoningEffort      string    `json:"reasoning_effort,omitempty"`
	ThinkingBudgetTokens int       `json:"thinking_budget_tokens,omitempty"`
}

type Usage struct {
	InputTokens          int  `json:"input_tokens"`
	OutputTokens         int  `json:"output_tokens"`
	CachedInputTokens    int  `json:"cached_input_tokens"`
	Available            bool `json:"available"`
	InputAvailable       bool `json:"input_available"`
	OutputAvailable      bool `json:"output_available"`
	CachedInputAvailable bool `json:"cached_input_available"`
}

type Metrics struct {
	Duration         time.Duration `json:"duration"`
	TimeToFirstToken time.Duration `json:"time_to_first_token"`
}

const (
	EventText      = "text"
	EventReasoning = "reasoning"
)

type StreamEvent struct {
	Kind  string `json:"kind"`
	Delta string `json:"delta"`
}

type Completion struct {
	Content         string            `json:"content"`
	Reasoning       string            `json:"reasoning,omitempty"`
	ReasoningOpaque string            `json:"reasoning_opaque,omitempty"`
	ToolCalls       []ToolCall        `json:"tool_calls,omitempty"`
	ProviderData    []json.RawMessage `json:"provider_data,omitempty"`
	Usage           Usage             `json:"usage"`
	Metrics         Metrics           `json:"metrics"`
}

// AssistantMessage preserves provider reasoning signatures for subsequent turns.
func (c Completion) AssistantMessage() Message {
	return Message{Role: "assistant", Content: c.Content, ToolCalls: c.ToolCalls,
		Reasoning: c.Reasoning, ReasoningOpaque: c.ReasoningOpaque, ProviderData: c.ProviderData}
}

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Copilot request failed (%d): %s", e.StatusCode, e.Message)
}
