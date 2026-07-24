package store

import (
	"encoding/json"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestBotDefinitionJSONDistinguishesMissingAndEmptySkills(t *testing.T) {
	missing, err := DecodeBotDefinitionJSON([]byte(`{"cloud_model":{"revision":4}}`))
	if err != nil {
		t.Fatal(err)
	}
	if missing.Skills != nil || missing.Model == nil || missing.Model.Revision != 4 {
		t.Fatalf("missing snapshot=%+v", missing)
	}

	raw, err := EncodeBotDefinitionJSON([]byte(`{"cloud_model":{"revision":4}}`), &types.BotDefinitionSkillsState{
		Schema: BotDefinitionSchema, Skills: []types.BotSkillRef{}, Revision: 1, UpdatedAt: "2026-07-24T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := DecodeBotDefinitionJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Skills == nil || empty.Skills.Skills == nil || len(empty.Skills.Skills) != 0 {
		t.Fatalf("explicit empty skills were lost: %+v", empty)
	}
}

func TestEncodeBotDefinitionJSONPreservesModelAndUnrelatedConfiguration(t *testing.T) {
	raw := []byte(`{"channel":"feishu","cloud_model":{"kind":"catalog","model_id":"minimax-m3","revision":7}}`)
	next, err := EncodeBotDefinitionJSON(raw, &types.BotDefinitionSkillsState{
		Schema:   BotDefinitionSchema,
		Skills:   []types.BotSkillRef{{SkillID: "lin/agent-browser", Version: "1.0.3"}},
		Revision: 2, UpdatedAt: "2026-07-24T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(next, &root); err != nil {
		t.Fatal(err)
	}
	if string(root["channel"]) != `"feishu"` || len(root["cloud_model"]) == 0 || len(root["bot_definition"]) == 0 {
		t.Fatalf("configuration was overwritten: %s", next)
	}
	decoded, err := DecodeBotDefinitionJSON(next)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Model.Revision != 7 || decoded.Model.ModelID != "minimax-m3" ||
		decoded.Skills.Revision != 2 || len(decoded.Skills.Skills) != 1 {
		t.Fatalf("decoded=%+v", decoded)
	}
}

func TestDecodeBotDefinitionJSONRejectsAmbiguousStoredState(t *testing.T) {
	for name, raw := range map[string]string{
		"missing skills":  `{"bot_definition":{"schema":"xiaoba.bot-definition.v1","revision":1,"updatedAt":"2026-07-24T00:00:00Z"}}`,
		"null skills":     `{"bot_definition":{"schema":"xiaoba.bot-definition.v1","skills":null,"revision":1,"updatedAt":"2026-07-24T00:00:00Z"}}`,
		"wrong schema":    `{"bot_definition":{"schema":"wrong","skills":[],"revision":1,"updatedAt":"2026-07-24T00:00:00Z"}}`,
		"zero revision":   `{"bot_definition":{"schema":"xiaoba.bot-definition.v1","skills":[],"revision":0,"updatedAt":"2026-07-24T00:00:00Z"}}`,
		"missing updated": `{"bot_definition":{"schema":"xiaoba.bot-definition.v1","skills":[],"revision":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeBotDefinitionJSON([]byte(raw)); err == nil {
				t.Fatal("expected invalid stored state to fail")
			}
		})
	}
}
