package provider

import (
	"encoding/json"
	"strings"
)

// Catalog retains picker visibility separately from availability for inference.
type Catalog struct {
	Data []CatalogEntry `json:"data"`
}

type CatalogEntry struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	DisplayName    string   `json:"display_name"`
	CreatedAt      string   `json:"created_at"`
	Lifecycle      string   `json:"lifecycle"`
	DeprecatedAt   string   `json:"deprecated_at"`
	RetiresAt      string   `json:"retires_at"`
	Line           string   `json:"line"`
	MaxInputTokens int      `json:"max_input_tokens"`
	MaxTokens      int      `json:"max_tokens"`
	Picker         *bool    `json:"model_picker_enabled"`
	Endpoints      []string `json:"supported_endpoints"`
	Policy         struct {
		State string `json:"state"`
	} `json:"policy"`
	Capabilities CatalogCapabilities `json:"capabilities"`
}

type CatalogCapabilities struct {
	Supports struct {
		Vision            *bool    `json:"vision"`
		Tools             *bool    `json:"tool_calls"`
		Reasoning         *bool    `json:"reasoning"`
		Efforts           []string `json:"reasoning_effort"`
		Adaptive          *bool    `json:"adaptive_thinking"`
		MinBudget         *int     `json:"min_thinking_budget"`
		MaxBudget         *int     `json:"max_thinking_budget"`
		StructuredOutputs *bool    `json:"structured_outputs"`
	} `json:"supports"`
	Limits struct {
		MaxOutput     int `json:"max_output_tokens"`
		ContextWindow int `json:"max_context_window_tokens"`
		Vision        struct {
			Types []string `json:"supported_media_types"`
		} `json:"vision"`
	} `json:"limits"`
	Native map[string]json.RawMessage `json:"-"`
}

// Retain native Models API capability fields alongside Copilot's supports/limits.
func (c *CatalogCapabilities) UnmarshalJSON(data []byte) error {
	type plain CatalogCapabilities
	if err := json.Unmarshal(data, (*plain)(c)); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &c.Native); err != nil {
		return err
	}
	delete(c.Native, "supports")
	delete(c.Native, "limits")
	for key := range c.Native {
		switch key {
		case "batch", "citations", "code_execution", "context_management", "effort", "image_input", "pdf_input", "server_tools", "structured_outputs", "thinking":
		default:
			delete(c.Native, key)
		}
	}
	return nil
}

// Model converts advertised capability facts after the provider chooses a route.
// A provider may adjust a copy of the entry to add documented capability defaults.
func (remote CatalogEntry) Model(endpoint string) Model {
	supports := remote.Capabilities.Supports
	model := Model{ID: remote.ID, Name: remote.Name, Endpoint: endpoint, Endpoints: append([]string(nil), remote.Endpoints...),
		MaxOutputTokens: remote.Capabilities.Limits.MaxOutput, ContextWindow: remote.Capabilities.Limits.ContextWindow,
		CreatedAt: remote.CreatedAt, Lifecycle: remote.Lifecycle, DeprecatedAt: remote.DeprecatedAt, RetiresAt: remote.RetiresAt, Line: remote.Line}
	if remote.MaxInputTokens > 0 {
		model.ContextWindow = remote.MaxInputTokens
	}
	if remote.MaxTokens > 0 {
		model.MaxOutputTokens = remote.MaxTokens
	}
	if remote.DisplayName != "" {
		model.Name = remote.DisplayName
	}
	if len(remote.Capabilities.Native) > 0 {
		model.Capabilities, _ = json.Marshal(remote.Capabilities.Native)
		var native struct {
			ImageInput *struct{ Supported bool } `json:"image_input"`
			PDFInput   *struct{ Supported bool } `json:"pdf_input"`
			Thinking   *struct {
				Supported bool
				Types     struct{ Adaptive struct{ Supported bool } }
			}
			Effort struct {
				Low, Medium, High, Xhigh, Max struct{ Supported bool }
			}
		}
		_ = json.Unmarshal(model.Capabilities, &native)
		if native.ImageInput != nil {
			model.Vision, model.VisionKnown = native.ImageInput.Supported, true
		}
		if native.PDFInput != nil {
			model.PDF, model.PDFKnown = native.PDFInput.Supported, true
		}
		if native.Thinking != nil {
			model.Reasoning, model.ReasoningKnown = native.Thinking.Supported, true
			model.AdaptiveThinking = native.Thinking.Types.Adaptive.Supported
		}
		for _, effort := range []struct {
			name      string
			supported bool
		}{
			{"low", native.Effort.Low.Supported}, {"medium", native.Effort.Medium.Supported},
			{"high", native.Effort.High.Supported}, {"xhigh", native.Effort.Xhigh.Supported}, {"max", native.Effort.Max.Supported},
		} {
			if effort.supported {
				model.ThinkingEfforts = append(model.ThinkingEfforts, effort.name)
			}
		}
	}
	if supports.Vision != nil {
		model.Vision, model.VisionKnown = *supports.Vision, true
	}
	if supports.StructuredOutputs != nil {
		model.StructuredOutputs = *supports.StructuredOutputs
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
	model.Reasoning = model.Reasoning || model.AdaptiveThinking || model.MaxThinkingBudget > 0 || len(supports.Efforts) > 0 || len(model.ThinkingEfforts) > 0
	// Anthropic's named effort requires its advertised adaptive-thinking route.
	if model.Endpoint != "/v1/messages" || model.AdaptiveThinking {
		if supports.Efforts != nil {
			model.ThinkingEfforts = nil
		}
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
