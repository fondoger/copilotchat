// Package microsoft implements the Microsoft 365 Copilot model service.
// Authentication is supplied by a renewable, account-pinned Entra token source.
package microsoft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/fondoger/copilotchat/internal/provider"
)

const (
	Host    = "df.loki.delve.office.com"
	BaseURL = "https://" + Host + "/api/v1/llmapi"
)

// TokenSource returns a current access token for one pinned Entra account.
// Its owner manages renewal and secure storage. The service cannot fall back to
// another provider's credentials or use a configurable inference destination.
type TokenSource func(context.Context) (string, error)

type Client struct {
	*provider.Engine
	http        *http.Client
	tokenSource TokenSource
	account     provider.AccountInfo
	mu          sync.Mutex
	models      map[string]provider.Model
}

var _ provider.Client = (*Client)(nil)

func NewClient(source TokenSource, account provider.AccountInfo) *Client {
	account.Host = Host
	client := &Client{http: provider.NewHTTPClient(), tokenSource: source,
		account: account, models: make(map[string]provider.Model)}
	client.Engine = provider.NewEngine(client)
	return client
}

func (c *Client) Host() string { return Host }

func (c *Client) Account(ctx context.Context) (provider.AccountInfo, error) {
	if err := c.connected(ctx); err != nil {
		return provider.AccountInfo{}, err
	}
	return c.account, nil
}

func (c *Client) connected(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.tokenSource == nil {
		return errors.New("connect Microsoft 365 Copilot in Providers settings first")
	}
	return nil
}

func (c *Client) requestToken(ctx context.Context) (string, error) {
	if err := c.connected(ctx); err != nil {
		return "", err
	}
	token, err := c.tokenSource(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("Microsoft Entra returned no access token; sign in again in Providers settings")
	}
	return token, nil
}

func allowedEndpoint(endpoint string) bool {
	switch endpoint {
	case BaseURL + "/models", BaseURL + "/v1/messages", BaseURL + "/v1/messages/count_tokens", BaseURL + "/chat/completions", BaseURL + "/responses":
		return true
	default:
		return false
	}
}

func (c *Client) do(ctx context.Context, method, endpoint string, body any, token string) (*http.Response, error) {
	if !allowedEndpoint(endpoint) {
		return nil, errors.New("Microsoft 365 credentials cannot be sent outside the configured Copilot service")
	}
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
	req.Header.Set("User-Agent", provider.UserAgent)
	serviceHeaders(req.Header, "", false)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.http.Do(req)
}

// PrepareInference permits only the fixed Microsoft service and known protocol
// paths. Token renewal completes before the shared engine starts its metrics.
func (c *Client) PrepareInference(ctx context.Context, request provider.Request, model provider.Model, encoded []byte, _ bool) (*http.Request, error) {
	token, err := c.requestToken(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := BaseURL + model.Endpoint
	if !allowedEndpoint(endpoint) {
		return nil, errors.New("the Microsoft 365 model advertises an unsupported inference endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", provider.UserAgent)
	conversation := request.ConversationID
	if conversation == "" {
		conversation = provider.RequestID()
	}
	agent := request.Background || (len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role != "user")
	serviceHeaders(req.Header, conversation, agent)
	if strings.HasPrefix(model.Endpoint, "/v1/messages") {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	return req, nil
}

func (c *Client) Do(req *http.Request) (*http.Response, error) { return c.http.Do(req) }
