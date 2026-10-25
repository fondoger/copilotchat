package localproxy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// ClassifierModelID is a purpose-specific route, not a catalog entry. Only the
// currently configured classifier can be reached through this stable account ID.
func ClassifierModelID(model Model) string {
	if model.Model.ID == "" {
		return ""
	}
	return fmt.Sprintf("copilotchat-classifier-%x", sha256.Sum256([]byte(model.ProviderID+"\x00"+model.Model.ID)))
}

// This fingerprint only selects a route; the caller must also authenticate with
// the Desktop key. Check before protocol conversion removes cache markers.
func isDesktopClassifierRequest(body []byte) bool {
	var request struct {
		System        json.RawMessage `json:"system"`
		StopSequences []string        `json:"stop_sequences"`
	}
	if json.Unmarshal(body, &request) != nil || len(request.StopSequences) != 1 || request.StopSequences[0] != "</severity>" {
		return false
	}
	var system []struct {
		CacheControl struct {
			Type string `json:"type"`
		} `json:"cache_control"`
	}
	if json.Unmarshal(request.System, &system) != nil {
		return false
	}
	for _, block := range system {
		if block.CacheControl.Type == "ephemeral" {
			return true
		}
	}
	return false
}
