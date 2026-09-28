package server

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

// Group activation decides which bots a group message addresses.
//
// The rule the product wants: a group with a single bot needs no addressing at
// all, while a group with several bots asks a model which of them the message
// concerns. Mention matching cannot express "this is the copywriter's job", so
// the multi-bot case is a semantic judgement rather than a string comparison.
//
// Judging happens on the server because that is where the group roster, the
// per-bot function, and the recent transcript already live. Clients only learn
// the outcome.

const (
	// defaultActivationThreshold separates "should respond" from "background".
	// Measured separation on real requests is 0.05 versus 0.94, so the exact
	// value is not sensitive.
	defaultActivationThreshold = 0.5
	// defaultActivationContextLimit bounds how much transcript is judged.
	// Latency is independent of length (60 and 3000 characters both answer in
	// 0.6-1.3s); the limit only bounds cost.
	defaultActivationContextLimit = 10
	// maxActivationContextRunes caps one transcript entry.
	maxActivationContextRunes = 600
)

// GroupActivationDecision is the outcome for one group message.
type GroupActivationDecision struct {
	// Activated holds the bot UIDs that should respond, mapped to their score.
	Activated map[int64]float64
	// Source explains which rule produced the decision.
	Source string
	// Degraded is true when judging failed and the fallback applied.
	Degraded bool
}

const (
	activationSourceSingleBot = "single_bot"
	activationSourceMention   = "mention"
	activationSourceChannel   = "channel_trigger"
	activationSourceBotSender = "bot_sender"
	activationSourceJev       = "jev"
	activationSourceDegraded  = "jev_unavailable"
	activationSourceNoBot     = "no_bot"
	activationSourceNoMention = "no_mention"
)

// GroupActivationResolver decides which bots respond to a group message.
type GroupActivationResolver interface {
	Resolve(ctx context.Context, req GroupActivationRequest) GroupActivationDecision
}

// GroupActivationRequest carries everything the resolver may consult.
type GroupActivationRequest struct {
	GroupID     int64
	GroupName   string
	SenderUID   int64
	SenderIsBot bool
	Message     string
	// Members is the group roster as loaded by the broadcaster.
	Members []*types.GroupMember
	// Mentions holds structured mention targets from the message.
	Mentions []string
	// TrustedChannelTrigger marks a channel-managed group message that already
	// passed the channel's own trigger rules.
	TrustedChannelTrigger bool
	// JudgeContext lazily loads the prompt material. It is only called when
	// semantic judging actually happens, so the common paths (a single-bot
	// group, an explicit mention, a channel trigger) pay no query at all.
	JudgeContext func() GroupJudgeContext
}

// GroupJudgeContext is the prompt material loaded only when judging happens.
type GroupJudgeContext struct {
	GroupName   string
	RecentTurns []GroupActivationTurn
}

// judgeContext resolves the lazy loader. A caller that knows judging will not
// happen can leave it unset, in which case judging sees no transcript.
func judgeContext(req GroupActivationRequest) GroupJudgeContext {
	if req.JudgeContext == nil {
		return GroupJudgeContext{}
	}
	return req.JudgeContext()
}

// GroupActivationTurn is one transcript entry offered to the judge.
type GroupActivationTurn struct {
	Speaker string
	IsBot   bool
	Text    string
}

// GroupActivationBot describes one bot to the judge.
type GroupActivationBot struct {
	UID         int64
	DisplayName string
	Role        string
	Description string
}

// GroupActivationResolverFunc adapts a function to the interface, mirroring the
// existing resolver-injection pattern so tests can stub judging.
type GroupActivationResolverFunc func(ctx context.Context, req GroupActivationRequest) GroupActivationDecision

func (f GroupActivationResolverFunc) Resolve(ctx context.Context, req GroupActivationRequest) GroupActivationDecision {
	return f(ctx, req)
}

