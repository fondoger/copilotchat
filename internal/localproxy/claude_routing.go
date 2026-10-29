package localproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

const (
	claudeSonnetDefault   = "ANTHROPIC_DEFAULT_SONNET_MODEL"
	claudeHaikuDefault    = "ANTHROPIC_DEFAULT_HAIKU_MODEL"
	claudeSubagentDefault = "CLAUDE_CODE_SUBAGENT_MODEL"
)

var desktopModelFamily = regexp.MustCompile(`^(?:claude-)?(sonnet|haiku)(?:-[0-9][0-9A-Za-z._-]*)?$`)

type modelRoute struct {
	model      Model
	target     Target
	ctx        context.Context
	reason     string
	classifier bool
}

func desktopModelDefault(id string) string {
	match := desktopModelFamily.FindStringSubmatch(strings.ToLower(id))
	if len(match) == 0 {
		return ""
	}
	if match[1] == "haiku" {
		return claudeHaikuDefault
	}
	return claudeSonnetDefault
}

func isDesktopSubagentRequest(header http.Header, body []byte) bool {
	if strings.TrimSpace(header.Get("X-Claude-Code-Agent-Id")) != "" {
		return true
	}
	var request struct {
		System json.RawMessage `json:"system"`
	}
	if json.Unmarshal(body, &request) != nil {
		return false
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(request.System, &blocks) != nil {
		var text string
		if json.Unmarshal(request.System, &text) != nil {
			return false
		}
		blocks = append(blocks, struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text", Text: text})
	}
	for _, block := range blocks {
		if block.Type != "text" || !strings.HasPrefix(block.Text, "x-anthropic-billing-header:") {
			continue
		}
		line, _, _ := strings.Cut(block.Text, "\n")
		line = strings.TrimPrefix(line, "x-anthropic-billing-header:")
		var desktop, subagent bool
		for field := range strings.SplitSeq(line, ";") {
			key, value, _ := strings.Cut(strings.TrimSpace(field), "=")
			switch key {
			case "cc_entrypoint":
				desktop = value == "claude-desktop-3p"
			case "cc_is_subagent":
				subagent = value == "true"
			}
		}
		if desktop && subagent {
			return true
		}
	}
	return false
}

func compatibleProtocol(model Model, endpoint, protocol string) bool {
	return supports(model.Model, protocol) || (protocol == "/v1/messages" && supportsMessages(model.Model)) || (endpoint == "/responses" && supportsResponses(model.Model))
}

// The caller holds s.mu while resolving the current profile and account.
func (s *Server) configuredRoute(model Model, endpoint, protocol, reason string, classifier bool) modelRoute {
	route := modelRoute{model: model, reason: reason, classifier: classifier}
	if item := s.targets[model.ProviderID]; item != nil && compatibleProtocol(model, endpoint, protocol) {
		route.target, route.ctx = item.Target, item.ctx
	}
	return route
}

func (s *Server) routeModel(profile *profileEntry, desktop bool, endpoint, protocol, id string, classifier, subagent bool) modelRoute {
	if profile == nil {
		return modelRoute{}
	}
	if endpoint == "/v1/messages" {
		if desktop && classifier {
			key := desktopModelDefault(id)
			if key == "" {
				key = claudeSonnetDefault
			}
			if model := profile.ClaudeDefaults[key]; model.Model.ID != "" {
				return s.configuredRoute(model, endpoint, protocol, "desktop_security_classifier", true)
			}
		}
		if model := profile.ClaudeDefaults[claudeSonnetDefault]; id == ClassifierModelID(model) {
			return s.configuredRoute(model, endpoint, protocol, "claude_code_classifier", true)
		}
	}
	if protocol == "/v1/messages" {
		for _, model := range profile.ClaudeDefaults {
			if id == DefaultModelID(model) {
				return s.configuredRoute(model, endpoint, protocol, "claude_default", false)
			}
		}
	}
	// Removed private routes must not switch to a different configured account.
	if strings.HasPrefix(id, "copilotchat-default-") || strings.HasPrefix(id, "copilotchat-classifier-") {
		return modelRoute{}
	}
	models := profile.Models
	desktopModels := clientModelIDs(models, desktop)
	for i, candidate := range models {
		matches := candidate.ID == id || slices.Contains(candidate.Routes, id) || (desktop && desktopModels[i].ID == id)
		if matches && compatibleProtocol(candidate, endpoint, protocol) {
			return s.configuredRoute(candidate, endpoint, protocol, "", false)
		}
	}
	if desktop {
		for _, candidate := range models {
			if compatibleProtocol(candidate, endpoint, protocol) && claudeDesktopModelAliasMatches(candidate, id) {
				return s.configuredRoute(candidate, endpoint, protocol, "", false)
			}
		}
	}
	if desktop && endpoint == "/v1/messages" && !classifier && subagent {
		key, reason := claudeSubagentDefault, "desktop_subagent_default_fallback"
		model := profile.ClaudeDefaults[key]
		if model.Model.ID == "" {
			key = desktopModelDefault(id)
			model = profile.ClaudeDefaults[key]
			if key == claudeSonnetDefault {
				reason = "desktop_subagent_sonnet_fallback"
			} else if key == claudeHaikuDefault {
				reason = "desktop_subagent_haiku_fallback"
			}
		}
		if model.Model.ID != "" {
			return s.configuredRoute(model, endpoint, protocol, reason, false)
		}
	}
	return modelRoute{}
}

func unavailableModelMessage(desktop bool, id string, classifier, subagent bool) string {
	message := "Model ID is disabled, unavailable, or does not support this API. Check LLM Gateway in Copilot Chat."
	if !desktop {
		return message
	}
	var level string
	switch desktopModelDefault(id) {
	case claudeSonnetDefault:
		level = "Sonnet"
	case claudeHaikuDefault:
		level = "Haiku"
	default:
		return message
	}
	message += " Please tell the user that they can configure a default " + level + " level model in Copilot Chat's Claude Desktop setup"
	if classifier {
		message += " for Auto mode security classifier requests"
	} else if subagent {
		message += " for subagent requests"
	}
	return message + "."
}
