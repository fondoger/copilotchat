package microsoft

import (
	"context"
	"errors"
	"net/http"

	"github.com/fondoger/copilotchat/internal/provider"
)

// Service headers verified against Copilot CLI's Loki transport on 2026-10-19.
// Request IDs change for each call; the conversation ID survives turns/restarts.
// Keep our own client version/User-Agent instead of claiming to run its JS SDK.
func serviceHeaders(h http.Header, conversation string, agent bool) {
	h.Set("X-ClientType", "GitHub")
	h.Set("X-ClientFeature", "LlmApiProxy")
	h.Set("X-ClientScenario", "Code")
	h.Set("X-Llm-Api-Version", "2026-09-28")
	h.Set("X-ClientVersion", provider.Version)
	h.Set("X-ClientCorrelationId", provider.RequestID())
	h.Set("X-ClientCallId", provider.RequestID())
	if conversation != "" {
		h.Set("X-ConversationId", conversation)
	}
	h.Set("X-Initiator", "user")
	h.Set("X-Interaction-Type", "conversation-user")
	if agent {
		h.Set("X-Initiator", "agent")
		h.Set("X-Interaction-Type", "conversation-agent")
	}
}

func (c *Client) Forward(ctx context.Context, raw provider.RawRequest) (*http.Response, error) {
	if !provider.InferencePath(raw.Endpoint) {
		return nil, errors.New("unsupported inference path")
	}
	req, err := c.PrepareInference(ctx, provider.Request{ConversationID: raw.ConversationID, Background: raw.AgentInitiated}, provider.Model{Endpoint: raw.Endpoint}, raw.Body, raw.Vision)
	if err != nil {
		return nil, err
	}
	provider.CopyProtocolHeaders(req.Header, raw.Header)
	if raw.Endpoint == "/responses/compact" || raw.Endpoint == "/responses/input_tokens" || raw.Endpoint == "/v1/messages/count_tokens" {
		req.Header.Set("Accept", "application/json")
	}
	req.URL.RawQuery = raw.RawQuery
	return c.Do(req)
}
