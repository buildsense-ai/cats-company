package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestEncodeBotDefinitionJSONPreservesUnrelatedConfiguration(t *testing.T) {
	raw := []byte(`{"cloud_model":{"model_id":"minimax-m3"},"skills":{"keep":true}}`)
	record := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: "xiaoba.bot-definition.v1",
			BotID:  "43",
			Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
			Prompt: types.BotDefinitionPrompt{Selected: "default"},
		},
		Revision:  1,
		UpdatedAt: "2026-07-28T00:00:00Z",
	}
	next, err := EncodeBotDefinitionJSON(raw, record, &types.BotDefinitionApplyState{DesiredRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(next, &root); err != nil {
		t.Fatal(err)
	}
	if len(root["cloud_model"]) == 0 || len(root["skills"]) == 0 {
		t.Fatalf("unrelated keys were lost: %s", next)
	}
	decoded, apply, err := DecodeBotDefinitionJSON(next)
	if err != nil {
		t.Fatal(err)
	}
	if decoded == nil || decoded.Revision != 1 || decoded.Definition.BotID != "43" {
		t.Fatalf("unexpected decoded record: %#v", decoded)
	}
	if apply.DesiredRevision != 1 {
		t.Fatalf("unexpected apply state: %#v", apply)
	}
}

func TestBotDefinitionJSONReusesCloudModelAndDoesNotDuplicateCiphertext(t *testing.T) {
	raw := []byte(`{
		"cloud_model":{"kind":"custom","model_id":"old-model","custom_ciphertext":"v1:old","revision":4},
		"other":{"keep":true}
	}`)
	record := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: "xiaoba.bot-definition.v1",
			BotID:  "43",
			Model: types.BotDefinitionModel{
				Kind: "custom", Model: "new-model", ReasoningEffort: "high",
				APIKeyEncrypted: "v1:new",
			},
			SavedCustomModel: &types.BotDefinitionCustomModel{
				Kind: "custom", APIKeyEncrypted: "v1:new",
			},
			Prompt: types.BotDefinitionPrompt{
				Selected: "custom", CustomSystemPrompt: "portable prompt",
			},
		},
		Revision:  7,
		UpdatedAt: "2026-07-28T00:00:00Z",
	}
	next, err := EncodeBotDefinitionJSON(raw, record, &types.BotDefinitionApplyState{DesiredRevision: 7})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(next, &root); err != nil {
		t.Fatal(err)
	}
	if string(root["bot_definition"]) == "" ||
		string(root["bot_definition"]) == "null" {
		t.Fatalf("missing metadata: %s", next)
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal(root["bot_definition"], &metadata); err != nil {
		t.Fatal(err)
	}
	if _, duplicated := metadata["model"]; duplicated {
		t.Fatalf("model was duplicated into bot_definition: %s", root["bot_definition"])
	}
	if strings.Contains(string(root["bot_definition"]), "v1:new") {
		t.Fatalf("ciphertext was duplicated into metadata")
	}
	model, err := DecodeBotModelConfigJSON(next)
	if err != nil {
		t.Fatal(err)
	}
	if model.ModelID != "new-model" || model.CustomCiphertext != "v1:new" || model.Revision != 5 {
		t.Fatalf("unexpected shared model state: %#v", model)
	}
	decoded, _, err := DecodeBotDefinitionJSON(next)
	if err != nil {
		t.Fatal(err)
	}
	if decoded == nil || decoded.Definition.Prompt.CustomSystemPrompt != "portable prompt" ||
		decoded.Definition.Model.APIKeyEncrypted != "v1:new" {
		t.Fatalf("definition was not reconstructed: %#v", decoded)
	}
}

func TestPromptOnlyDefinitionWriteAndAckDoNotChangeLegacyModelState(t *testing.T) {
	raw := []byte(`{
		"cloud_model":{"kind":"catalog","model_id":"minimax-m3","revision":4,
			"applied_kind":"catalog","applied_model_id":"minimax-m3","applied_revision":4},
		"bot_definition":{
			"schema":"xiaoba.bot-definition.v1","botId":"43",
			"prompt":{"selected":"default"},"revision":6
		},
		"bot_definition_apply":{"desiredRevision":6}
	}`)
	record, apply, err := DecodeBotDefinitionJSON(raw)
	if err != nil || record == nil {
		t.Fatalf("decode failed: %#v %v", record, err)
	}
	record.Definition.Prompt = types.BotDefinitionPrompt{
		Selected: "custom", CustomSystemPrompt: "new prompt",
	}
	record.Revision = 7
	next, err := EncodeBotDefinitionJSON(raw, record, apply)
	if err != nil {
		t.Fatal(err)
	}
	model, err := DecodeBotModelConfigJSON(next)
	if err != nil {
		t.Fatal(err)
	}
	if model.Revision != 4 || model.ModelID != "minimax-m3" {
		t.Fatalf("prompt update changed model revision: %#v", model)
	}

	apply = &types.BotDefinitionApplyState{
		DesiredRevision: 7, AppliedRevision: 7,
		AppliedAt: "applied", LastAttemptAt: "attempted",
	}
	acked, err := EncodeBotDefinitionJSON(next, record, apply)
	if err != nil {
		t.Fatal(err)
	}
	model, err = DecodeBotModelConfigJSON(acked)
	if err != nil {
		t.Fatal(err)
	}
	if model.AppliedRevision != 4 || model.AppliedModelID != "minimax-m3" ||
		model.LastAttemptRevision != 0 {
		t.Fatalf("prompt-only Definition ACK polluted legacy model state: %#v", model)
	}
}

