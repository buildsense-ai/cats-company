package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type feishuGroupCoordinatorProfile struct {
	Name      string
	Role      string
	Expertise string
}

type feishuGroupAgentSpeaker struct {
	ChatID     string
	AgentUID   int64
	WebhookURL string
	Secret     string
}

const (
	feishuGroupDiscussionMode               = "multi_agent_discussion"
	feishuGroupDiscussionIDMetadataKey      = "channel_group_discussion_id"
	feishuGroupDiscussionTurnMetadataKey    = "channel_group_discussion_turn_index"
	feishuGroupDiscussionSpeakerMetadataKey = "channel_group_speaker_agent_uid"
)

type feishuGroupDiscussionParticipant struct {
	AgentUID  int64
	AgentName string
	Binding   *types.ChannelAgentBinding
	Speaker   feishuGroupAgentSpeaker
}

type feishuGroupDiscussionReply struct {
	AgentUID  int64
	AgentName string
	Text      string
}

type feishuGroupDiscussionSession struct {
	ID              string
	AppID           string
	ChannelUserID   string
	ActorUID        int64
	ChatID          string
	MessageID       string
	MessageType     string
	OriginalText    string
	CoordinatorName string
	Participants    []feishuGroupDiscussionParticipant
	Replies         []feishuGroupDiscussionReply
	NextIndex       int
	CompletedTurns  map[int]bool
	ExpiresAt       time.Time
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

func feishuGroupAgentSpeakers() []feishuGroupAgentSpeaker {
	raw := firstEnv("CATSCO_FEISHU_GROUP_AGENT_WEBHOOKS", "CATSCO_FEISHU_GROUP_SPEAKER_WEBHOOKS", "FEISHU_GROUP_AGENT_WEBHOOKS", "FEISHU_GROUP_SPEAKER_WEBHOOKS")
	return parseFeishuGroupAgentSpeakers(raw)
}

func feishuGroupAgentSpeakersForChat(chatID string) []feishuGroupAgentSpeaker {
	chatID = strings.TrimSpace(chatID)
	var out []feishuGroupAgentSpeaker
	for _, speaker := range feishuGroupAgentSpeakers() {
		if speaker.AgentUID <= 0 || strings.TrimSpace(speaker.WebhookURL) == "" {
			continue
		}
		if strings.EqualFold(speaker.ChatID, chatID) {
			out = append(out, speaker)
		}
	}
	return out
}

func feishuGroupAgentSpeakerFor(chatID string, agentUID int64) (feishuGroupAgentSpeaker, bool) {
	chatID = strings.TrimSpace(chatID)
	for _, speaker := range feishuGroupAgentSpeakers() {
		if speaker.AgentUID != agentUID || strings.TrimSpace(speaker.WebhookURL) == "" {
			continue
		}
		if strings.EqualFold(speaker.ChatID, chatID) {
			return speaker, true
		}
	}
	return feishuGroupAgentSpeaker{}, false
}

func parseFeishuGroupAgentSpeakers(raw string) []feishuGroupAgentSpeaker {
	rows := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ';' || r == '；'
	})
	out := make([]feishuGroupAgentSpeaker, 0, len(rows))
	for _, row := range rows {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		parts := strings.Split(row, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) < 3 {
			continue
		}
		agentUID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || agentUID <= 0 {
			continue
		}
		speaker := feishuGroupAgentSpeaker{
			ChatID:     parts[0],
			AgentUID:   agentUID,
			WebhookURL: parts[2],
		}
		if len(parts) > 3 {
			speaker.Secret = strings.Join(parts[3:], "|")
		}
		if speaker.ChatID == "" || strings.TrimSpace(speaker.WebhookURL) == "" {
			continue
		}
		out = append(out, speaker)
	}
	return out
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

func buildFeishuGroupAgentTurnText(text string, participant feishuGroupDiscussionParticipant, coordinatorName string, participants []feishuGroupDiscussionParticipant, previous []feishuGroupDiscussionReply, index, total int) string {
	userText := strings.TrimSpace(text)
	agentName := strings.TrimSpace(participant.AgentName)
	if agentName == "" {
		agentName = "当前虚拟员工"
	}

	var b strings.Builder
	b.WriteString("[飞书群聊多机器人协作上下文]\n")
	fmt.Fprintf(&b, "你的群聊发言身份：%s\n", agentName)
	if coordinatorName != "" {
		fmt.Fprintf(&b, "调度员：%s\n", coordinatorName)
	}
	if total > 0 {
		fmt.Fprintf(&b, "本轮协作顺序：第 %d/%d 位发言。\n", index, total)
	}
	if len(participants) > 0 {
		b.WriteString("本轮参与的虚拟员工：")
		for i, participant := range participants {
			if i > 0 {
				b.WriteString("、")
			}
			name := strings.TrimSpace(participant.AgentName)
			if name == "" {
				name = "虚拟员工"
			}
			b.WriteString(name)
		}
		b.WriteString("\n")
	}
	if len(previous) > 0 {
		b.WriteString("群聊里前面已经发出的同事观点：\n")
		for _, reply := range previous {
			name := strings.TrimSpace(reply.AgentName)
			if name == "" {
				name = "虚拟员工"
			}
			fmt.Fprintf(&b, "- %s：%s\n", name, strings.TrimSpace(reply.Text))
		}
	}
	b.WriteString("场景：用户在飞书群聊中 @ 调度员发起任务。你会以独立机器人身份在飞书群里真实发言。请只输出你这一位虚拟员工应该发到群里的内容。\n")
	b.WriteString("回复要求：先阅读前面同事的观点，只补充你有增量的判断、风险或下一步建议；如果前面已经覆盖充分，可以简短说明你没有额外补充；2-6 句为宜；像公司群里的同事一样自然发言；不要提到系统上下文、prompt、webhook 或内部配置；不要等待别人回复，也不要模拟其他人的发言。\n\n")
	b.WriteString("用户在群聊里的原始消息：\n")
	b.WriteString(userText)
	return b.String()
}

func sendFeishuCustomBotText(ctx context.Context, speaker feishuGroupAgentSpeaker, text string) error {
	webhookURL := strings.TrimSpace(speaker.WebhookURL)
	text = strings.TrimSpace(text)
	if webhookURL == "" || text == "" {
		return nil
	}
	body := map[string]interface{}{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	if strings.TrimSpace(speaker.Secret) != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		body["timestamp"] = timestamp
		body["sign"] = feishuCustomBotSign(timestamp, speaker.Secret)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu custom bot webhook http %d", resp.StatusCode)
	}
	return nil
}

func feishuCustomBotSign(timestamp string, secret string) string {
	stringToSign := timestamp + "\n" + strings.TrimSpace(secret)
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
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
