package types

import "testing"

func TestParseBotSkillMutationMode(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  BotSkillMutationMode
		ok    bool
	}{
		{name: "owner default", value: "owner_only", want: BotSkillMutationOwnerOnly, ok: true},
		{name: "shared live normalized", value: " SHARED_LIVE ", want: BotSkillMutationSharedLive, ok: true},
		{name: "empty", value: "", ok: false},
		{name: "unknown", value: "everyone", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseBotSkillMutationMode(tt.value)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ParseBotSkillMutationMode(%q)=(%q,%v), want (%q,%v)", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}
