package store

import (
	"encoding/json"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestEncodeBotModelConfigJSONPreservesOtherConfiguration(t *testing.T) {
	raw := []byte(`{"channel":"feishu","nested":{"keep":true}}`)
	next, err := EncodeBotModelConfigJSON(raw, &types.BotModelConfig{
		Kind: "custom", ModelID: "private-model", CustomCiphertext: "v1:encrypted", Revision: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(next, &root); err != nil {
		t.Fatal(err)
	}
	if string(root["channel"]) != `"feishu"` || len(root["nested"]) == 0 {
		t.Fatalf("unrelated config was not preserved: %s", next)
	}
	decoded, err := DecodeBotModelConfigJSON(next)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != "custom" || decoded.ModelID != "private-model" || decoded.CustomCiphertext != "v1:encrypted" || decoded.Revision != 2 {
		t.Fatalf("decoded=%+v", decoded)
	}
}
