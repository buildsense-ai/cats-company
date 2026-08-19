package store

import (
	"errors"

	"github.com/openchat/openchat/server/store/types"
)

const botModelConfigJSONKey = "cloud_model"

var ErrStaleBotModelRevision = errors.New("stale bot model revision")

// DecodeBotModelConfigJSON reads only the cloud model node while leaving the
// rest of bot_config.config available to other features.
func DecodeBotModelConfigJSON(raw []byte, botUID ...int64) (*types.BotModelConfig, error) {
	uid := int64(0)
	if len(botUID) > 0 {
		uid = botUID[0]
	}
	record, err := DecodeBotDefinitionJSON(raw, uid)
	if err != nil {
		return nil, err
	}
	return legacyModelConfigFromRecord(record), nil
}

// EncodeBotModelConfigJSON replaces only the cloud model node and preserves
// unrelated bot configuration owned by other features.
func EncodeBotModelConfigJSON(raw []byte, config *types.BotModelConfig, botUID ...int64) ([]byte, error) {
	uid := int64(0)
	if len(botUID) > 0 {
		uid = botUID[0]
	}
	record, err := DecodeBotDefinitionJSON(raw, uid)
	if err != nil {
		return nil, err
	}
	applyLegacyModelConfig(record, config)
	return EncodeBotDefinitionJSON(raw, record)
}
