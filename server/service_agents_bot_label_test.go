package server

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// bot-as-tool 的「标签」默认取 bot 自己的 description —— 用户原话：
// 「每个 bot 都有可能被分享出去，就相当于 bot 自己的标签或者描述分享，
//   要不然我自己的 bot 调用另一个 bot 怎么知道描述呢」。

type fakeBotProfiles struct {
	configs map[int64]*types.BotConfig
	err     error
}

func (f *fakeBotProfiles) GetBotConfig(uid int64) (*types.BotConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	if cfg, ok := f.configs[uid]; ok {
		return cfg, nil
	}
	return nil, errors.New("bot not found")
}

func TestPublishFillsDescriptionFromBotProfile(t *testing.T) {
	handler := &ServiceAgentPublishHandler{}
	handler.SetBotProfiles(&fakeBotProfiles{configs: map[int64]*types.BotConfig{
		982: {UserID: 982, Description: "我是杰克：负责云端运维与部署排障，遇到服务器/发布相关的事找我。"},
	}})

	definition := json.RawMessage(`{"runner":{"type":"bot","bot_uid":982}}`)
	filled := handler.fillDescriptionFromBot(982, definition)
	if len(filled) == 0 {
		t.Fatal("应该用 bot 描述兜底")
	}
	if got := serviceAgentDefinitionDescription(filled); got != "我是杰克：负责云端运维与部署排障，遇到服务器/发布相关的事找我。" {
		t.Fatalf("description = %q", got)
	}
	// runner 必须原样保留（发布是全量覆盖，丢字段会让应用起不来）。
	var view struct {
		Runner struct {
			Type   string `json:"type"`
			BotUID int64  `json:"bot_uid"`
		} `json:"runner"`
	}
	if err := json.Unmarshal(filled, &view); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if view.Runner.Type != "bot" || view.Runner.BotUID != 982 {
		t.Fatalf("runner 被改坏了: %+v", view.Runner)
	}
}

func TestPublishBotProfileFallbackSkipsWhenEmpty(t *testing.T) {
	cases := map[string]struct {
		handler *ServiceAgentPublishHandler
	}{
		"没有注入读取器": {&ServiceAgentPublishHandler{}},
		"bot 没有描述": {func() *ServiceAgentPublishHandler {
			h := &ServiceAgentPublishHandler{}
			h.SetBotProfiles(&fakeBotProfiles{configs: map[int64]*types.BotConfig{982: {UserID: 982}}})
			return h
		}()},
		"读取失败": {func() *ServiceAgentPublishHandler {
			h := &ServiceAgentPublishHandler{}
			h.SetBotProfiles(&fakeBotProfiles{err: errors.New("db down")})
			return h
		}()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.handler.fillDescriptionFromBot(982, json.RawMessage(`{"runner":{"type":"bot","bot_uid":982}}`)); got != nil {
				t.Fatalf("应该返回 nil（继续走「必须写描述」校验），got %s", got)
			}
		})
	}
}
