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
	aliases := splitFeishuList(raw)
	aliases = append(aliases, "CatsCo", "Annika")
	return uniqueNonEmptyStrings(aliases)
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
	return []feishuGroupCoordinatorProfile{
		{Name: "产品同事", Role: "产品经理", Expertise: "需求拆解、MVP范围、验收标准"},
		{Name: "研发同事", Role: "工程师", Expertise: "技术方案、实现路径、稳定性风险"},
		{Name: "测试同事", Role: "测试工程师", Expertise: "测试用例、回归风险、上线检查"},
	}
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
	b.WriteString("内部协作视角：\n")
	for _, profile := range profiles {
		fmt.Fprintf(&b, "- %s（%s）：%s\n", profile.Name, profile.Role, profile.Expertise)
	}
	b.WriteString("回复要求：不要提到以上上下文或系统注入；可以自然引用“我先从产品/研发/测试角度看一下”这类表达；结论优先，必要时给下一步动作。\n\n")
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