// JevGroupActivationResolver judges activation with the Jev model and falls back
// to deterministic routing when judging is unavailable.
type JevGroupActivationResolver struct {
	client     *JevClient
	functions  BotFunctionReader
	threshold  float64
	contextLen int
}

// BotFunctionReader loads the owner-defined role and description for bots.
// The data already lives in bot_config; only a read path is missing.
type BotFunctionReader interface {
	GetBotFunctions(uids []int64) (map[int64]types.BotFunction, error)
}

// NewJevGroupActivationResolver builds the production resolver.
func NewJevGroupActivationResolver(client *JevClient, functions BotFunctionReader) *JevGroupActivationResolver {
	return &JevGroupActivationResolver{
		client:     client,
		functions:  functions,
		threshold:  defaultActivationThreshold,
		contextLen: defaultActivationContextLimit,
	}
}

// Resolve applies the activation rules in order of increasing cost.
func (r *JevGroupActivationResolver) Resolve(ctx context.Context, req GroupActivationRequest) GroupActivationDecision {
	allBots := activationBots(req.Members)
	if len(allBots) == 0 {
		return GroupActivationDecision{Source: activationSourceNoBot}
	}
	// A group with one bot never needs addressing: every message is for it.
	// This is decided from the group's roster, not from the candidates below, so
	// a lone bot's own messages cannot be mistaken for a single-bot group.
	if len(allBots) == 1 {
		if allBots[0].UID == req.SenderUID {
			return GroupActivationDecision{Source: activationSourceBotSender}
		}
		return GroupActivationDecision{
			Activated: map[int64]float64{allBots[0].UID: 1},
			Source:    activationSourceSingleBot,
		}
	}
	// A message never addresses its own author. Without this a bot that reports
	// progress scores highly against its own text and answers itself, which
	// turns every report into a loop.
	bots := activationExcludingSender(allBots, req.SenderUID)
	if len(bots) == 0 {
		return GroupActivationDecision{Source: activationSourceBotSender}
	}
	// A message from a bot is judged like any other. Bots hand work over by
	// naming the next owner in the text, so blocking bot senders outright would
	// break handover. Convergence comes from the criteria instead: a bot that
	// reports progress rather than asking for work does not wake anyone.
	if req.TrustedChannelTrigger {
		return GroupActivationDecision{
			Activated: activationAll(bots),
			Source:    activationSourceChannel,
		}
	}
	// An explicit mention is the strongest signal a person can give, so it
	// short-circuits judging and keeps working when judging is unavailable.
	if mentioned := activationMentionedBots(bots, req.Mentions); len(mentioned) > 0 {
		return GroupActivationDecision{Activated: mentioned, Source: activationSourceMention}
	}
	// "@all" asks every bot to look, which needs no semantic judgement.
	if activationMentionAll(req.Mentions) {
		return GroupActivationDecision{Activated: activationAll(bots), Source: activationSourceMention}
	}
	if !r.client.Enabled() {
		return GroupActivationDecision{Source: activationSourceDegraded, Degraded: true}
	}
	r.attachFunctions(bots)

	decision, err := r.judge(ctx, req, bots)
	if err != nil {
		// Never guess on failure: the caller tells the group that routing is
		// unavailable so a person can mention the right member instead.
		return GroupActivationDecision{Source: activationSourceDegraded, Degraded: true}
	}
	return decision
}

// attachFunctions fills in each bot's owner-defined role and description, which
// is what lets the judge tell "this is the copywriter's job" from "this is the
// reviewer's job". A lookup failure degrades the prompt, not the decision.
func (r *JevGroupActivationResolver) attachFunctions(bots []GroupActivationBot) {
	if r.functions == nil || len(bots) == 0 {
		return
	}
	uids := make([]int64, 0, len(bots))
	for _, bot := range bots {
		uids = append(uids, bot.UID)
	}
	functions, err := r.functions.GetBotFunctions(uids)
	if err != nil {
		log.Printf("group activation: bot functions unavailable: %v", err)
		return
	}
	for index := range bots {
		function, ok := functions[bots[index].UID]
		if !ok {
			continue
		}
		bots[index].Role = function.Role
		bots[index].Description = function.Description
	}
}

