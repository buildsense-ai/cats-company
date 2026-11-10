package store

import "github.com/openchat/openchat/server/store/types"

// RecordBotSkillAdditions runs under the BotDefinition row lock. Only a newly
// present Skill receives a date; legacy inventory stays unknown, version
// updates retain the date, and removal/readdition starts a new date.
func RecordBotSkillAdditions(record *types.BotDefinitionRecord, previous []types.BotSkillRef, now string) {
	previousIDs := make(map[string]bool, len(previous))
	for _, skill := range previous {
		previousIDs[skill.SkillID] = true
	}
	next := make(map[string]string)
	for _, skill := range record.Definition.Skills {
		if !previousIDs[skill.SkillID] {
			next[skill.SkillID] = now
		} else if date := record.SkillAddedAt[skill.SkillID]; date != "" {
			next[skill.SkillID] = date
		}
	}
	record.SkillAddedAt = next
}
