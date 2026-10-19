package github

import (
	"bytes"
	"context"
	"net/http"

	"github.com/fondoger/copilotchat/internal/provider"
)

// PrepareInference authorizes only this GitHub account's service request.
func (c *Client) PrepareInference(ctx context.Context, request provider.Request, model provider.Model, encoded []byte, vision bool) (*http.Request, error) {
	token, base := c.credentials()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+model.Endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", provider.UserAgent)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("Openai-Intent", "conversation-edits")
	req.Header.Set("X-Interaction-Type", "conversation-agent")
	if request.Background {
		req.Header.Set("X-Interaction-Type", "conversation-background")
	}
	if request.ConversationID != "" {
		req.Header.Set("X-Interaction-Id", request.ConversationID)
		req.Header.Set("X-Session-Id", request.ConversationID)
		req.Header.Set("x-session-affinity", request.ConversationID)
	}
	initiator := "user"
	if request.Background || (len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role != "user") {
		initiator = "agent"
	}
	req.Header.Set("x-initiator", initiator)
	if vision {
		req.Header.Set("Copilot-Vision-Request", "true")
	}
	if model.Endpoint == "/v1/messages" {
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
	}
	return req, nil
}

func (c *Client) Do(req *http.Request) (*http.Response, error) { return c.http.Do(req) }
