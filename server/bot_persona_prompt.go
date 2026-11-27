package server

import (
	"encoding/json"
	"regexp"
	"strings"
)

// The web editor stores choices as labelled lines, or wraps them with scene rules
// and boundaries. Ordinary hand-written system prompts are passed through verbatim.
var personaChoiceLine = regexp.MustCompile(`(?m)^(相处身份|怎么称呼我|性格|兴趣爱好|性别|虚拟身高|日常穿搭|回复优先项|回复模式|回复语气|回复篇幅|回答结构|互动习惯|常用语|信息不足时)(（自定义）)?：[^\s]`)

const personaConversationDefaults = `把下方设定用作平时的对话偏好。用户写明的频率、场景约定、行动前确认和权限边界仍须遵守。
需要招呼或回应对方时才自然使用称呼；接着聊、改稿和交付时可以直接接话。口头禅、兴趣和性格只在贴合话题时体现，不必逐项展示。
按用户这一次要做的事决定语气、长度和结构。代写内容按收件人和用途措辞，给可直接使用的成稿。追改时沿用上一轮的对象、事实和要求，只调整对方提到的部分。
能给有用答案就先答。缺少会改变结果的关键信息时才按用户偏好追问或标出待定内容。用户表示满意或结束时简短回应，无需追加建议或问题。`

func effectivePersonaPrompt(raw string) string {
	if !personaChoiceLine.MatchString(raw) {
		if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
			return raw
		}
		var form struct {
			Description *string            `json:"助手设定"`
			Scenes      *[]json.RawMessage `json:"场景约定"`
			Boundaries  *string            `json:"行为边界"`
		}
		if json.Unmarshal([]byte(raw), &form) != nil || form.Description == nil || form.Scenes == nil || form.Boundaries == nil {
			return raw
		}
	}
	return personaConversationDefaults + "\n\n" + raw
}
