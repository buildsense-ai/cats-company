package store

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestShouldReplaceBotDefaultPrompt(t *testing.T) {
	current := &types.BotDefaultPromptSnapshot{XiaoBaVersion: "1.5.0"}
	for _, test := range []struct {
		name     string
		version  string
		expected bool
	}{
		{name: "older", version: "1.4.9", expected: false},
		{name: "older prerelease", version: "1.5.0-rc.1", expected: false},
		{name: "large prerelease", version: "1.5.0-rc.184467440737095516160", expected: false},
		{name: "equal", version: "v1.5.0+build.2", expected: true},
		{name: "newer", version: "2.0.0", expected: true},
		{name: "unknown", version: "development", expected: true},
		{name: "non ASCII", version: "1.5.0-预览", expected: true},
		{name: "missing", version: "", expected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			incoming := types.BotDefaultPromptSnapshot{XiaoBaVersion: test.version}
			if actual := ShouldReplaceBotDefaultPrompt(current, incoming); actual != test.expected {
				t.Fatalf("ShouldReplaceBotDefaultPrompt(%q)=%v want %v", test.version, actual, test.expected)
			}
		})
	}
}
