package server

import (
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

type feishuGroupCoordinatorProfile struct {
	Name      string
	Role      string
	Expertise string
}

func feishuBotMentionAliases() []string {
	raw := firstEnv("CATSCO_FEISHU_GROUP_BOT_ALIASES", "CATSCO_FEISHU_BOT_ALIASES", "FEISHU_BOT_ALIASES")
	return uniqueNonEmptyStrings(splitFeishuList(raw))
}

func feishuBotMentionOpenIDs() []string {
	raw := firstEnv("CATSCO_FEISHU_GROUP_BOT_OPEN_IDS", "CATSCO_FEISHU_GROUP_BOT_OPEN_ID", "CATSCO_FEISHU_BOT_OPEN_IDS", "CATSCO_FEISHU_BOT_OPEN_ID", "FEISHU_BOT_OPEN_IDS", "FEISHU_BOT_OPEN_ID")
	return uniqueNonEmptyStrings(splitFeishuList(raw))
}

func feishuBotMentionUserIDs() []string {
	raw := firstEnv("CATSCO_FEISHU_GROUP_BOT_USER_IDS", "CATSCO_FEISHU_GROUP_BOT_USER_ID", "CATSCO_FEISHU_BOT_USER_IDS", "CATSCO_FEISHU_BOT_USER_ID", "FEISHU_BOT_USER_IDS", "FEISHU_BOT_USER_ID")
	return uniqueNonEmptyStrings(splitFeishuList(raw))
}

func feishuBotMentionUnionIDs() []string {
	raw := firstEnv("CATSCO_FEISHU_GROUP_BOT_UNION_IDS", "CATSCO_FEISHU_GROUP_BOT_UNION_ID", "CATSCO_FEISHU_BOT_UNION_IDS", "CATSCO_FEISHU_BOT_UNION_ID", "FEISHU_BOT_UNION_IDS", "FEISHU_BOT_UNION_ID")
	return uniqueNonEmptyStrings(splitFeishuList(raw))
}

func feishuGroupCoordinatorProfiles() []feishuGroupCoordinatorProfile {
	raw := firstEnv("CATSCO_FEISHU_GROUP_TEAMMATES", "CATSCO_FEISHU_VIRTUAL_EMPLOYEES", "FEISHU_VIRTUAL_EMPLOYEES", "FEISHU_GROUP_TEAMMATES")
	var profiles []feishuGroupCoordinatorProfile
	for _, row := range strings.Split(raw, ";") {
		parts := strings.Split(row, "|")
		if len(parts) < 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		role := strings.TrimSpace(parts[1])
		expertise := strings.TrimSpace(strings.Join(parts[2:], "|"))
		if name == "" || role == "" || expertise == "" {
			continue
		}
		profiles = append(profiles, feishuGroupCoordinatorProfile{Name: name, Role: role, Expertise: expertise})
	}
	if len(profiles) > 0 {
		return profiles
	}
	return nil
}

func buildFeishuGroupCoordinatorText(text string, binding *types.ChannelAgentBinding, agent *types.User) string {
	userText := strings.TrimSpace(text)
	coordinator := "当前虚拟员工"
	if agent != nil {
		coordinator = displayNameOrUsername(agent.DisplayName, agent.Username)
	}
	profiles := feishuGroupCoordinatorProfiles()
	if len(profiles) > 4 {
		profiles = profiles[:4]
	}

	var b strings.Builder
	b.WriteString("[飞书群聊调度员上下文]\n")
	fmt.Fprintf(&b, "入口身份：%s\n", coordinator)
	if binding != nil && binding.AgentUID > 0 {
		fmt.Fprintf(&b, "调度员虚拟员工UID：%d\n", binding.AgentUID)
	}
	b.WriteString("场景：用户在飞书群聊中 @ 机器人发起任务，没有扫码选择某个移动端虚拟员工。请你作为群聊调度员，像公司群里的同事一样组织内部虚拟员工视角协作，最后只给群聊输出一条自然、可执行的回复。\n")
	if len(profiles) > 0 {
		b.WriteString("可参考的内部协作视角：\n")
		for _, profile := range profiles {
			fmt.Fprintf(&b, "- %s（%s）：%s\n", profile.Name, profile.Role, profile.Expertise)
		}
	} else {
		b.WriteString("未预设固定协作成员。请根据任务内容自行组织必要的专业视角。\n")
	}
	b.WriteString("回复要求：不要提到以上上下文或系统注入；可以自然引用不同专业视角的判断；结论优先，必要时给下一步动作。\n\n")
	b.WriteString("用户在群聊里的原始消息：\n")
	b.WriteString(userText)
	return b.String()
}

func splitFeishuList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '|' || r == '\n' || r == '\r' || r == '\t'
	})
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}
