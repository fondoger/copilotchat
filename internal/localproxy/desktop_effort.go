package localproxy

import (
	"encoding/json"

	"github.com/fondoger/copilotchat/internal/provider"
)

// Desktop's family presets can offer max/xhigh even when the upstream model
// doesn't. Clamp before either native forwarding or protocol conversion.
func normalizeDesktopEffort(body []byte, model provider.Model) []byte {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return body
	}
	clamp := func(fields map[string]json.RawMessage, key string) bool {
		var effort string
		if json.Unmarshal(fields[key], &effort) != nil || (effort != "max" && effort != "xhigh") {
			return false
		}
		level := supportedEffort(effort, model.ThinkingEfforts)
		if level == effort {
			return false
		}
		if level == "" {
			delete(fields, key)
		} else {
			fields[key], _ = json.Marshal(level)
		}
		return true
	}
	changed := clamp(fields, "reasoning_effort")
	for _, key := range []string{"output_config", "reasoning"} {
		var config map[string]json.RawMessage
		if json.Unmarshal(fields[key], &config) == nil && clamp(config, "effort") {
			fields[key], _ = json.Marshal(config)
			changed = true
		}
	}
	if !changed {
		return body
	}
	// Retain exact tool numbers, cache controls, and signed thinking blocks.
	encoded, _ := json.Marshal(fields)
	return encoded
}