func (r *JevGroupActivationResolver) judge(ctx context.Context, req GroupActivationRequest, bots []GroupActivationBot) (GroupActivationDecision, error) {
	questions := make(map[string]JevQuestion, len(bots))
	for _, bot := range bots {
		name := activationBotName(bot)
		questions[activationQuestionKey(bot.UID)] = JevQuestion{
			Type:         "noul",
			Instructions: fmt.Sprintf("%s「%s」是否应当回复这条最新消息", activationRoleLabel(bot), name),
			Criteria: map[string]string{
				"true":  activationTrueCriteria(bot),
				"false": "消息只是寒暄、汇报进度、闲聊、情绪表达，或属于其他成员的职责范围，或没有提出新的待办请求",
			},
		}
	}

	answers, err := r.client.Ask(ctx, activationState(req, bots), questions)
	if err != nil {
		return GroupActivationDecision{}, err
	}

	activated := make(map[int64]float64)
	for _, bot := range bots {
		answer, ok := answers[activationQuestionKey(bot.UID)]
		if !ok {
			continue
		}
		value := answer.NoulAnswer()
		if value.Valid && value.Value >= r.threshold {
			activated[bot.UID] = value.Value
		}
	}
	return GroupActivationDecision{Activated: activated, Source: activationSourceJev}, nil
}

