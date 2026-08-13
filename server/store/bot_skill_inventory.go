package store

import (
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// ShouldReplaceBotSkillInventory prevents an older in-flight runtime snapshot
// from overwriting a newer one. The database adapter calls this while holding
// the bot-definition row lock, so a runtime-instance change is ordered by
// server receipt order rather than an untrusted client clock. Within one
// runtime instance, the monotonic report sequence remains the authoritative
// ordering token.
func ShouldReplaceBotSkillInventory(existing *types.BotSkillInventory, incoming types.BotSkillInventory) bool {
	if existing == nil {
		return true
	}

	existingInstance := strings.TrimSpace(existing.RuntimeInstanceID)
	incomingInstance := strings.TrimSpace(incoming.RuntimeInstanceID)
	if incomingInstance != "" && incomingInstance == existingInstance {
		if incoming.ReportSequence > 0 || existing.ReportSequence > 0 {
			return incoming.ReportSequence > existing.ReportSequence
		}
		// Legacy reports from the same instance have no monotonic token; the
		// row-lock receipt order is the only safe ordering available.
		return true
	}

	// Different runtime instances (including legacy reports without an
	// instance ID) are ordered by the server-side receipt time. Never compare
	// ObservedAt here: it is supplied by the client and can be delayed or
	// clock-skewed.
	incomingReceipt, incomingOK := parseInventoryTime(incoming.ReportedAt)
	existingReceipt, existingOK := parseInventoryTime(existing.ReportedAt)
	if incomingOK && existingOK {
		return incomingReceipt.After(existingReceipt)
	}
	return true
}

func parseInventoryTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return parsed, err == nil
}
