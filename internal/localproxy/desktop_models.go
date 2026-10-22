package localproxy

import (
	"crypto/sha256"
	"math/big"
	"slices"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
)

// Desktop checks the ID against its own model families before showing effort
// controls, and rejects IDs containing other vendors' names. Keep the real name
// in display_name only. This is a client compatibility name, not a claim that
// another provider's model has Claude's capabilities.
func claudeDesktopModelLine(m provider.Model) string {
	if strings.Contains(strings.ToLower(m.ID), "claude") {
		for _, line := range []string{"haiku", "sonnet", "opus", "fable", "mythos"} {
			if slices.Contains(strings.Split(strings.ToLower(m.ID), "-"), line) {
				return line
			}
		}
		return "sonnet"
	}
	// Use the larger families for extra effort levels and long contexts. Keep
	// the actual window and effort capabilities in /v1/models unchanged.
	if slices.Contains(m.ThinkingEfforts, "max") || slices.Contains(m.ThinkingEfforts, "xhigh") {
		if m.ContextWindow >= 1000000 {
			return "mythos"
		}
		return "fable"
	}
	hasEffort := slices.ContainsFunc(m.ThinkingEfforts, func(level string) bool { return level != "" && level != "none" })
	if m.Reasoning || m.AdaptiveThinking || m.MaxThinkingBudget > 0 || hasEffort {
		if m.ContextWindow >= 1000000 && slices.Contains(m.ThinkingEfforts, "high") {
			return "opus"
		}
		return "sonnet"
	}
	return "haiku"
}

func claudeDesktopModelID(m Model) string {
	name := claudeDesktopModelLine(m.Model)
	if strings.Contains(strings.ToLower(m.Model.ID), "claude") {
		name = m.Model.ID
	}
	// Resolve the 128-bit decimal fingerprint against the active profile rather
	// than keeping a separate alias database. Provider identity prevents account
	// collisions; omitting catalog order, public routes and capabilities keeps
	// the code stable across restarts, account additions and catalog refreshes.
	digest := sha256.Sum256([]byte(m.ProviderID + "\x00" + m.Model.ID))
	code := new(big.Int).SetBytes(digest[:16]).String()
	return name + "-" + code
}
