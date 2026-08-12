package server

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestChannelInboundMediaClassifiesAudioForPlayback(t *testing.T) {
	file := uploadPayload{
		FileKey:  "20260812_0123456789abcdef0123456789abcdef.ogg",
		URL:      "/uploads/files/20260812_0123456789abcdef0123456789abcdef.ogg",
		Name:     "voice.ogg",
		Size:     42,
		Type:     "file",
		MimeType: "audio/ogg; codecs=opus",
	}

	blocks := channelInboundContentBlocks("", []uploadPayload{file})
	if len(blocks) != 1 || blocks[0].Type != "audio" {
		t.Fatalf("blocks = %#v, want one audio block", blocks)
	}
	if got := channelInboundMessageType(file); got != "voice" {
		t.Fatalf("channelInboundMessageType = %q, want voice", got)
	}
	if got := channelInboundContentBlockType(uploadPayload{Type: "file", MimeType: "video/ogg"}); got != "file" {
		t.Fatalf("video/ogg block type = %q, want file", got)
	}
}

func TestDeliverInboundChannelAudioToAgentStoresVoiceMessage(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "alice", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "assistant", AccountType: types.AccountBot}
	db.owners[43] = 7
	if _, err := db.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_alice",
		ActorUID:      100,
		CanonicalUID:  7,
		OwnerUID:      7,
		AgentUID:      43,
		Status:        types.ChannelAgentBindingActive,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	file := uploadPayload{
		FileKey:  "20260812_0123456789abcdef0123456789abcdef.ogg",
		URL:      "/uploads/files/20260812_0123456789abcdef0123456789abcdef.ogg",
		Name:     "voice.ogg",
		Size:     42,
		Type:     "file",
		MimeType: "audio/ogg; codecs=opus",
	}
	if err := deliverInboundChannelMessageToAgent(db, nil, 100, 43, "", []uploadPayload{file}, "audio-agent", "feishu", nil); err != nil {
		t.Fatalf("deliver audio: %v", err)
	}

	if len(db.messages) != 1 {
		t.Fatalf("messages = %#v, want one saved message", db.messages)
	}
	message := db.messages[0]
	if message.TopicID != "p2p_7_43" || message.FromUID != 7 || message.MsgType != "voice" {
		t.Fatalf("message routing = %#v, want canonical voice message in p2p_7_43", message)
	}
	if len(message.ContentBlocks) != 1 || message.ContentBlocks[0].Type != "audio" {
		t.Fatalf("content blocks = %#v, want one audio block", message.ContentBlocks)
	}
}

func TestDeliverInboundChannelAudioToGroupStoresVoiceMessage(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "alice", AccountType: types.AccountHuman}
	db.groups[80] = &types.Group{ID: 80, Name: "Project", Kind: types.GroupKindStandard}
	db.groupMembers[80] = map[int64]*types.GroupMember{
		7: {GroupID: 80, UserID: 7, Role: "member"},
	}
	binding := &types.ChannelGroupBinding{
		Channel:       "feishu",
		ChannelAppID:  "cli_app",
		ChannelUserID: "ou_alice",
		ActorUID:      100,
		CanonicalUID:  7,
		GroupID:       80,
		TopicID:       "grp_80",
		Status:        types.ChannelAgentBindingActive,
	}
	file := uploadPayload{
		FileKey:  "20260812_0123456789abcdef0123456789abcdef.mp3",
		URL:      "/uploads/files/20260812_0123456789abcdef0123456789abcdef.mp3",
		Name:     "voice.mp3",
		Size:     42,
		Type:     "file",
		MimeType: "audio/mpeg",
	}
	if err := deliverInboundChannelMessageToGroup(db, nil, 100, binding, "", []uploadPayload{file}, "audio-group", "feishu", nil); err != nil {
		t.Fatalf("deliver group audio: %v", err)
	}

	if len(db.messages) != 1 {
		t.Fatalf("messages = %#v, want one saved message", db.messages)
	}
	message := db.messages[0]
	if message.TopicID != "grp_80" || message.FromUID != 100 || message.MsgType != "voice" {
		t.Fatalf("message routing = %#v, want actor voice message in grp_80", message)
	}
	if len(message.ContentBlocks) != 1 || message.ContentBlocks[0].Type != "audio" {
		t.Fatalf("content blocks = %#v, want one audio block", message.ContentBlocks)
	}
}