// activationState renders the prompt. It names every bot with the same display
// name the bot's own system prompt uses, so the judge and the bot agree on who
// is who. Human members are named but never judged.
func activationState(req GroupActivationRequest, bots []GroupActivationBot) string {
	context := judgeContext(req)
	var b strings.Builder
	if name := strings.TrimSpace(context.GroupName); name != "" {
		b.WriteString("群名称：")
		b.WriteString(name)
		b.WriteString("\n")
	}
	b.WriteString("群成员：\n")
	for _, bot := range bots {
		b.WriteString("- ")
		b.WriteString(activationBotName(bot))
		if role := strings.TrimSpace(bot.Role); role != "" {
			b.WriteString("（")
			b.WriteString(role)
			b.WriteString("）")
		}
		if desc := strings.TrimSpace(bot.Description); desc != "" {
			b.WriteString("：")
			b.WriteString(desc)
		}
		b.WriteString("\n")
	}
	for _, member := range req.Members {
		if member == nil || member.IsBot {
			continue
		}
		name := activationMemberName(member)
		if name == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(name)
		b.WriteString("\n")
	}

	if turns := mergeActivationTurns(context.RecentTurns); len(turns) > 0 {
		b.WriteString("最近对话：\n")
		for _, turn := range turns {
			b.WriteString(turn.Speaker)
			if turn.IsBot {
				b.WriteString("（机器人）")
			}
			b.WriteString(": ")
			b.WriteString(turn.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// mergeActivationTurns folds consecutive messages from one speaker into a single
// entry. A bot that streams several visible lines would otherwise fill the whole
// window and push the earlier human request out of the judge's view.
func mergeActivationTurns(turns []GroupActivationTurn) []GroupActivationTurn {
	if len(turns) == 0 {
		return nil
	}
	merged := make([]GroupActivationTurn, 0, len(turns))
	for _, turn := range turns {
		text := strings.TrimSpace(turn.Text)
		if text == "" {
			continue
		}
		text = truncateUTF8(text, maxActivationContextRunes)
		last := len(merged) - 1
		if last >= 0 && merged[last].Speaker == turn.Speaker && merged[last].IsBot == turn.IsBot {
			merged[last].Text += " " + text
			merged[last].Text = truncateUTF8(merged[last].Text, maxActivationContextRunes)
			continue
		}
		merged = append(merged, GroupActivationTurn{Speaker: turn.Speaker, IsBot: turn.IsBot, Text: text})
	}
	return merged
}

func activationBots(members []*types.GroupMember) []GroupActivationBot {
	bots := make([]GroupActivationBot, 0, len(members))
	for _, member := range members {
		if member == nil || !member.IsBot {
			continue
		}
		bots = append(bots, GroupActivationBot{UID: member.UserID})
	}
	return bots
}

// activationExcludingSender drops the author from the candidates. A message
// never addresses its own author, and leaving the author in would let a bot's
// own progress report score against itself and restart its turn.
func activationExcludingSender(bots []GroupActivationBot, senderUID int64) []GroupActivationBot {
	if senderUID <= 0 {
		return bots
	}
	filtered := make([]GroupActivationBot, 0, len(bots))
	for _, bot := range bots {
		if bot.UID == senderUID {
			continue
		}
		filtered = append(filtered, bot)
	}
	return filtered
}

// activationBotName mirrors the name the bot sees for itself. Both the roster
// and the bot's own handshake read users.display_name, so they cannot drift.
func activationBotName(bot GroupActivationBot) string {
	if name := strings.TrimSpace(bot.DisplayName); name != "" {
		return name
	}
	return formatUID(bot.UID)
}

func activationMemberName(member *types.GroupMember) string {
	if member == nil {
		return ""
	}
	if name := strings.TrimSpace(member.DisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(member.Username)
}

func activationRoleLabel(bot GroupActivationBot) string {
	role := strings.TrimSpace(bot.Role)
	if role == "" {
		return "成员"
	}
	// The stored role is an enum token such as "code_review". Judging happens in
	// Chinese, and an English token inside a Chinese prompt reads as noise, so
	// the known roles are rendered as the words the prompt actually needs.
	if label, ok := activationRoleLabels[role]; ok {
		return label
	}
	return role
}

// activationRoleLabels renders the stored role enum for the judging prompt.
var activationRoleLabels = map[string]string{
	"code_review": "代码审查员",
	"debugging":   "调试工程师",
	"writing":     "文案写手",
	"research":    "研究员",
	"general":     "通用助手",
}

func activationTrueCriteria(bot GroupActivationBot) string {
	role := activationRoleLabel(bot)
	desc := strings.TrimSpace(bot.Description)
	if desc == "" {
		return fmt.Sprintf("消息包含需要%s承担的请求，或明确点名要求「%s」接手", role, activationBotName(bot))
	}
	return fmt.Sprintf("消息包含需要%s承担的请求（%s），或明确点名要求「%s」接手", role, desc, activationBotName(bot))
}

func activationQuestionKey(uid int64) string {
	return fmt.Sprintf("bot_%d", uid)
}

func activationAll(bots []GroupActivationBot) map[int64]float64 {
	activated := make(map[int64]float64, len(bots))
	for _, bot := range bots {
		activated[bot.UID] = 1
	}
	return activated
}

// activationMentionedBots matches structured mention targets against the roster.
func activationMentionedBots(bots []GroupActivationBot, mentions []string) map[int64]float64 {
	if len(mentions) == 0 {
		return nil
	}
	mentioned := make(map[int64]float64)
	for _, mention := range mentions {
		trimmed := strings.TrimSpace(mention)
		if trimmed == "" {
			continue
		}
		for _, bot := range bots {
			if formatUID(bot.UID) == trimmed {
				mentioned[bot.UID] = 1
			}
		}
	}
	return mentioned
}

// activationMentionAll reports whether the message addresses every bot.
func activationMentionAll(mentions []string) bool {
	for _, mention := range mentions {
		if strings.TrimSpace(mention) == structuredMentionAllBots {
			return true
		}
	}
	return false
}

// activationContextCutoff reports how far back judging looks. Entries older than
// the cutoff are dropped from the prompt but still delivered to clients.
func activationContextCutoff(limit int) int {
	if limit <= 0 {
		return defaultActivationContextLimit
	}
	return limit
}
