package server

import (
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
)

// DeepSeek Flash protocol-lane split.
//
// CatsCompany is the authority for which relay protocol a bot uses: desktop
// clients materialize their relay runtime from the catalog descriptor shipped
// with the bot definition / model configuration. DeepSeek Flash can be served
// by two relay lanes (OpenAI Responses and Anthropic); both reach the same
// upstream model, but their gateways behave differently (the Responses bridge
// drops leading output characters, the Anthropic bridge does not).
//
// To migrate traffic between the two lanes without asking users to pick a
// protocol, the descriptor's provider is decided here:
//   - CATS_DEEPSEEK_ANTHROPIC_GRAY_PERCENT: 0-100, deterministic share of bots
//     assigned to the Anthropic lane (default 0 keeps the current behavior).
//   - CATS_DEEPSEEK_ANTHROPIC_GRAY_BOTS: comma-separated bot uids forced onto
//     the Anthropic lane regardless of the percentage (canary bots).
//   - CATS_DEEPSEEK_ANTHROPIC_GRAY_EXCLUDE_BOTS: comma-separated bot uids kept
//     on the default lane; exclusion wins over both other controls.
//
// The assignment is a stable hash of the bot uid, so a bot never flaps between
// protocols while the configuration stays put; raising the percentage only
// moves bots onto the Anthropic lane and lowering it moves them back. The
// switches apply to every bot that holds a catalog selection resolving to
// DeepSeek Flash; bots on other models or with local definitions are untouched.

const (
	deepSeekAnthropicLanePercentEnv = "CATS_DEEPSEEK_ANTHROPIC_GRAY_PERCENT"
	deepSeekAnthropicLaneBotsEnv    = "CATS_DEEPSEEK_ANTHROPIC_GRAY_BOTS"
	deepSeekAnthropicLaneExcludeEnv = "CATS_DEEPSEEK_ANTHROPIC_GRAY_EXCLUDE_BOTS"
)

var deepSeekAnthropicLaneInvalidPercentOnce sync.Once

// deepSeekAnthropicLanePercent reads the configured share of bots that use the
// Anthropic lane. Unset values and explicit 0 disable the split; malformed
// values disable it too but are reported once so an operator typo cannot look
// like a silent rollout.
func deepSeekAnthropicLanePercent() int {
	raw := strings.TrimSpace(os.Getenv(deepSeekAnthropicLanePercentEnv))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		deepSeekAnthropicLaneInvalidPercentOnce.Do(func() {
			log.Printf("deepseek-flash anthropic lane: ignoring invalid %s=%q", deepSeekAnthropicLanePercentEnv, raw)
		})
		return 0
	}
	if value == 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// deepSeekAnthropicLaneUIDList parses a comma-separated uid list, ignoring
// malformed or non-positive entries.
func deepSeekAnthropicLaneUIDList(raw string) []int64 {
	values := make([]int64, 0, 4)
	for _, item := range strings.Split(raw, ",") {
		uid, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
		if err == nil && uid > 0 {
			values = append(values, uid)
		}
	}
	return values
}

// deepSeekAnthropicLaneForced reports whether the bot is listed as a canary.
func deepSeekAnthropicLaneForced(botUID int64) bool {
	if botUID <= 0 {
		return false
	}
	for _, uid := range deepSeekAnthropicLaneUIDList(os.Getenv(deepSeekAnthropicLaneBotsEnv)) {
		if uid == botUID {
			return true
		}
	}
	return false
}

// deepSeekAnthropicLaneExcluded reports whether the bot is pinned to the
// default lane. Exclusion wins over both the percentage and the canary list so
// a single bot can be pulled back without touching the rollout share.
func deepSeekAnthropicLaneExcluded(botUID int64) bool {
	if botUID <= 0 {
		return false
	}
	for _, uid := range deepSeekAnthropicLaneUIDList(os.Getenv(deepSeekAnthropicLaneExcludeEnv)) {
		if uid == botUID {
			return true
		}
	}
	return false
}

// deepSeekFlashUsesAnthropicLane decides the lane for a bot. Zero and negative
// bot ids (unconfigured records) stay on the default lane.
func deepSeekFlashUsesAnthropicLane(botUID int64) bool {
	if botUID <= 0 {
		return false
	}
	if deepSeekAnthropicLaneExcluded(botUID) {
		return false
	}
	if deepSeekAnthropicLaneForced(botUID) {
		return true
	}
	percent := deepSeekAnthropicLanePercent()
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	hasher := fnv.New32a()
	fmt.Fprintf(hasher, "%s:%d", deepSeekPublicModelID, botUID)
	return int(hasher.Sum32()%100) < percent
}

// catalogRuntimeDescriptorForBot returns the runtime descriptor for a catalog
// model as seen by one bot. Only DeepSeek Flash can be served by either relay
// protocol; every other model keeps the catalog default.
func catalogRuntimeDescriptorForBot(botUID int64, modelID string) *botModelRuntimeDescriptor {
	descriptor := catalogRuntimeDescriptorForModel(modelID)
	if descriptor == nil {
		return nil
	}
	if descriptor.Provider != "openai" || descriptor.CatalogModelID != deepSeekPublicModelID {
		return descriptor
	}
	if !deepSeekFlashUsesAnthropicLane(botUID) {
		return descriptor
	}
	override := *descriptor
	override.Provider = "anthropic"
	override.OpenAIAPIMode = ""
	return &override
}
