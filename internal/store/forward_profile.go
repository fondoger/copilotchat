package store

import "encoding/json"

// UnmarshalJSON migrates classifier choices on load. Saving uses only the
// canonical field, including an empty choice that must not revive old defaults.
func (p *ForwardProfile) UnmarshalJSON(data []byte) error {
	type profile ForwardProfile
	var decoded profile
	wire := struct {
		*profile
		Classifier       json.RawMessage `json:"claudeClassifierModel"`
		LegacyClassifier ForwardModelRef `json:"claudeDesktopClassifierModel"`
	}{profile: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Classifier != nil {
		if err := json.Unmarshal(wire.Classifier, &decoded.ClaudeClassifierModel); err != nil {
			return err
		}
	} else if wire.LegacyClassifier != (ForwardModelRef{}) {
		decoded.ClaudeClassifierModel = wire.LegacyClassifier
	} else {
		decoded.ClaudeClassifierModel = decoded.ClaudeDefaults["CLAUDE_CODE_AUTO_MODE_MODEL"]
	}
	delete(decoded.ClaudeDefaults, "CLAUDE_CODE_AUTO_MODE_MODEL")
	*p = ForwardProfile(decoded)
	return nil
}
