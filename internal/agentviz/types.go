// Package agentviz observes agent traffic without changing the forwarded protocol.
// Its history is a bounded, process-local prefix tree; it never writes payloads to disk.
package agentviz

import "encoding/json"

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Usage struct {
	InputTokens    int64   `json:"inputTokens"` // Includes both cache reads and cache writes.
	OutputTokens   int64   `json:"outputTokens"`
	CachedTokens   int64   `json:"cachedTokens"`
	UncachedTokens int64   `json:"uncachedTokens"`
	TotalTokens    int64   `json:"totalTokens"`
	CacheHitRate   float64 `json:"cacheHitRate"`
}

func (u *Usage) totals() {
	u.CachedTokens = min(u.InputTokens, u.CachedTokens)
	u.UncachedTokens = u.InputTokens - u.CachedTokens
	u.TotalTokens = u.InputTokens + u.OutputTokens
	if u.InputTokens > 0 {
		u.CacheHitRate = float64(u.CachedTokens) / float64(u.InputTokens)
	}
}

func (u *Usage) add(v Usage) {
	u.InputTokens += v.InputTokens
	u.OutputTokens += v.OutputTokens
	u.CachedTokens += v.CachedTokens
	u.totals()
}

type TurnSummary struct {
	ID                 string `json:"id"`
	SessionID          string `json:"sessionId"`
	Number             int    `json:"number"`
	Attempt            int    `json:"attempt"`
	Status             string `json:"status"` // running, waiting (for tools), completed, failed
	Trigger            string `json:"trigger"`
	StartedAt          int64  `json:"startedAt"` // Unix milliseconds throughout the API.
	EndedAt            int64  `json:"endedAt,omitempty"`
	FirstChunkMS       *int64 `json:"firstChunkMs"`
	DurationMS         int64  `json:"durationMs"`
	ModelID            string `json:"modelId"`
	ModelName          string `json:"modelName"`
	ToolCount          int    `json:"toolCount"`
	RequestCount       int    `json:"requestCount"`
	Column             int    `json:"column"`
	Level              int    `json:"level"`
	Error              string `json:"error,omitempty"`
	InstructionPreview string `json:"instructionPreview"`
	CaptureNotice      string `json:"captureNotice,omitempty"`
	Usage
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Result    json.RawMessage `json:"result,omitempty"`
	IsError   bool            `json:"isError,omitempty"`
	StartedAt int64           `json:"startedAt"`
	EndedAt   int64           `json:"endedAt,omitempty"`
}

type RequestSummary struct {
	ModelID    string `json:"modelId"`
	StartedAt  int64  `json:"startedAt"`
	EndedAt    int64  `json:"endedAt"`
	Status     int    `json:"status"`
	StopReason string `json:"stopReason,omitempty"`
	Usage
}

type TurnDetail struct {
	TurnSummary
	Models      []Model           `json:"models"`
	System      json.RawMessage   `json:"system"`
	Tools       json.RawMessage   `json:"tools"`
	Instruction json.RawMessage   `json:"instruction"`
	Reply       json.RawMessage   `json:"reply"`
	Calls       []ToolCall        `json:"calls"`
	Requests    []RequestSummary  `json:"requests"`
	Injections  []json.RawMessage `json:"injections"`
}

type SessionSummary struct {
	ID         string        `json:"id"`
	Label      string        `json:"label"`
	Client     string        `json:"client"`
	GroupID    string        `json:"groupId"`
	ParentID   string        `json:"parentId,omitempty"`
	ForkTurnID string        `json:"forkTurnId,omitempty"`
	Kind       string        `json:"kind,omitempty"`
	Detached   bool          `json:"detached,omitempty"`
	LastStart  int64         `json:"lastStart"`
	Turns      []TurnSummary `json:"turns"`
}

type Column struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Gap   bool  `json:"gap"`
}

type Snapshot struct {
	Revision        uint64           `json:"revision"`
	StartedAt       int64            `json:"startedAt"`
	MemoryBytes     int64            `json:"memoryBytes"`
	DroppedSessions int              `json:"droppedSessions"`
	Columns         []Column         `json:"columns"`
	Sessions        []SessionSummary `json:"sessions"`
}
