package store

import (
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// ShouldReplaceBotSkillInventory prevents an older in-flight runtime snapshot
// from overwriting a newer one. New XiaoBa runtimes provide a per-process
// instance ID and monotonic sequence. Legacy reporters are ordered by their
// observed-at timestamp instead.
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
	}

	incomingObserved, incomingOK := parseInventoryTime(incoming.ObservedAt)
	existingObserved, existingOK := parseInventoryTime(existing.ObservedAt)
	if incomingOK && existingOK {
		return incomingObserved.After(existingObserved)
	}
	// The handler validates new observations. This fallback keeps malformed
	// legacy records from permanently blocking the first valid new snapshot.
	return true
}

func parseInventoryTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return parsed, err == nil
}
