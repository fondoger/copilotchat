// Package copilot implements GitHub Copilot's device authorization and streaming
// inference protocols. It does not persist credentials or execute model tools.
package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	clientID       = "Ov23li8tweQw6odWQebz"
	apiVersion     = "2026-08-01"
	userAPIVersion = "2025-04-01"
	restAPIVersion = "2022-11-28"
	userAgent      = "CopilotChatNative/0.3.1"
)

type DeviceCode struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	issuedAt        time.Time
}

type Model struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Vision            bool     `json:"vision"`
	PDF               bool     `json:"pdf"`
	Endpoint          string   `json:"endpoint"`
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

type Client struct {
	http          *http.Client
	githubURL     string
	githubAPIURL  string
	host          string
	mu            sync.Mutex
	token         string
	baseURL       string
	accountLoaded bool
	models        map[string]Model
	sleep         func(context.Context, time.Duration) error
}

func NewClient(token string) *Client {
	client, _ := NewClientForHost(token, "github.com")
	return client
}

// NormalizeHost accepts a deployment hostname, never a URL or arbitrary server.
func NormalizeHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "github.com" {
		return host, nil
	}
	if len(host) > 253 || !strings.HasSuffix(host, ".ghe.com") {
		return "", errors.New("enter github.com or a GitHub Enterprise domain ending in .ghe.com")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid GitHub deployment hostname")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("use a hostname without a scheme, path, port, or credentials")
			}
		}
	}
	return host, nil
}

func fallbackBaseURL(host string) string {
	if host == "github.com" {
		return "https://api.githubcopilot.com"
	}
	return "https://copilot-api." + host
}

func NewClientForHost(token, host string) (*Client, error) {
	host, err := NormalizeHost(host)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	apiURL := "https://api." + host
	return &Client{
		http: &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Never forward a credential-bearing request to a redirect destination.
			return http.ErrUseLastResponse
		}},
		githubURL: "https://" + host, githubAPIURL: apiURL, host: host,
		token: token, baseURL: fallbackBaseURL(host), models: make(map[string]Model), sleep: sleepContext,
	}, nil
}

func (c *Client) Host() string { return c.host }

// Account identifies this client's signed-in user; no other deployment is queried.
func (c *Client) Account(ctx context.Context) (AccountInfo, error) {
	token, _ := c.credentials()
	if token == "" {
		return AccountInfo{}, errors.New("connect GitHub Copilot first")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, c.githubAPIURL+"/user", nil, token, restAPIVersion)
	if err != nil {
		return AccountInfo{}, err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := readJSON(resp, &user); err != nil {
		return AccountInfo{}, err
	}
	if user.Login == "" {
		return AccountInfo{}, errors.New("GitHub returned an account without a login name")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != token {
		return AccountInfo{}, errors.New("the active GitHub account changed; retry the request")
	}
	return AccountInfo{Login: user.Login, Host: c.host}, nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) credentials() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, c.baseURL
}

func (c *Client) do(ctx context.Context, method, endpoint string, body any, token, version string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if version != "" {
		req.Header.Set("X-GitHub-Api-Version", version)
	}
	return c.http.Do(req)
}

func readJSON(resp *http.Response, target any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(target); err != nil {
		return fmt.Errorf("invalid Copilot response: %w", err)
	}
	return nil
}

func responseError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	message := strings.TrimSpace(string(data))
	var detail struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &detail) == nil {
		if detail.Message != "" {
			message = detail.Message
		} else if len(detail.Error) != 0 {
			var nested struct {
				Message string `json:"message"`
			}
			var plain string
			if json.Unmarshal(detail.Error, &nested) == nil && nested.Message != "" {
				message = nested.Message
			} else if json.Unmarshal(detail.Error, &plain) == nil {
				message = plain
			}
		}
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	return &HTTPError{StatusCode: resp.StatusCode, Message: message}
}

