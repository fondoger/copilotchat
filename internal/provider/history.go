package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// HistoryHash binds an ordered request history to its local conversation.
func HistoryHash(localID string, messages []Message) string {
	h := sha256.New()
	encoder := json.NewEncoder(h)
	_ = encoder.Encode(localID)
	if encoder.Encode(messages) != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
