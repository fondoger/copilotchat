package microsoft

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
	token, err := c.requestToken(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, BaseURL+"/models", nil, token)
	if err != nil {
		return nil, err
	}
	var catalog provider.Catalog
	if err := provider.ReadJSON(resp, &catalog); err != nil {
		return nil, err
	}
	if catalog.Data == nil {
		return nil, errors.New("Microsoft 365 returned an invalid model catalog without a data array")
	}
	models := make([]provider.Model, 0, len(catalog.Data))
	cache := make(map[string]provider.Model)
	for _, remote := range catalog.Data {
		if remote.ID == "" || remote.Policy.State == "disabled" {
			continue
		}
		endpoint := modelEndpoint(remote.Endpoints)
		// Never infer a route or follow an arbitrary catalog-supplied URL.
		if endpoint == "" {
			continue
		}
		supports := &remote.Capabilities.Supports
		if endpoint == "/v1/messages" && supports.Adaptive == nil && len(supports.Efforts) > 0 {
			adaptive := true
			supports.Adaptive = &adaptive
		}
		model := remote.Model(endpoint)
		applyCapabilities(&model)
		cache[model.ID] = model
		if remote.Picker == nil || *remote.Picker {
			models = append(models, model)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = cache
	return models, nil
}

// LookupModel resolves only advertised routes from this Microsoft account.
func (c *Client) LookupModel(ctx context.Context, id string) (provider.Model, error) {
	if err := c.connected(ctx); err != nil {
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

func modelEndpoint(advertised []string) string {
	for _, preferred := range []string{"/v1/messages", "/responses", "/chat/completions"} {
		for _, endpoint := range advertised {
			if endpoint == preferred {
				return preferred
			}
		}
	}
	return ""
}

func applyCapabilities(model *provider.Model) {
	if model.Endpoint != "/v1/messages" {
		return
	}
	knownClaude := strings.HasPrefix(model.ID, "claude-opus-") || strings.HasPrefix(model.ID, "claude-sonnet-") || strings.HasPrefix(model.ID, "claude-haiku-")
	if !knownClaude {
		return
	}
	// The M365 catalog omits these flags for the established Claude families.
	// Keep explicit service restrictions authoritative. Do not infer PDF support
	// or an output limit from the context-window size.
	if !model.VisionKnown {
		model.Vision, model.VisionKnown = true, true
	}
	if !model.ToolsKnown {
		model.Tools, model.ToolsKnown = true, true
	}
	// The supplied M365 integration documents Haiku 4.5's budget-based
	// thinking. Limit the default budget to the app's 8192-token output request.
	if model.ID == "claude-haiku-4.5" && !model.ReasoningKnown {
		budget := 8191
		if model.MaxOutputTokens > 0 {
			budget = min(budget, model.MaxOutputTokens-1)
		}
		if budget >= 1024 {
			model.Reasoning, model.ReasoningKnown = true, true
			model.MinThinkingBudget, model.MaxThinkingBudget = 1024, budget
		}
	}
}
