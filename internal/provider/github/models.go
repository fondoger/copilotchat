package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

func (c *Client) Models(ctx context.Context) ([]provider.Model, error) {
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
	var catalog provider.Catalog
	if err := provider.ReadJSON(resp, &catalog); err != nil {
		return nil, err
	}
	models := make([]provider.Model, 0, len(catalog.Data))
	cache := make(map[string]provider.Model)
	for _, remote := range catalog.Data {
		if remote.ID == "" || remote.Policy.State == "disabled" || remote.ID == "gpt-5-chat-latest" {
			continue
		}
		model := remote.Model(modelEndpoint(remote.ID, remote.Endpoints))
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

// LookupModel resolves against this account, including models hidden from its picker.
func (c *Client) LookupModel(ctx context.Context, id string) (provider.Model, error) {
	if err := c.loadAccount(ctx); err != nil {
		return provider.Model{}, err
	}
	c.mu.Lock()
	model, found := c.models[id]
	c.mu.Unlock()
	if !found {
		if _, err := c.Models(ctx); err != nil {
			return provider.Model{}, err
		}
		c.mu.Lock()
		model, found = c.models[id]
		c.mu.Unlock()
		if !found {
			return provider.Model{}, fmt.Errorf("Copilot model %q is unavailable for this account; refresh the model list", id)
		}
	}
	return model, nil
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
