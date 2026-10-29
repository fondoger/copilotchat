package store

import "encoding/json"

// UnmarshalJSON migrates shared classifier choices to Code's dedicated field
// and Desktop's Sonnet default. The new field prevents repeating that migration.
func (p *ForwardProfile) UnmarshalJSON(data []byte) error {
	type profile ForwardProfile
	var decoded profile
	wire := struct {
		*profile
		CodeClassifier   json.RawMessage `json:"claudeCodeClassifierModel"`
		Classifier       json.RawMessage `json:"claudeClassifierModel"`
		LegacyClassifier ForwardModelRef `json:"claudeDesktopClassifierModel"`
	}{profile: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.CodeClassifier != nil {
		if err := json.Unmarshal(wire.CodeClassifier, &decoded.ClaudeClassifierModel); err != nil {
			return err
		}
	} else if wire.Classifier != nil {
		if err := json.Unmarshal(wire.Classifier, &decoded.ClaudeClassifierModel); err != nil {
			return err
		}
	} else if wire.LegacyClassifier != (ForwardModelRef{}) {
		decoded.ClaudeClassifierModel = wire.LegacyClassifier
	} else {
		decoded.ClaudeClassifierModel = decoded.ClaudeDefaults["CLAUDE_CODE_AUTO_MODE_MODEL"]
	}
	delete(decoded.ClaudeDefaults, "CLAUDE_CODE_AUTO_MODE_MODEL")
	if wire.CodeClassifier == nil && decoded.Agent == "Claude Desktop" && decoded.ClaudeClassifierModel.ModelID != "" {
		if decoded.ClaudeDefaults == nil {
			decoded.ClaudeDefaults = map[string]ForwardModelRef{}
		}
		if _, set := decoded.ClaudeDefaults["ANTHROPIC_DEFAULT_SONNET_MODEL"]; !set {
			decoded.ClaudeDefaults["ANTHROPIC_DEFAULT_SONNET_MODEL"] = decoded.ClaudeClassifierModel
		}
	}
	*p = ForwardProfile(decoded)
	return nil
}
