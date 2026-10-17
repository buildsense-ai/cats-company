package server

import (
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// Runtime traffic from a peer must stay separable from that peer's
// conversation. Both are ineligible for a turn, but only one is worth reading:
// on a live group a bare "execute_shell" was read as a peer message and woke
// the other Agent.
func TestAgentContextSeparatesPeerWorkingTrafficFromPeerSpeech(t *testing.T) {
	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
		43: {ID: 43, Username: "peer_agent", DisplayName: "Peer Agent", AccountType: types.AccountBot},
	}
	hub := NewHub(&identityMessageStore{users: users}, nil)

	// Real conversation from the peer: kept, ineligible (it never wakes us).
	spoken := &types.Message{
		ID: 10, TopicID: "grp_80", FromUID: 43, MsgType: "text", Content: "我处理完了",
	}
	spokenCopy := hub.agentContextHistoryAPIMessageForRecipient(42, spoken, users, nil)
	if spokenCopy["context_reason"] != "other_agent_message" {
		t.Fatalf("peer speech reason = %#v, want other_agent_message", spokenCopy["context_reason"])
	}
	if spokenCopy["context_eligible"] != false {
		t.Fatalf("peer speech eligible = %#v, want false", spokenCopy["context_eligible"])
	}

	// Peer's tool call and tool result: a distinct reason, so a reader can drop
	// them without also dropping the peer's real messages.
	toolCall := &types.Message{
		ID: 11, TopicID: "grp_80", FromUID: 43, MsgType: "text", Content: "glob",
		ContentBlocks: []types.ContentBlock{{Type: "tool_use", ID: "call_1", Name: "glob"}},
	}
	result := &types.Message{
		ID: 12, TopicID: "grp_80", FromUID: 43, MsgType: "text", Content: "Found 0/0 entries",
		ContentBlocks: []types.ContentBlock{{Type: "tool_result", ToolUseID: "call_1", Content: "Found 0/0 entries"}},
	}
	for _, message := range []*types.Message{toolCall, result} {
		copied := hub.agentContextHistoryAPIMessageForRecipient(42, message, users, nil)
		if copied["context_reason"] != "other_agent_working" {
			t.Fatalf("peer working traffic reason = %#v, want other_agent_working", copied["context_reason"])
		}
		if copied["context_eligible"] != false {
			t.Fatalf("peer working traffic eligible = %#v, want false", copied["context_eligible"])
		}
	}
}

// A human @mention reaches an Agent as a raw token. The Agent has no member
// list to resolve it against, so its read copy carries the name instead.
func TestAgentContextResolvesMentionNamesOnAgentCopyOnly(t *testing.T) {
	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
		43: {ID: 43, Username: "peer_agent", DisplayName: "Peer Agent", AccountType: types.AccountBot},
	}
	hub := NewHub(&identityMessageStore{users: users}, nil)
	stored := "@usr43 顺便看下 usr436 这个编号别被误替换"
	message := &types.Message{
		ID: 20, TopicID: "grp_80", FromUID: 7, MsgType: "text", Content: stored,
		ContentBlocks: []types.ContentBlock{{
			Type: "text", Text: stored,
			Payload: map[string]interface{}{"mentions": []string{"usr43"}},
		}},
	}

	agentCopy := hub.agentContextHistoryAPIMessageForRecipient(42, message, users, map[string]string{
		"usr43": "Peer Agent",
		"all":   "所有人",
	})
	agentText, _ := agentCopy["content"].(string)
	if !strings.Contains(agentText, "@Peer Agent") {
		t.Fatalf("agent copy kept the raw token: %q", agentText)
	}
	if strings.Contains(agentText, "@usr43") {
		t.Fatalf("agent copy still carries the raw token: %q", agentText)
	}
	// usr436 is not a member: a prefix match would corrupt it into @Peer Agent6.
	if !strings.Contains(agentText, "usr436") {
		t.Fatalf("agent copy corrupted an unrelated token: %q", agentText)
	}

	// The structured mentions array stays raw: activation matches usr<id>.
	mentions, _ := agentCopy["mentions"].([]string)
	if len(mentions) != 1 || mentions[0] != "usr43" {
		t.Fatalf("structured mentions were rewritten: %#v", agentCopy["mentions"])
	}

	// The human reader resolves tokens itself; its copy stays byte-identical.
	humanCopy := hub.historyAPIMessageForRecipient(7, message, users)
	if humanText, _ := humanCopy["content"].(string); humanText != stored {
		t.Fatalf("human copy was rewritten: %q", humanText)
	}
	if message.Content != stored {
		t.Fatalf("the stored message was mutated: %q", message.Content)
	}
}

