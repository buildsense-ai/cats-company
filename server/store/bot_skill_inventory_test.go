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
		ReportedAt:        "2026-08-12T06:02:00.000000002Z",
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
			name: "legacy report cannot reset a sequenced runtime",
			incoming: types.BotSkillInventory{
				ObservedAt: later, ReportedAt: "2026-08-12T06:02:00.000000001Z", RuntimeInstanceID: "runtime-a",
			},
			want: false,
		},
		{
			name: "a replacement runtime wins by server receipt order",
			incoming: types.BotSkillInventory{
				ObservedAt: "2026-08-12T05:00:00Z", RuntimeInstanceID: "runtime-b", ReportSequence: 1,
			},
			want: true,
		},
		{
			name: "a delayed report from a replacement runtime still follows receipt order",
			incoming: types.BotSkillInventory{
				ObservedAt: later, ReportedAt: "2026-08-12T06:02:00.000000001Z", RuntimeInstanceID: "runtime-b", ReportSequence: 1,
			},
			want: false,
		},
		{
			name: "a legacy reporter follows server receipt order",
			incoming: types.BotSkillInventory{
				ObservedAt: earlier,
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

	t.Run("legacy same-runtime reports use server receipt time", func(t *testing.T) {
		legacyExisting := &types.BotSkillInventory{
			ReportedAt:        "2026-08-12T06:02:00.000000002Z",
			RuntimeInstanceID: "runtime-a",
		}
		olderReceipt := types.BotSkillInventory{
			ReportedAt:        "2026-08-12T06:02:00.000000001Z",
			RuntimeInstanceID: "runtime-a",
		}
		if ShouldReplaceBotSkillInventory(legacyExisting, olderReceipt) {
			t.Fatal("older legacy receipt replaced the newer inventory")
		}
		newerReceipt := types.BotSkillInventory{
			ReportedAt:        "2026-08-12T06:02:00.000000003Z",
			RuntimeInstanceID: "runtime-a",
		}
		if !ShouldReplaceBotSkillInventory(legacyExisting, newerReceipt) {
			t.Fatal("newer legacy receipt did not replace the older inventory")
		}
	})
}
