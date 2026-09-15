package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHandleKickMemberNotifiesKickedMember verifies that a kicked member, who
// is no longer returned by GetGroupMembers, still receives the
// "member_kicked" event with pres.user_id pointing at their uid. Clients use
// it to stop tasks that are still running for the group.
func TestHandleKickMemberNotifiesKickedMember(t *testing.T) {
	db := newChannelAgentTestStore()
	groupID, err := db.CreateGroup("kick-notify", 7)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.AddGroupMember(groupID, 42, "member"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	kickedClient := &Client{uid: 42, send: make(chan []byte, 8)}
	adminClient := &Client{uid: 7, send: make(chan []byte, 8)}
	hub := &Hub{
		clients: map[int64]map[*Client]struct{}{
			42: {kickedClient: {}},
			7:  {adminClient: {}},
		},
	}
	handler := NewGroupHandler(db, hub)

	body := bytes.NewBufferString(fmt.Sprintf(`{"group_id":%d,"user_id":42}`, groupID))
	req := httptest.NewRequest(http.MethodPost, "/api/groups/kick", body)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleKickMember(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertMemberKicked(t, "kicked member", kickedClient, groupID, 42)
	assertMemberKicked(t, "remaining member", adminClient, groupID, 42)

	members, err := db.GetGroupMembers(groupID)
	if err != nil {
		t.Fatalf("get members: %v", err)
	}
	if len(members) != 1 || members[0].UserID != 7 {
		t.Fatalf("members after kick = %+v, want only uid 7", members)
	}
}

func assertMemberKicked(t *testing.T, label string, client *Client, groupID int64, wantKickedUID int64) {
	t.Helper()
	select {
	case data := <-client.send:
		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("%s: unmarshal message: %v", label, err)
		}
		if msg.Pres == nil || msg.Pres.What != "member_kicked" {
			t.Fatalf("%s: pres=%+v, want what=member_kicked", label, msg.Pres)
		}
		if wantTopic := fmt.Sprintf("grp_%d", groupID); msg.Pres.Topic != wantTopic {
			t.Fatalf("%s: pres.topic=%q want=%q", label, msg.Pres.Topic, wantTopic)
		}
		if msg.Pres.UserID != wantKickedUID {
			t.Fatalf("%s: pres.user_id=%d want=%d", label, msg.Pres.UserID, wantKickedUID)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s: did not receive member_kicked event", label)
	}
}

// TestNotifyGroupEventIncludingUserDeduplicatesRecipients covers the defensive
// branch where the explicit user is still present in GetGroupMembers: the
// notification must be delivered exactly once, not twice.
func TestNotifyGroupEventIncludingUserDeduplicatesRecipients(t *testing.T) {
	db := newChannelAgentTestStore()
	groupID, err := db.CreateGroup("kick-dedupe", 7)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.AddGroupMember(groupID, 42, "member"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	memberClient := &Client{uid: 42, send: make(chan []byte, 8)}
	hub := &Hub{
		clients: map[int64]map[*Client]struct{}{
			42: {memberClient: {}},
		},
	}
	handler := NewGroupHandler(db, hub)

	handler.notifyGroupEventIncludingUser(groupID, 42, "member_kicked")

	select {
	case <-memberClient.send:
	default:
		t.Fatal("member 42 did not receive the event")
	}
	select {
	case <-memberClient.send:
		t.Fatal("member 42 received the event twice")
	default:
	}
}
