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
	if slices.Contains(m.ThinkingEfforts, "max") {
		return "fable"
	}
	if slices.Contains(m.ThinkingEfforts, "xhigh") {
		return "opus"
	}
	hasEffort := slices.ContainsFunc(m.ThinkingEfforts, func(level string) bool { return level != "" && level != "none" })
	if m.Reasoning || m.AdaptiveThinking || m.MaxThinkingBudget > 0 || hasEffort || m.ContextWindow >= 1000000 {
		return "opus"
	}
	return "sonnet"
}

func claudeDesktopModelID(m Model) string {
	name := claudeDesktopModelLine(m.Model)
	if strings.Contains(strings.ToLower(m.Model.ID), "claude") {
		name = m.Model.ID
	} else if !slices.Contains(m.Model.ThinkingEfforts, "max") && !slices.Contains(m.Model.ThinkingEfforts, "xhigh") {
		name += "-4-5"
	}
	// Resolve the 128-bit decimal fingerprint against the active profile rather
	// than keeping a separate alias database. Provider identity prevents account
	// collisions; omitting catalog order, public routes and capabilities keeps
	// the numeric code stable across restarts and capability refreshes.
	digest := sha256.Sum256([]byte(m.ProviderID + "\x00" + m.Model.ID))
	code := new(big.Int).SetBytes(digest[:16]).String()
	return name + "-" + code
}
