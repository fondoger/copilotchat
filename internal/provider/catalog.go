package provider

import "strings"

// Catalog retains picker visibility separately from availability for inference.
type Catalog struct {
	Data []CatalogEntry `json:"data"`
}

type CatalogEntry struct {
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
}

// Model converts advertised capability facts after the provider chooses a route.
// A provider may adjust a copy of the entry to add documented capability defaults.
func (remote CatalogEntry) Model(endpoint string) Model {
	supports := remote.Capabilities.Supports
	model := Model{ID: remote.ID, Name: remote.Name, Endpoint: endpoint, Endpoints: append([]string(nil), remote.Endpoints...),
		MaxOutputTokens: remote.Capabilities.Limits.MaxOutput}
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
	return model
}
