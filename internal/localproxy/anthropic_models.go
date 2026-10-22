package localproxy

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

func clientModelIDs(models []Model, desktop bool) []Model {
	if !desktop {
		return models
	}
	models = slices.Clone(models)
	// Profile order is persisted. Assign the plain Claude ID before any catalog
	// date sorting or pagination, and use this same mapping to resolve requests.
	seen := map[string]bool{}
	for i := range models {
		id := claudeDesktopModelID(models[i])
		if seen[id] {
			id = claudeDesktopOpaqueID(models[i])
		}
		seen[id] = true
		models[i].ID = id
	}
	return models
}

func modelReleaseDate(value string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC()
	}
	return time.Unix(0, 0).UTC() // The Models API permits epoch for unknown dates.
}

func nullableDate(value string) any {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return nil
}

func positiveLimit(value int) any {
	if value > 0 {
		return value
	}
	return nil
}

func modelLifecycle(model Model) string {
	switch model.Model.Lifecycle {
	case "deprecated", "retired":
		return model.Model.Lifecycle
	default:
		return "active"
	}
}

func capability(supported bool) object { return object{"supported": supported} }

func claudeModelLine(id string) string {
	id = strings.ToLower(id)
	for _, line := range []string{"haiku", "sonnet", "opus", "fable", "mythos"} {
		if strings.Contains(id, "-"+line+"-") {
			return line
		}
	}
	return ""
}

// Fill missing native fields from facts already loaded from the Copilot catalog.
// A model must not advertise APIs that the gateway cannot forward or translate.
func anthropicCapabilities(model Model) object {
	m := model.Model
	native := supports(m, "/v1/messages")
	effort := capability(false)
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		supported := slices.Contains(m.ThinkingEfforts, level)
		effort[level] = capability(supported)
		if supported {
			effort["supported"] = true
		}
	}
	defaults := object{
		"batch": capability(false), "citations": capability(false), "code_execution": capability(false),
		"context_management": object{"supported": false, "clear_thinking_20251015": nil, "clear_tool_uses_20250919": nil, "compact_20260112": nil},
		"effort":             effort, "image_input": capability(m.Vision), "pdf_input": capability(m.PDF),
		"server_tools":       object{"supported": false, "code_execution": capability(false), "web_search": capability(false)},
		"structured_outputs": capability(m.StructuredOutputs),
		"thinking": object{"supported": m.Reasoning, "types": object{
			"adaptive": capability(m.AdaptiveThinking || (!native && len(m.ThinkingEfforts) > 0)),
			"enabled":  capability(m.Reasoning && (!native || m.MaxThinkingBudget > 0)),
			"disabled": capability(!native || !m.Reasoning), "between_tools": capability(false),
		}},
	}
	result, err := decodeObject(m.Capabilities)
	if err != nil {
		result = object{}
	}
	var fill func(object, object)
	fill = func(dst, src object) {
		for key, value := range src {
			if existing, ok := dst[key]; !ok {
				dst[key] = value
			} else if nested := obj(value); nested != nil && obj(existing) != nil {
				fill(obj(existing), nested)
			}
		}
	}
	fill(result, defaults)
	result["batch"] = capability(false)
	if !native {
		for _, key := range []string{"citations", "code_execution", "context_management", "server_tools"} {
			result[key] = defaults[key]
		}
	}
	return result
}

func anthropicModel(model Model) object {
	m := model.Model
	label := m.Name
	if label == "" {
		label = m.ID
	}
	if model.ProviderName != "" {
		label += " (" + model.ProviderName + ")"
	}
	var line any
	if family := claudeModelLine(m.ID); family != "" {
		line = family
	}
	lifecycle := modelLifecycle(model)
	var deprecated, retires any
	if lifecycle != "active" {
		deprecated, retires = nullableDate(m.DeprecatedAt), nullableDate(m.RetiresAt)
	}
	return object{
		"type": "model", "id": model.ID, "display_name": label,
		"created_at":       modelReleaseDate(m.CreatedAt).Format(time.RFC3339Nano),
		"capabilities":     anthropicCapabilities(model),
		"max_input_tokens": positiveLimit(m.ContextWindow), "max_tokens": positiveLimit(m.MaxOutputTokens),
		"lifecycle": lifecycle, "deprecated_at": deprecated, "retires_at": retires, "line": line,
	}
}

func writeAnthropicModels(w http.ResponseWriter, r *http.Request, models []Model, desktop bool) {
	q := r.URL.Query()
	limit := 20
	if values, ok := q["limit"]; ok {
		var err error
		if len(values) != 1 {
			apiError(w, 400, "limit must be an integer between 1 and 1000")
			return
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 1000 {
			apiError(w, 400, "limit must be an integer between 1 and 1000")
			return
		}
	}
	after, before := q.Get("after_id"), q.Get("before_id")
	if after != "" && before != "" {
		apiError(w, 400, "Use either after_id or before_id, not both")
		return
	}
	lifecycles := append(slices.Clone(q["lifecycle"]), q["lifecycle[]"]...)
	if len(lifecycles) > 3 {
		apiError(w, 400, "lifecycle accepts at most 3 values")
		return
	}
	for _, lifecycle := range lifecycles {
		if !slices.Contains([]string{"active", "deprecated", "retired"}, lifecycle) {
			apiError(w, 400, "Unknown model lifecycle")
			return
		}
	}
	if len(lifecycles) == 0 {
		lifecycles = []string{"active", "deprecated"}
	}
	filtered := []Model{}
	for _, model := range clientModelIDs(models, desktop) {
		if supportsMessages(model.Model) && slices.Contains(lifecycles, modelLifecycle(model)) {
			filtered = append(filtered, model)
		}
	}
	// Sorting the catalog must never change the saved provider routing priority.
	slices.SortStableFunc(filtered, func(a, b Model) int {
		return modelReleaseDate(b.Model.CreatedAt).Compare(modelReleaseDate(a.Model.CreatedAt))
	})
	start, end := 0, len(filtered)
	if cursor := after + before; cursor != "" {
		index := slices.IndexFunc(filtered, func(m Model) bool { return m.ID == cursor })
		if index < 0 {
			apiError(w, 400, "Model cursor is not available in this catalog")
			return
		}
		if after != "" {
			start = index + 1
		} else {
			end = index
		}
	}
	hasMore := false
	if before != "" {
		start = max(start, end-limit)
		hasMore = start > 0
	} else {
		end = min(end, start+limit)
		hasMore = end < len(filtered)
	}
	data := []object{}
	var firstID, lastID any
	for _, model := range filtered[start:end] {
		row := anthropicModel(model)
		if desktop {
			row["line"] = claudeDesktopModelLine(model.Model)
			if strings.HasPrefix(model.ID, "mythos-") {
				row["line"] = "mythos"
			}
		}
		data = append(data, row)
	}
	if len(data) > 0 {
		firstID, lastID = data[0]["id"], data[len(data)-1]["id"]
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(object{"data": data, "has_more": hasMore, "first_id": firstID, "last_id": lastID})
}
