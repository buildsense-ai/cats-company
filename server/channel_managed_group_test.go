package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestChannelManagedGroupRejectsHumanTopicAccess(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "agent", AccountType: types.AccountBot}
	groupID, err := db.CreateGroup("Feishu internal session", 7)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	db.groups[groupID].Kind = types.GroupKindChannelManaged
	if err := db.AddGroupMember(groupID, 43, "member"); err != nil {
		t.Fatalf("add bot: %v", err)
	}
	hub := NewHub(db, nil)
	topic := fmt.Sprintf("grp_%d", groupID)

	if code, _ := hub.validateTopicReadAccess(7, types.AccountHuman, topic); code != http.StatusNotFound {
		t.Fatalf("human read code=%d, want 404", code)
	}
	if code, _ := hub.validateMessagePublish(7, types.AccountHuman, topic, false); code != http.StatusNotFound {
		t.Fatalf("human publish code=%d, want 404", code)
	}
	if code, text := hub.validateTopicReadAccess(43, types.AccountBot, topic); code != 0 {
		t.Fatalf("bot read code=%d text=%s", code, text)
	}
	if code, text := hub.validateMessagePublish(43, types.AccountBot, topic, false); code != 0 {
		t.Fatalf("bot publish code=%d text=%s", code, text)
	}
}

func TestChannelManagedGroupRejectsHumanNotesAndLegacyGroupBinding(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", AccountType: types.AccountHuman}
	db.users[43] = &types.User{ID: 43, Username: "agent", AccountType: types.AccountBot}
	groupID, err := db.CreateGroup("Feishu internal session", 7)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	db.groups[groupID].Kind = types.GroupKindChannelManaged
	if err := db.AddGroupMember(groupID, 43, "member"); err != nil {
		t.Fatalf("add bot: %v", err)
	}
	topic := fmt.Sprintf("grp_%d", groupID)
	hub := NewHub(db, nil)
	botClient := &Client{hub: hub, uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.addClient(botClient)
	hub.handleNote(
		&Client{hub: hub, uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 1)},
		&MsgClientNote{Topic: topic, What: "kp"},
	)
	if got := len(botClient.send); got != 0 {
		t.Fatalf("hidden group bot received %d human note messages", got)
	}

	_, err = validateDeliverableChannelGroupBinding(db, &types.ChannelGroupBinding{
		Status:       types.ChannelAgentBindingActive,
		CanonicalUID: 7,
		GroupID:      groupID,
		TopicID:      topic,
	}, false)
	if err == nil {
		t.Fatal("legacy mobile binding to channel-managed group should be rejected")
	}
}

func TestChannelManagedGroupRejectsOrdinaryManagement(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", AccountType: types.AccountHuman}
	groupID, err := db.CreateGroup("Feishu internal session", 7)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	db.groups[groupID].Kind = types.GroupKindChannelManaged
	handler := NewGroupHandler(db, nil)
	body := []byte(`{"group_id":` + strconv.FormatInt(groupID, 10) + `,"name":"renamed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/groups/update", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleUpdateGroup(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChannelManagedGroupCannotCreateOrConsumeMobileLink(t *testing.T) {
	db := newChannelAgentTestStore()
	db.users[7] = &types.User{ID: 7, Username: "owner", DisplayName: "Owner", AccountType: types.AccountHuman}
	groupID, err := db.CreateGroup("Feishu internal session", 7)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	db.groups[groupID].Kind = types.GroupKindChannelManaged
	handler := NewChannelAgentBindingHandler(db, nil)
	topic := fmt.Sprintf("grp_%d", groupID)
	body := fmt.Sprintf(`{"group_id":%d,"topic_id":"%s","channel":"feishu"}`, groupID, topic)
	req := httptest.NewRequest(http.MethodPost, "/api/channel-agent-bindings/group-mobile-link", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleCreateChannelGroupMobileLink(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("managed group create status=%d body=%s", rec.Code, rec.Body.String())
	}

	link := &types.ChannelGroupMobileLink{
		SceneKey:     "g.old-managed-link",
		Channel:      "feishu",
		CanonicalUID: 7,
		GroupID:      groupID,
		TopicID:      topic,
		Status:       "active",
		ExpiresAt:    time.Now().Add(time.Minute),
	}
	if _, err := db.CreateChannelGroupMobileLink(link); err != nil {
		t.Fatalf("seed old link: %v", err)
	}
	if _, _, err := resolveChannelGroupMobileLink(db, link.SceneKey, "feishu", "", false); err == nil {
		t.Fatal("existing managed-group link should be rejected")
	}
}
