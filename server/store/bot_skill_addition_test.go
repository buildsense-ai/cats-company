package store

import (
	"reflect"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestBotSkillAdditionDatesSurvivePersistenceAndVersionUpdates(t *testing.T) {
	legacy := types.BotSkillRef{SkillID: "legacy", Version: "1"}
	added := types.BotSkillRef{SkillID: "new", Version: "1"}
	record := &types.BotDefinitionRecord{Definition: types.BotDefinition{Skills: []types.BotSkillRef{legacy, added}}}
	const first = "2026-11-10T06:00:00.123456789Z"
	RecordBotSkillAdditions(record, []types.BotSkillRef{legacy}, first)
	if !reflect.DeepEqual(record.SkillAddedAt, map[string]string{"new": first}) {
		t.Fatalf("invented legacy date or missed addition: %v", record.SkillAddedAt)
	}
	raw, err := EncodeBotDefinitionJSON([]byte(`{"unrelated":true}`), record)
	if err != nil {
		t.Fatal(err)
	}
	record, err = DecodeBotDefinitionJSON(raw, 42)
	if err != nil {
		t.Fatal(err)
	}
	previous := append([]types.BotSkillRef(nil), record.Definition.Skills...)
	record.Definition.Skills[1].Version = "2"
	RecordBotSkillAdditions(record, previous, "2026-11-11T06:00:00Z")
	if !reflect.DeepEqual(record.SkillAddedAt, map[string]string{"new": first}) {
		t.Fatalf("update changed original addition: %v", record.SkillAddedAt)
	}
	previous = record.Definition.Skills
	record.Definition.Skills = []types.BotSkillRef{legacy}
	RecordBotSkillAdditions(record, previous, "2026-11-12T06:00:00Z")
	if len(record.SkillAddedAt) != 0 {
		t.Fatal("removal retained date")
	}
	previous = record.Definition.Skills
	record.Definition.Skills = append([]types.BotSkillRef{added}, previous...)
	const readded = "2026-11-13T06:00:00Z"
	RecordBotSkillAdditions(record, previous, readded)
	if record.SkillAddedAt["new"] != readded {
		t.Fatal("readdition did not reset date")
	}
}