// loadAccount uses the account's regional Copilot endpoint when GitHub reports
// one. A temporary entitlement lookup failure does not turn into a login denial.
func (c *Client) loadAccount(ctx context.Context) error {
	c.mu.Lock()
	loaded, token := c.accountLoaded, c.token
	c.mu.Unlock()
	if token == "" {
		return errors.New("connect GitHub Copilot in Providers settings first")
	}
	if loaded {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.do(lookupCtx, http.MethodGet, c.githubAPIURL+"/copilot_internal/user", nil, token, userAPIVersion)
	if ctx.Err() != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return ctx.Err()
	}
	var user struct {
		ChatEnabled *bool `json:"chat_enabled"`
		CanSignup   bool  `json:"can_signup_for_limited"`
		Endpoints   struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err == nil {
		err = readJSON(resp, &user)
	}
	if err == nil && user.ChatEnabled != nil && !*user.ChatEnabled {
		if user.CanSignup {
			return errors.New("this GitHub account needs to sign up for Copilot Free at https://github.com/features/copilot/plans")
		}
		return errors.New("this GitHub account needs an active Copilot subscription or an organization-assigned Copilot seat")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != token {
		return errors.New("the active GitHub account changed; retry the request")
	}
	if err == nil && user.Endpoints.API != "" {
		endpoint, parseErr := url.Parse(user.Endpoints.API)
		if parseErr == nil && c.allowedAPIEndpoint(endpoint) {
			c.baseURL = strings.TrimRight(user.Endpoints.API, "/")
		}
	}
	c.accountLoaded = true
	return nil
}

func (c *Client) allowedAPIEndpoint(endpoint *url.URL) bool {
	if endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Port() != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return false
	}
	host := strings.ToLower(endpoint.Hostname())
	if c.host == "github.com" {
		return host == "githubcopilot.com" || strings.HasSuffix(host, ".githubcopilot.com")
	}
	return strings.HasSuffix(host, "."+c.host)
}

func (c *Client) Models(ctx context.Context) ([]Model, error) {
	if err := c.loadAccount(ctx); err != nil {
		return nil, err
	}
	token, base := c.credentials()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, base+"/models", nil, token, apiVersion)
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Data []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Picker    *bool    `json:"model_picker_enabled"`
			Endpoints []string `json:"supported_endpoints"`
			Policy    struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities struct {
				Supports struct {
					Vision    *bool    `json:"vision"`
					Tools     *bool    `json:"tool_calls"`
					Reasoning *bool    `json:"reasoning"`
					Efforts   []string `json:"reasoning_effort"`
					Adaptive  *bool    `json:"adaptive_thinking"`
					MinBudget *int     `json:"min_thinking_budget"`
					MaxBudget *int     `json:"max_thinking_budget"`
				} `json:"supports"`
				Limits struct {
					MaxOutput int `json:"max_output_tokens"`
					Vision    struct {
						Types []string `json:"supported_media_types"`
					} `json:"vision"`
				} `json:"limits"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := readJSON(resp, &catalog); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(catalog.Data))
	cache := make(map[string]Model)
	for _, remote := range catalog.Data {
		if remote.ID == "" || remote.Policy.State == "disabled" || remote.ID == "gpt-5-chat-latest" {
			continue
		}
		supports := remote.Capabilities.Supports
		model := Model{ID: remote.ID, Name: remote.Name,
			Endpoint: modelEndpoint(remote.ID, remote.Endpoints), MaxOutputTokens: remote.Capabilities.Limits.MaxOutput}
		if supports.Vision != nil {
			model.Vision, model.VisionKnown = *supports.Vision, true
		}
		if supports.Tools != nil {
			model.Tools, model.ToolsKnown = *supports.Tools, true
		}
		if supports.Reasoning != nil {
			model.Reasoning, model.ReasoningKnown = *supports.Reasoning, true
		}
		if supports.Adaptive != nil {
			model.AdaptiveThinking, model.ReasoningKnown = *supports.Adaptive, true
		}
		if supports.MinBudget != nil {
			model.MinThinkingBudget = max(0, *supports.MinBudget)
		}
		if supports.MaxBudget != nil {
			model.MaxThinkingBudget, model.ReasoningKnown = max(0, *supports.MaxBudget), true
		}
		if supports.Efforts != nil {
			model.ReasoningKnown = true
		}
		model.Reasoning = model.Reasoning || model.AdaptiveThinking || model.MaxThinkingBudget > 0 || len(supports.Efforts) > 0
		// Anthropic's named effort requires its advertised adaptive-thinking route.
		if model.Endpoint != "/v1/messages" || model.AdaptiveThinking {
			seen := make(map[string]bool)
			for _, effort := range supports.Efforts {
				if effort != "" && strings.TrimSpace(effort) == effort && !seen[effort] {
					model.ThinkingEfforts = append(model.ThinkingEfforts, effort)
					seen[effort] = true
				}
			}
		}
		if model.Name == "" {
			model.Name = model.ID
		}
		if remote.Capabilities.Limits.Vision.Types != nil {
			model.PDFKnown = true
		}
		for _, mime := range remote.Capabilities.Limits.Vision.Types {
			model.Vision = model.Vision || strings.HasPrefix(mime, "image/")
			model.PDF = model.PDF || mime == "application/pdf"
			if strings.HasPrefix(mime, "image/") {
				model.VisionKnown = true
			}
		}
		cache[model.ID] = model
		if remote.Picker == nil || *remote.Picker {
			models = append(models, model)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if token != c.token {
		return nil, errors.New("the active GitHub account changed; retry the request")
	}
	c.models = cache
	return models, nil
}

func modelEndpoint(id string, advertised []string) string {
	for _, preferred := range []string{"/v1/messages", "/responses", "/chat/completions"} {
		for _, endpoint := range advertised {
			if endpoint == preferred {
				return preferred
			}
		}
	}
	if strings.HasPrefix(id, "gpt-") && !strings.HasPrefix(id, "gpt-5-mini") {
		var generation int
		if _, err := fmt.Sscanf(id, "gpt-%d", &generation); err == nil && generation >= 5 {
			return "/responses"
		}
	}
	if strings.HasPrefix(id, "grok-") || strings.HasPrefix(id, "mai-code-") {
		return "/responses"
	}
	return "/chat/completions"
}