// @all is addressed to everyone; a name is clearer than the sentinel.
func TestAgentContextResolvesAllBotsMention(t *testing.T) {
	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
	}
	hub := NewHub(&identityMessageStore{users: users}, nil)
	message := &types.Message{
		ID: 21, TopicID: "grp_80", FromUID: 7, MsgType: "text", Content: "@all 一起看",
		ContentBlocks: []types.ContentBlock{{
			Type: "text", Text: "@all 一起看",
			Payload: map[string]interface{}{"mentions": []string{structuredMentionAllBots}},
		}},
	}

	agentCopy := hub.agentContextHistoryAPIMessageForRecipient(42, message, users, map[string]string{
		structuredMentionAllBots: "所有人",
	})
	if text, _ := agentCopy["content"].(string); !strings.Contains(text, "@所有人") {
		t.Fatalf("agent copy did not resolve @all: %q", text)
	}
	if agentCopy["context_reason"] != "group_message_targets_all_agents" {
		t.Fatalf("all-bots reason = %#v", agentCopy["context_reason"])
	}
}

// A peer's final answer often arrives as `thinking + assistant_text`. It must
// stay readable: the judged transcript keeps it, and readers drop only genuine
// working traffic. Classifying by "contains any internal block" instead of
// "internal and nothing readable" would silently lose the peer's reply.
func TestAgentContextKeepsPeerAnswerThatCarriesInternalBlocks(t *testing.T) {
	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
		43: {ID: 43, Username: "peer_agent", DisplayName: "Peer Agent", AccountType: types.AccountBot},
	}
	hub := NewHub(&identityMessageStore{users: users}, nil)

	cases := []struct {
		name    string
		msgType string
		content string
		blocks  []types.ContentBlock
	}{
		{
			name: "thinking + assistant_text",
			blocks: []types.ContentBlock{
				{Type: "thinking", Thinking: "内部推理"},
				{Type: "assistant_text", Text: "最终回答"},
			},
		},
		{
			name: "tool_result + visible text",
			blocks: []types.ContentBlock{
				{Type: "tool_result", ToolUseID: "c1", Content: "raw"},
				{Type: "text", Text: "说明"},
			},
		},
		{name: "video", msgType: "video"},
	}
	for _, c := range cases {
		message := &types.Message{
			ID: 30, TopicID: "grp_80", FromUID: 43, MsgType: c.msgType,
			Content: c.content, ContentBlocks: c.blocks,
		}
		got := hub.agentContextHistoryAPIMessageForRecipient(42, message, users, nil)
		if got["context_reason"] != "other_agent_message" {
			t.Fatalf("%s: reason = %#v, want other_agent_message (peer content must stay readable)",
				c.name, got["context_reason"])
		}
	}
}

// Only traffic that carries no readable block is working traffic.
func TestAgentContextStillMarksPureWorkingTraffic(t *testing.T) {
	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
		43: {ID: 43, Username: "peer_agent", DisplayName: "Peer Agent", AccountType: types.AccountBot},
	}
	hub := NewHub(&identityMessageStore{users: users}, nil)

	for _, block := range []types.ContentBlock{
		{Type: "tool_use", ID: "c1", Name: "glob"},
		{Type: "tool_result", ToolUseID: "c1", Content: "Found 0/0"},
		{Type: "thinking", Thinking: "内部推理"},
		{Type: "runtime_plan"},
	} {
		message := &types.Message{
			ID: 31, TopicID: "grp_80", FromUID: 43, MsgType: "text",
			Content: block.Type, ContentBlocks: []types.ContentBlock{block},
		}
		got := hub.agentContextHistoryAPIMessageForRecipient(42, message, users, nil)
		if got["context_reason"] != "other_agent_working" {
			t.Fatalf("%s: reason = %#v, want other_agent_working", block.Type, got["context_reason"])
		}
	}
}

// The member list, not the page's senders, supplies the names: a member who has
// not spoken on this page is exactly who a peer cannot identify.
func TestMentionNamesComeFromGroupMembersNotPageSenders(t *testing.T) {
	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
	}
	// usr99 is a group member who never speaks on this page.
	store := &identityMessageStore{
		users: users,
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7, DisplayName: "Alice"},
			{GroupID: 80, UserID: 42, DisplayName: "Dev Agent"},
			{GroupID: 80, UserID: 99, DisplayName: "Quiet Member"},
		},
	}
	handler := &MessageHandler{db: store}

	names := handler.mentionNamesForAgentContext("grp_80", users)
	if names["usr99"] != "Quiet Member" {
		t.Fatalf("member who did not speak is missing from names: %#v", names)
	}
	if names["usr7"] != "Alice" {
		t.Fatalf("page sender missing from names: %#v", names)
	}

	// A p2p topic has no member list and no @ addressing: nothing to rewrite.
	if names := handler.mentionNamesForAgentContext("p2p_7_42", users); len(names) != 0 {
		t.Fatalf("p2p topic produced names: %#v", names)
	}
}

// Messages without mentions, and non-group topics, are untouched.
func TestAgentContextLeavesMentionFreeMessagesAlone(t *testing.T) {
	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
	}
	hub := NewHub(&identityMessageStore{users: users}, nil)
	message := &types.Message{ID: 22, TopicID: "grp_80", FromUID: 7, MsgType: "text", Content: "没有 at 符号"}

	agentCopy := hub.agentContextHistoryAPIMessageForRecipient(42, message, users, nil)
	if agentCopy["content"] != "没有 at 符号" {
		t.Fatalf("mention-free content changed: %#v", agentCopy["content"])
	}
}
