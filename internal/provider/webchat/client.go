// Package webchat runs native chats through a caller-owned website session.
// Browser authentication and the site's own request preparation stay in that session.
package webchat

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
	_ "golang.org/x/image/webp"
)

const (
	ChatGPT            = "chatgpt-web"
	Claude             = "claude-web"
	ChatGPTRecoveryURL = "copilotchat://providers/chatgpt/open"
	ClaudeRecoveryURL  = "copilotchat://providers/claude/open"
	maxImage           = 20 << 20
	maxImages          = 10
)

type Info struct {
	Account provider.AccountInfo `json:"account"`
	Models  []provider.Model     `json:"models"`
}

type Input struct {
	Model          string                `json:"model"`
	Prompt         string                `json:"prompt"`
	Images         []provider.Attachment `json:"images,omitempty"`
	ConversationID string                `json:"conversation_id,omitempty"`
	ParentID       string                `json:"parent_id,omitempty"`
	Effort         string                `json:"effort,omitempty"`
}

type Session interface {
	Info(context.Context) (Info, error)
	Start(context.Context, Input) (*http.Response, error)
	Title(context.Context, string) (string, error)
	Close()
}

type Client struct {
	kind    string
	open    func(context.Context) (Session, error)
	mu      sync.RWMutex
	account provider.AccountInfo
}

var _ provider.Client = (*Client)(nil)

func NewClient(kind string, open func(context.Context) (Session, error)) *Client {
	return &Client{kind: kind, open: open}
}

func Host(kind string) string {
	if kind == Claude {
		return "claude.ai"
	}
	if kind == ChatGPT {
		return "chatgpt.com"
	}
	return ""
}

func Name(kind string) string {
	if kind == Claude {
		return "Claude Web"
	}
	return "ChatGPT Web"
}

func RecoveryURL(kind string) string {
	if kind == Claude {
		return ClaudeRecoveryURL
	}
	return ChatGPTRecoveryURL
}

func Failure(kind string, status int) error {
	message := Name(kind) + " returned an unsupported response"
	switch status {
	case 401:
		message = Name(kind) + " sign-in is required"
	case 403:
		message = Name(kind) + " rejected the web request; check sign-in, verification, and account access"
	case 429:
		return &provider.HTTPError{StatusCode: 429, Message: Name(kind) + " is rate limited; try again later"}
	default:
		status = 502
	}
	return &provider.HTTPError{StatusCode: status, Message: message + ". [Open " + strings.TrimSuffix(Name(kind), " Web") + "](" + RecoveryURL(kind) + ")"}
}

func (c *Client) Host() string { return Host(c.kind) }

func (c *Client) Models(ctx context.Context) ([]provider.Model, error) {
	s, err := c.open(ctx)
	if s != nil {
		defer s.Close()
	}
	if err != nil {
		return nil, c.sessionError(ctx, err)
	}
	if s == nil {
		return nil, Failure(c.kind, 401)
	}
	info, err := s.Info(ctx)
	if err != nil {
		return nil, c.sessionError(ctx, err)
	}
	if info.Account.ID == "" || len(info.Models) == 0 {
		return nil, Failure(c.kind, 401)
	}
	c.mu.Lock()
	c.account = info.Account
	c.mu.Unlock()
	return slices.Clone(info.Models), nil
}

func (c *Client) Account(ctx context.Context) (provider.AccountInfo, error) {
	c.mu.RLock()
	account := c.account
	c.mu.RUnlock()
	if account.ID == "" {
		return account, Failure(c.kind, 401)
	}
	return account, ctx.Err()
}

func (c *Client) Stream(ctx context.Context, req provider.Request, onDelta func(string)) error {
	_, err := c.StreamWithTools(ctx, req, onDelta)
	return err
}

func (c *Client) StreamWithTools(ctx context.Context, req provider.Request, onDelta func(string)) (provider.Completion, error) {
	return c.StreamWithEvents(ctx, req, func(e provider.StreamEvent) {
		if e.Kind == provider.EventText && onDelta != nil {
			onDelta(e.Delta)
		}
	})
}