func TestCatalogPatchCanExplicitlyClearSavedCustomCredential(t *testing.T) {
	raw := []byte(`{
		"cloud_model":{"kind":"catalog","model_id":"minimax-m3","custom_ciphertext":"v1:saved","revision":4},
		"bot_definition":{
			"schema":"xiaoba.bot-definition.v1","botId":"43",
			"prompt":{"selected":"default"},"revision":6
		}
	}`)
	record, apply, err := DecodeBotDefinitionJSON(raw)
	if err != nil || record == nil {
		t.Fatalf("decode failed: %#v %v", record, err)
	}
	record.Definition.Model.ClearSavedCustom = true
	record.Definition.SavedCustomModel = nil
	record.Revision++
	next, err := EncodeBotDefinitionJSON(raw, record, apply)
	if err != nil {
		t.Fatal(err)
	}
	model, err := DecodeBotModelConfigJSON(next)
	if err != nil {
		t.Fatal(err)
	}
	if model.CustomCiphertext != "" {
		t.Fatalf("saved custom credential was not cleared: %#v", model)
	}
}

func TestDefinitionJSONPreservesUnknownFieldsInsideManagedNodes(t *testing.T) {
	raw := []byte(`{
		"cloud_model":{"kind":"catalog","model_id":"minimax-m3","revision":4,"provider_options":{"keep":true}},
		"bot_definition":{
			"schema":"xiaoba.bot-definition.v1","botId":"43",
			"prompt":{"selected":"default","futureTone":"warm"},"revision":6,"skills":{"keep":true}
		},
		"bot_definition_apply":{"desiredRevision":6,"future_status":"keep"}
	}`)
	record, apply, err := DecodeBotDefinitionJSON(raw)
	if err != nil || record == nil {
		t.Fatalf("decode failed: %#v %v", record, err)
	}
	record.Definition.Prompt = types.BotDefinitionPrompt{Selected: "custom", CustomSystemPrompt: "updated"}
	record.Revision++
	apply.DesiredRevision = record.Revision
	next, err := EncodeBotDefinitionJSON(raw, record, apply)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"provider_options", "skills", "future_status", "futureTone"} {
		if !strings.Contains(string(next), marker) {
			t.Fatalf("unknown nested field %q was lost: %s", marker, next)
		}
	}
}

func TestPromptOnlyFailedDefinitionAckDoesNotPolluteLegacyModelFailure(t *testing.T) {
	raw := []byte(`{
		"cloud_model":{"kind":"catalog","model_id":"minimax-m3","revision":4,
			"applied_kind":"catalog","applied_model_id":"minimax-m3","applied_revision":4},
		"bot_definition":{
			"schema":"xiaoba.bot-definition.v1","botId":"43",
			"prompt":{"selected":"default"},"revision":7
		}
	}`)
	record, _, err := DecodeBotDefinitionJSON(raw)
	if err != nil || record == nil {
		t.Fatalf("decode failed: %#v %v", record, err)
	}
	next, err := EncodeBotDefinitionJSON(raw, record, &types.BotDefinitionApplyState{
		DesiredRevision: 7, AppliedRevision: 7, AppliedAt: "old-success",
		LastAttemptAt: "failed-at", LastError: "connector failed", LastErrorRevision: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := DecodeBotModelConfigJSON(next)
	if err != nil {
		t.Fatal(err)
	}
	if model.LastAttemptRevision != 0 || model.LastAttemptAt != "" || model.LastError != "" {
		t.Fatalf("prompt-only failure polluted legacy model state: %#v", model)
	}
}

func TestModelChangingDefinitionFailureBridgesLegacyFailure(t *testing.T) {
	raw := []byte(`{
		"cloud_model":{"kind":"catalog","model_id":"minimax-m3","revision":4},
		"bot_definition":{
			"schema":"xiaoba.bot-definition.v1","botId":"43",
			"prompt":{"selected":"default"},"revision":7
		}
	}`)
	record, _, err := DecodeBotDefinitionJSON(raw)
	if err != nil || record == nil {
		t.Fatalf("decode failed: %#v %v", record, err)
	}
	next, err := EncodeBotDefinitionJSON(raw, record, &types.BotDefinitionApplyState{
		DesiredRevision: 7, LastAttemptAt: "failed-at",
		LastError: "connector failed", LastErrorRevision: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := DecodeBotModelConfigJSON(next)
	if err != nil {
		t.Fatal(err)
	}
	if model.LastAttemptRevision != 4 || model.LastAttemptAt != "failed-at" ||
		model.LastError != "connector failed" {
		t.Fatalf("model-changing Definition failure was not bridged: %#v", model)
	}
}
