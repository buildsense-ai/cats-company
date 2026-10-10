package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Scores holds every judged candidate's score, including the ones below the
	// threshold. Activated alone cannot explain a message that reached nobody:
	// it keeps only the winners, so a correct "nobody" and a criteria that never
	// fires look identical. This exists for observability and does not affect
	// routing.
	Scores map[int64]float64
	// Source explains which rule produced the decision.
	Source string
	// Degraded is true when judging failed and the fallback applied.
	Degraded bool
}

const (
	activationSourceSingleBot    = "single_bot"
	activationSourceMention      = "mention"
	activationSourceChannel      = "channel_trigger"
	activationSourceBotSender    = "bot_sender"
	activationSourceJev          = "jev"
	activationSourceDegraded     = "jev_unavailable"
	activationSourceNoBot        = "no_bot"
	activationSourceNoMention    = "no_mention"
	activationSourceDefaultAgent = "default_agent"
	// activationSourceNotConversation marks a message that never reaches
	// judging because it is not conversation: agent working traffic (tool
	// calls, tool results, thinking, runtime plans, stream deltas, task
	// status) rather than something a member could reply to.
	activationSourceNotConversation = "not_conversation"
	// activationSourceNoCandidate marks a judged group where every bot was
	// filtered out before judging — the author itself, a member who cannot
	// speak, or one already working. There is nobody left to ask about, so the
	// judge is never called.
	activationSourceNoCandidate = "no_candidate"
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
	// NotConversation marks agent runtime traffic rather than a message a
	// member could reply to: tool calls, tool results, thinking, runtime plans,
	// stream deltas and task status.
	//
	// The zero value is false, so a caller that does not classify the message
	// keeps the previous behaviour of judging it.
	NotConversation bool
	// MutedUIDs lists members the group has silenced. A muted member is
	// forbidden from posting, so activating it would only produce a rejected
	// write — the judgement, the delivery and the model turn are all waste.
	// Mute is therefore a lock: a muted member is never activated, not even by
	// an explicit mention.
	//
	// It is read from the roster the broadcaster already loaded, so it costs
	// nothing on the paths that never judge.
	MutedUIDs []int64
	// WorkingUIDs lazily loads the members that are mid-turn. They are dropped
	// from the judgement because a bot that is already running does not need to
	// be asked whether it should run: the answer cannot change anything, and
	// the question is what makes two busy bots keep waking each other.
	//
	// It is a loader rather than a slice because it costs a store query, and
	// the paths that never judge — a single-bot group, a mention, and above all
	// agent working traffic, which is the bulk of a busy group's messages —
	// must not pay for it.
	WorkingUIDs func() []int64
	// DefaultAgentUID is the agent an agent-task group falls back to when
	// judging is not installed. It is only consulted on the deterministic path:
	// once judging runs, its verdict is the decision, including a verdict of
	// "nobody", and a fallback would silently overrule it.
	DefaultAgentUID int64
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
//
// Only the identity is carried. Judging asks whether a member would answer,
// which is a question about participation rather than about a job title, so the
// owner-defined role and description are deliberately absent: feeding them in
// turns the decision into "does this match the posting" and narrows it to
// task-shaped messages.
type GroupActivationBot struct {
	UID         int64
	DisplayName string
}

// GroupActivationResolverFunc adapts a function to the interface, mirroring the
// existing resolver-injection pattern so tests can stub judging.
type GroupActivationResolverFunc func(ctx context.Context, req GroupActivationRequest) GroupActivationDecision

func (f GroupActivationResolverFunc) Resolve(ctx context.Context, req GroupActivationRequest) GroupActivationDecision {
	return f(ctx, req)
}

// JevGroupActivationResolver judges activation with an installed judge.
//
// The judge is an interface rather than a concrete client so a deployment can
// keep judging when the Jev lane is unreachable: the chat backend answers the
// same per-bot questions, and every rule around it stays identical.
type JevGroupActivationResolver struct {
	client    activationJudge
	threshold float64
}

// NewJevGroupActivationResolver builds the production resolver.
func NewJevGroupActivationResolver(client *JevClient) *JevGroupActivationResolver {
	if client == nil {
		return &JevGroupActivationResolver{threshold: defaultActivationThreshold}
	}
	return NewGroupActivationResolver(client)
}

// NewGroupActivationResolver builds the resolver around any judge.
func NewGroupActivationResolver(judge activationJudge) *JevGroupActivationResolver {
	return &JevGroupActivationResolver{
		client:    judge,
		threshold: defaultActivationThreshold,
	}
}

// Resolve applies the activation rules in order of increasing cost.
//
// Activating a bot and judging with Jev are two separate things. A mention
// activates a bot without any model call; judging is only the fallback for
// messages that name nobody. The filters below therefore sit on the judging
// path, and a member that must not be *judged* can still be *activated* by name.
func (r *JevGroupActivationResolver) Resolve(ctx context.Context, req GroupActivationRequest) GroupActivationDecision {
	// Working traffic is not conversation. Judging it would ask the model about
	// something the transcript itself drops, and the answer could only come
	// from imagination — measured in the field, a bare "execute_shell" scored
	// 0.56-0.60 for another bot and woke it.
	if req.NotConversation {
		return GroupActivationDecision{Source: activationSourceNotConversation}
	}
	allBots := activationBots(req.Members)
	if len(allBots) == 0 {
		return GroupActivationDecision{Source: activationSourceNoBot}
	}
	muted := activationUIDSet(req.MutedUIDs)

	// A group with one bot never needs addressing: every message is for it.
	// This is decided from the group's roster, not from the candidates below, so
	// a lone bot's own messages cannot be mistaken for a single-bot group.
	if len(allBots) == 1 {
		if allBots[0].UID == req.SenderUID {
			return GroupActivationDecision{Source: activationSourceBotSender}
		}
		// A muted member cannot post, so waking it only produces a rejected
		// write. Mute is a lock, and it applies here too: a lone muted bot is
		// simply not reachable until it is unmuted.
		if _, blocked := muted[allBots[0].UID]; blocked {
			return GroupActivationDecision{Source: activationSourceNoCandidate}
		}
		// A busy lone bot is still activated. It needs no judgement, and
		// delivering the message lets it fold the new text into the turn it is
		// already running.
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
		reachable := activationExcludingUIDs(bots, muted)
		if len(reachable) == 0 {
			return GroupActivationDecision{Source: activationSourceNoCandidate}
		}
		return GroupActivationDecision{
			Activated: activationAll(reachable),
			Source:    activationSourceChannel,
		}
	}
	// An explicit mention is the strongest signal a person can give, so it
	// short-circuits judging and keeps working when judging is unavailable.
	//
	// It is matched against the roster *including* members filtered out below.
	// A mention is a designation: "@someone else go do it" must not be
	// reinterpreted as an open request for the judge to pick a different member
	// when the named one happens to be silenced. If the named member cannot be
	// activated, the correct outcome is that nobody is.
	if mentioned := activationMentionedBots(bots, req.Mentions); len(mentioned) > 0 {
		reachable := activationExcludingScores(mentioned, muted)
		if len(reachable) == 0 {
			return GroupActivationDecision{Source: activationSourceNoCandidate}
		}
		// A member that is merely busy stays addressable by name: this costs no
		// model call, and the client folds the message into the running turn.
		return GroupActivationDecision{Activated: reachable, Source: activationSourceMention}
	}
	// "@all" asks every bot to look, which needs no semantic judgement.
	if activationMentionAll(req.Mentions) {
		reachable := activationExcludingUIDs(bots, muted)
		if len(reachable) == 0 {
			return GroupActivationDecision{Source: activationSourceNoCandidate}
		}
		return GroupActivationDecision{Activated: activationAll(reachable), Source: activationSourceMention}
	}
	// Mute is a lock, so it filters before the judge is asked. A muted member
	// cannot post, so offering it would spend a model call to produce a write
	// the server rejects.
	bots = activationExcludingUIDs(bots, muted)
	if len(bots) == 0 {
		return GroupActivationDecision{Source: activationSourceNoCandidate}
	}
	// Only now, on the path that actually calls the model, are the busy members
	// dropped. A bot that is already running does not need to be asked whether
	// it should run: the answer cannot change anything, and asking is what let
	// two bots keep waking each other on every progress line.
	//
	// The lookup is resolved here rather than at the top because it costs a
	// store query, and the paths above — working traffic, a single-bot group, a
	// mention — never need it.
	bots = activationExcludingUIDs(bots, activationWorkingUIDSet(req))
	if len(bots) == 0 {
		// Nobody is left to ask about. The judge is not called: the answer
		// "nobody should reply" is already known, and paying for a round trip
		// to hear it would be pure waste.
		return GroupActivationDecision{Source: activationSourceNoCandidate}
	}
	// A resolver with no judge installed cannot judge. Reporting it as
	// degraded is the documented behaviour: the group is told routing is
	// unavailable instead of a member being guessed.
	if r.client == nil || !r.client.Enabled() {
		// Judging is switched off or unreachable. The caller reports that to
		// the group instead of guessing an addressee.
		return GroupActivationDecision{Source: activationSourceDegraded, Degraded: true}
	}

	decision, err := r.judge(ctx, req, bots)
	if err != nil {
		// Never guess on failure: the caller tells the group that routing is
		// unavailable so a person can mention the right member instead.
		return GroupActivationDecision{Source: activationSourceDegraded, Degraded: true}
	}
	// A judgement that reaches nobody is a result, not a failure. The group is
	// told nothing and no bot runs: an unanswered message is the correct
	// outcome for chat, and inventing an addressee would make every message
	// produce a reply.
	return decision
}

func (r *JevGroupActivationResolver) judge(ctx context.Context, req GroupActivationRequest, bots []GroupActivationBot) (GroupActivationDecision, error) {
	prompt := loadActivationPrompt()
	questions := make(map[string]JevQuestion, len(bots))
	for _, bot := range bots {
		name := activationBotName(bot)
		questions[activationQuestionKey(bot.UID)] = JevQuestion{
			Type:         "noul",
			Instructions: renderActivationPrompt(prompt.Instructions, name),
			Criteria: map[string]string{
				"true":  renderActivationPrompt(prompt.CriteriaTrue, name),
				"false": renderActivationPrompt(prompt.CriteriaFalse, name),
			},
		}
	}

	answers, err := r.client.Ask(ctx, activationState(req, bots), questions)
	if err != nil {
		return GroupActivationDecision{}, err
	}
	return r.scoreAnswers(answers, bots), nil
}

// scoreAnswers turns the raw answers into a decision.
//
// Scores keeps every candidate's value, including the ones below the threshold.
// Activated alone cannot explain a message that reached nobody: it holds only
// the winners, so a correct "nobody" and a criteria that never fires look
// identical. Keeping the losers is what makes the difference visible.
func (r *JevGroupActivationResolver) scoreAnswers(answers map[string]jevAnswer, bots []GroupActivationBot) GroupActivationDecision {
	activated := make(map[int64]float64)
	scores := make(map[int64]float64, len(bots))
	for _, bot := range bots {
		answer, ok := answers[activationQuestionKey(bot.UID)]
		if !ok {
			continue
		}
		value := answer.NoulAnswer()
		if !value.Valid {
			continue
		}
		scores[bot.UID] = value.Value
		if value.Value >= r.threshold {
			activated[bot.UID] = value.Value
		}
	}
	return GroupActivationDecision{
		Activated: activated,
		Scores:    scores,
		Source:    activationSourceJev,
	}
}

// activationState renders the prompt material. It names every member by the
// display name the group shows, so the judge and the participants agree on who
// is who, and it marks each one as a bot or a user so the judge can tell the
// two apart.
//
// The state is built as a named structure and sent as JSON text — the API takes
// the state as a string, so this is the serialised form, not an object on the
// wire. Each part carries a name ("group", "members", "messages"), which is what
// the upstream recommends and what keeps the roster from being read as more
// transcript.
func activationState(req GroupActivationRequest, bots []GroupActivationBot) string {
	context := judgeContext(req)
	state := map[string]any{}
	if name := strings.TrimSpace(context.GroupName); name != "" {
		state["group"] = map[string]any{"name": name}
	}

	members := make([]map[string]any, 0, len(req.Members))
	seen := make(map[int64]bool, len(req.Members))
	for _, bot := range bots {
		seen[bot.UID] = true
		members = append(members, map[string]any{
			"name": activationBotName(bot),
			"type": "bot",
		})
	}
	for _, member := range req.Members {
		if member == nil || member.IsBot || seen[member.UserID] {
			continue
		}
		name := activationMemberName(member)
		if name == "" {
			continue
		}
		seen[member.UserID] = true
		members = append(members, map[string]any{
			"name": name,
			"type": "user",
		})
	}
	if len(members) > 0 {
		state["members"] = members
	}

	if turns := mergeActivationTurns(context.RecentTurns); len(turns) > 0 {
		messages := make([]map[string]any, 0, len(turns))
		for _, turn := range turns {
			kind := "user"
			if turn.IsBot {
				kind = "bot"
			}
			messages = append(messages, map[string]any{
				"from": turn.Speaker,
				"type": kind,
				"text": turn.Text,
			})
		}
		state["messages"] = messages
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		// Encoding plain strings and maps cannot fail in practice; falling back
		// to the group name alone keeps a judgement possible rather than
		// turning a serialisation problem into an outage.
		return context.GroupName
	}
	return string(encoded)
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

// activationBots lists the group's bots. The display name is carried through
// because the judge is told who each member is by name; without it the prompt
// would name bots by uid and the roster would not line up with the transcript,
// which speaks display names.
func activationBots(members []*types.GroupMember) []GroupActivationBot {
	bots := make([]GroupActivationBot, 0, len(members))
	for _, member := range members {
		if member == nil || !member.IsBot {
			continue
		}
		bots = append(bots, GroupActivationBot{
			UID:         member.UserID,
			DisplayName: activationMemberName(member),
		})
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

// activationWorkingUIDSet resolves the lazily loaded busy members. A caller
// that knows judging will not happen can leave the loader unset, in which case
// nobody is treated as busy — the pre-change behaviour.
func activationWorkingUIDSet(req GroupActivationRequest) map[int64]struct{} {
	if req.WorkingUIDs == nil {
		return nil
	}
	return activationUIDSet(req.WorkingUIDs())
}

// activationUIDSet indexes a uid list for membership checks.
func activationUIDSet(uids []int64) map[int64]struct{} {
	if len(uids) == 0 {
		return nil
	}
	index := make(map[int64]struct{}, len(uids))
	for _, uid := range uids {
		if uid > 0 {
			index[uid] = struct{}{}
		}
	}
	return index
}

// activationExcludingUIDs drops the listed members from the candidates.
func activationExcludingUIDs(bots []GroupActivationBot, excluded map[int64]struct{}) []GroupActivationBot {
	if len(excluded) == 0 {
		return bots
	}
	filtered := make([]GroupActivationBot, 0, len(bots))
	for _, bot := range bots {
		if _, drop := excluded[bot.UID]; drop {
			continue
		}
		filtered = append(filtered, bot)
	}
	return filtered
}

// activationExcludingScores drops the listed members from a selection that is
// already keyed by uid. Mentions arrive in that form, so re-filtering them
// keeps "who was named" and "who may actually be woken" separate.
func activationExcludingScores(selected map[int64]float64, excluded map[int64]struct{}) map[int64]float64 {
	if len(excluded) == 0 {
		return selected
	}
	filtered := make(map[int64]float64, len(selected))
	for uid, score := range selected {
		if _, drop := excluded[uid]; drop {
			continue
		}
		filtered[uid] = score
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