func (c *Client) StreamWithEvents(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (result provider.Completion, streamErr error) {
	started := time.Now()
	defer func() {
		result.Metrics.Duration = time.Since(started)
		if streamErr != nil {
			result.ConversationState, result.ConversationTitle = nil, ""
		}
	}()
	if err := validate(req); err != nil {
		return result, err
	}
	state := resume(c.kind, req)
	input, err := prepare(req, state)
	if err != nil {
		return result, err
	}
	session, err := c.open(ctx)
	if session != nil {
		defer session.Close()
	}
	if err != nil {
		return result, c.sessionError(ctx, err)
	}
	if session == nil {
		return result, Failure(c.kind, 401)
	}
	stop := context.AfterFunc(ctx, session.Close)
	defer stop()
	info, err := session.Info(ctx)
	if err != nil {
		return result, c.sessionError(ctx, err)
	}
	c.mu.RLock()
	previousAccount := c.account.ID
	c.mu.RUnlock()
	if info.Account.ID == "" {
		return result, Failure(c.kind, 401)
	}
	if previousAccount != "" && previousAccount != info.Account.ID || req.AccountID != "" && req.AccountID != info.Account.ID || state.Scope != "" && state.Scope != info.Account.ID {
		return result, &provider.HTTPError{StatusCode: 403, Message: Name(c.kind) + " account changed; reload models and start a new conversation. [Open " + strings.TrimSuffix(Name(c.kind), " Web") + "](" + RecoveryURL(c.kind) + ")"}
	}
	index := slices.IndexFunc(info.Models, func(m provider.Model) bool { return m.ID == req.Model })
	if index < 0 {
		return result, fmt.Errorf("%s model is unavailable for this account; reload models", Name(c.kind))
	}
	model := info.Models[index]
	if len(input.Images) > 0 && !model.Vision {
		return result, errors.New("this web model does not support images")
	}
	if req.ReasoningEffort != "" && !slices.Contains(model.ThinkingEfforts, req.ReasoningEffort) {
		return result, errors.New("this web model does not support the selected thinking setting")
	}
	response, err := session.Start(ctx, input)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return result, c.sessionError(ctx, err)
	}
	if response == nil || response.Body == nil {
		return result, Failure(c.kind, 502)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, Failure(c.kind, response.StatusCode)
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		return result, Failure(c.kind, 502)
	}
	first := true
	emit := func(kind, text string) {
		if text == "" {
			return
		}
		if first {
			result.Metrics.TimeToFirstToken = time.Since(started)
			first = false
		}
		if kind == provider.EventReasoning {
			result.Reasoning += text
		} else {
			result.Content += text
		}
		if onEvent != nil {
			onEvent(provider.StreamEvent{Kind: kind, Delta: text})
		}
	}
	var remote conversationState
	if c.kind == Claude {
		remote, err = readClaude(response.Body, response.Header.Get("X-Web-Conversation"), emit)
	} else {
		remote, err = readChatGPT(response.Body, input.ConversationID, emit)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		return result, c.sessionError(ctx, err)
	}
	if result.Content == "" {
		return result, Failure(c.kind, 502)
	}
	result.StopReason = "stop"
	remote.Kind, remote.Model, remote.Scope = c.kind, req.Model, info.Account.ID
	remote.HistoryHash, remote.AnswerHash = provider.HistoryHash(req.ConversationID, req.Messages), hashText(result.Content)
	if !req.Background && req.ConversationID != "" {
		result.ConversationState, _ = json.Marshal(remote)
	}
	if req.WantConversationTitle {
		response.Body.Close()
		titleCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		result.ConversationTitle, _ = session.Title(titleCtx, remote.ID)
		cancel()
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, nil
}

func (c *Client) sessionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var safe *provider.HTTPError
	if errors.As(err, &safe) {
		return safe
	}
	return Failure(c.kind, 502)
}

type conversationState struct {
	ID          string `json:"conversation_id"`
	Parent      string `json:"parent_id"`
	Kind        string `json:"kind"`
	Model       string `json:"model"`
	Scope       string `json:"scope"`
	HistoryHash string `json:"history_hash"`
	AnswerHash  string `json:"answer_hash"`
}

var conversationIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

func validID(s string) bool { return conversationIDPattern.MatchString(s) }

func resume(kind string, req provider.Request) conversationState {
	var s conversationState
	n := len(req.Messages)
	if req.Background || req.ConversationID == "" || n < 3 || req.Messages[n-1].Role != "user" || req.Messages[n-2].Role != "assistant" || json.Unmarshal(req.ConversationState, &s) != nil {
		return conversationState{}
	}
	if !validID(s.ID) || !validID(s.Parent) || s.Kind != kind || s.Model != req.Model || s.Scope == "" || s.HistoryHash != provider.HistoryHash(req.ConversationID, req.Messages[:n-2]) || s.AnswerHash != hashText(req.Messages[n-2].Content) {
		return conversationState{}
	}
	return s
}

func hashText(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func validate(req provider.Request) error {
	if len(req.Tools) != 0 || req.NativeSearch {
		return errors.New("web API mode does not support native tools or search controls")
	}
	if req.ThinkingBudgetTokens != 0 {
		return errors.New("web API mode does not support thinking token budgets")
	}
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != "user" {
		return errors.New("web API mode requires a user message")
	}
	for _, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" && m.Role != "system" || len(m.ToolCalls) != 0 || m.ToolCallID != "" || m.Replay != nil {
			return errors.New("web API mode cannot replay tool or protocol-specific history")
		}
	}
	return nil
}

func prepare(req provider.Request, state conversationState) (Input, error) {
	input := Input{Model: req.Model, ConversationID: state.ID, ParentID: state.Parent, Effort: req.ReasoningEffort}
	messages := req.Messages
	if state.ID != "" {
		messages = messages[len(messages)-1:]
	}
	type historyMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		Images  []int  `json:"images,omitempty"`
	}
	history := make([]historyMessage, 0, len(messages))
	total := 0
	for _, m := range messages {
		h := historyMessage{Role: m.Role, Content: m.Content}
		for _, a := range m.Attachments {
			if !slices.Contains([]string{"image/png", "image/jpeg", "image/webp", "image/gif"}, a.MIME) || len(a.Data) > (maxImage+2)/3*4 {
				return input, errors.New("web API mode accepts PNG, JPEG, WebP, or GIF images up to 20 MB")
			}
			n, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(a.Data)))
			if err != nil || n == 0 || n > maxImage {
				return input, errors.New("invalid web image attachment")
			}
			config, format, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(a.Data)))
			mime := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}[format]
			if err != nil || mime != a.MIME || config.Width <= 0 || config.Height <= 0 || config.Width > 40000000/config.Height {
				return input, errors.New("invalid web image type or dimensions")
			}
			total += int(n)
			if len(input.Images) >= maxImages || total > 40<<20 {
				return input, errors.New("too many web images or more than 40 MB in one request")
			}
			h.Images = append(h.Images, len(input.Images)+1)
			input.Images = append(input.Images, a)
		}
		history = append(history, h)
	}
	if len(history) == 1 && history[0].Role == "user" {
		input.Prompt = history[0].Content
	} else {
		data, err := json.Marshal(history)
		if err != nil {
			return input, err
		}
		input.Prompt = string(data)
	}
	if strings.TrimSpace(input.Prompt) == "" && len(input.Images) == 0 {
		return input, errors.New("web message is empty")
	}
	return input, nil
}
