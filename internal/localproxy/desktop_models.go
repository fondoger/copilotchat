package localproxy

import (
	"crypto/md5"
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

// Bind the decimal fingerprint to the stable account and original model.
// Fall back to the configured provider ID before account metadata is available.
func claudeDesktopOpaqueID(m Model) string {
	identity := m.ProviderKey
	if identity == "" {
		identity = m.ProviderID
	}
	digest := md5.Sum([]byte(identity + "\x00" + m.Model.ID))
	code := new(big.Int).SetBytes(digest[:8]).String()
	return "mythos-" + code
}

// Accept earlier encodings as request aliases without advertising extra models.
func claudeDesktopModelAliasMatches(m Model, id string) bool {
	if !strings.HasPrefix(id, "mythos-") {
		return false
	}
	if claudeDesktopOpaqueID(m) == id {
		return true
	}
	if m.ProviderKey != "" {
		m.ProviderKey = ""
		if claudeDesktopOpaqueID(m) == id {
			return true
		}
	}
	digest := sha256.Sum256([]byte(m.ProviderID + "\x00" + m.Model.ID))
	return id == "mythos-"+new(big.Int).SetBytes(digest[:16]).String()
}
