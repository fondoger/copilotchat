package github

import (
	"context"
	"errors"
	"net/http"

	"github.com/fondoger/copilotchat/internal/provider"
)

func (c *Client) Forward(ctx context.Context, raw provider.RawRequest) (*http.Response, error) {
	if !provider.InferencePath(raw.Endpoint) {
		return nil, errors.New("unsupported inference path")
	}
	if err := c.loadAccount(ctx); err != nil {
		return nil, err
	}
	req, err := c.PrepareInference(ctx, provider.Request{ConversationID: raw.ConversationID, Background: raw.AgentInitiated}, provider.Model{Endpoint: raw.Endpoint}, raw.Body, raw.Vision)
	if err != nil {
		return nil, err
	}
	provider.CopyProtocolHeaders(req.Header, raw.Header)
	if raw.Endpoint == "/responses/compact" {
		req.Header.Set("Accept", "application/json")
	}
	req.URL.RawQuery = raw.RawQuery
	return c.Do(req)
}
