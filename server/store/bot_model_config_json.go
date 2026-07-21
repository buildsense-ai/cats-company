package store

import (
	"encoding/json"
	"errors"

	"github.com/openchat/openchat/server/store/types"
)

const botModelConfigJSONKey = "cloud_model"

var ErrStaleBotModelRevision = errors.New("stale bot model revision")

// DecodeBotModelConfigJSON reads only the cloud model node while leaving the
// rest of bot_config.config available to other features.
func DecodeBotModelConfigJSON(raw []byte) (*types.BotModelConfig, error) {
	root := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, err
		}
	}
	config := &types.BotModelConfig{}
	if value := root[botModelConfigJSONKey]; len(value) > 0 {
		if err := json.Unmarshal(value, config); err != nil {
			return nil, err
		}
	}
	return config, nil
}

// EncodeBotModelConfigJSON replaces only the cloud model node and preserves
// unrelated bot configuration owned by other features.
func EncodeBotModelConfigJSON(raw []byte, config *types.BotModelConfig) ([]byte, error) {
	root := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, err
		}
	}
	value, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	root[botModelConfigJSONKey] = value
	return json.Marshal(root)
}
