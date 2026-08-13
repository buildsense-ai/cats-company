package store

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestShouldReplaceBotSkillInventory(t *testing.T) {
	const (
		earlier = "2026-08-12T06:00:00Z"
		later   = "2026-08-12T06:01:00Z"
	)
	existing := &types.BotSkillInventory{
		ObservedAt:        later,
		RuntimeInstanceID: "runtime-a",
		ReportSequence:    2,
	}
	tests := []struct {
		name     string
		incoming types.BotSkillInventory
		want     bool
	}{
		{
			name: "newer sequence from the same runtime wins even with an earlier observation",
			incoming: types.BotSkillInventory{
				ObservedAt: earlier, RuntimeInstanceID: "runtime-a", ReportSequence: 3,
			},
			want: true,
		},
		{
			name: "older sequence from the same runtime cannot overwrite",
			incoming: types.BotSkillInventory{
				ObservedAt: later, RuntimeInstanceID: "runtime-a", ReportSequence: 1,
			},
			want: false,
		},
		{
			name: "a later observation from a replacement runtime wins",
			incoming: types.BotSkillInventory{
				ObservedAt: "2026-08-12T06:02:00Z", RuntimeInstanceID: "runtime-b", ReportSequence: 1,
			},
			want: true,
		},
		{
			name: "an older observation from a replacement runtime cannot overwrite",
			incoming: types.BotSkillInventory{
				ObservedAt: earlier, RuntimeInstanceID: "runtime-b", ReportSequence: 1,
			},
			want: false,
		},
		{
			name: "a legacy reporter remains ordered by observation time",
			incoming: types.BotSkillInventory{
				ObservedAt: "2026-08-12T06:02:00Z",
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldReplaceBotSkillInventory(existing, tc.incoming); got != tc.want {
				t.Fatalf("ShouldReplaceBotSkillInventory() = %v, want %v", got, tc.want)
			}
		})
	}
}
