package localproxy

import (
	"crypto/sha256"
	"math/big"
	"slices"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
)

// Desktop rejects other vendors' model IDs and uses Claude family names for UI
// defaults. The actual name and capabilities remain in the model metadata.
func claudeDesktopModelLine(m provider.Model) string {
	if strings.Contains(strings.ToLower(m.ID), "claude") {
		for _, line := range []string{"haiku", "sonnet", "opus", "fable", "mythos"} {
			if slices.Contains(strings.Split(strings.ToLower(m.ID), "-"), line) {
				return line
			}
		}
		return "sonnet"
	}
	return "mythos"
}

func claudeDesktopModelID(m Model) string {
	if strings.Contains(strings.ToLower(m.Model.ID), "claude") {
		return strings.ReplaceAll(m.Model.ID, ".", "-")
	}
	return claudeDesktopOpaqueID(m)
}

// Bind the decimal fingerprint to the original model and provider, independent
// of capabilities, account display names, and catalog sorting.
func claudeDesktopOpaqueID(m Model) string {
	digest := sha256.Sum256([]byte(m.ProviderID + "\x00" + m.Model.ID))
	code := new(big.Int).SetBytes(digest[:16]).String()
	return "mythos-" + code
}
