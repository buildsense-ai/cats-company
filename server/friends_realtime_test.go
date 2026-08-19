package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type realtimeFriendEventStore struct {
	store.Store

	users    map[int64]*types.User
	owners   map[int64]int64
	accepted [2]int64
	rejected [2]int64
	blocked  [2]int64
	removed  [2]int64
}

func (s *realtimeFriendEventStore) GetUser(uid int64) (*types.User, error) {
	return s.users[uid], nil
}

func (s *realtimeFriendEventStore) GetBotOwner(botUID int64) (int64, error) {
	return s.owners[botUID], nil
}

func (s *realtimeFriendEventStore) AcceptFriendRequest(fromUID, toUID int64) error {
	s.accepted = [2]int64{fromUID, toUID}
	return nil
}

func (s *realtimeFriendEventStore) RejectFriendRequest(fromUID, toUID int64) error {
	s.rejected = [2]int64{fromUID, toUID}
	return nil
}

func (s *realtimeFriendEventStore) BlockUser(uid, blockedUID int64) error {
	s.blocked = [2]int64{uid, blockedUID}
	return nil
}

func (s *realtimeFriendEventStore) RemoveFriend(uid, friendUID int64) error {
	s.removed = [2]int64{uid, friendUID}
	return nil
}

func newFriendEventTestHub(uids ...int64) (*Hub, map[int64]*Client) {
	hub := &Hub{clients: make(map[int64]map[*Client]struct{}, len(uids))}
	clients := make(map[int64]*Client, len(uids))
	for _, uid := range uids {
		client := &Client{uid: uid, send: make(chan []byte, 4)}
		hub.clients[uid] = map[*Client]struct{}{client: {}}
		clients[uid] = client
	}
	return hub, clients
}

func readFriendEvent(t *testing.T, client *Client) *MsgServerFriend {
	t.Helper()
	select {
	case payload := <-client.send:
		var message ServerMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("unmarshal friend event: %v", err)
		}
		if message.Friend == nil {
			t.Fatalf("expected friend event, payload=%s", payload)
		}
		return message.Friend
	default:
		t.Fatal("expected friend event to be delivered")
		return nil
	}
}

func assertFriendEvent(t *testing.T, client *Client, action string, fromUID, toUID int64) {
	t.Helper()
	event := readFriendEvent(t, client)
	if event.Action != action || event.From != fromUID || event.To != toUID {
		t.Fatalf(
			"unexpected friend event: got action=%q from=%d to=%d, want action=%q from=%d to=%d",
			event.Action,
			event.From,
			event.To,
			action,
			fromUID,
			toUID,
		)
	}
}

func assertNoFriendEvent(t *testing.T, client *Client) {
	t.Helper()
	select {
	case payload := <-client.send:
		t.Fatalf("unexpected friend event: %s", payload)
	default:
	}
}

func TestFriendHandlerNotifyFriendEventDeduplicatesRecipients(t *testing.T) {
	client := &Client{uid: 8, send: make(chan []byte, 2)}
	hub := &Hub{
		clients: map[int64]map[*Client]struct{}{
			8: {client: {}},
		},
	}
	handler := NewFriendHandler(nil, hub)

	handler.notifyFriendEvent("request", 7, 8, "hello", 8, 8, 0)

	select {
	case payload := <-client.send:
		var message ServerMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("unmarshal friend event: %v", err)
		}
		if message.Friend == nil {
			t.Fatal("expected friend event")
		}
		if message.Friend.Action != "request" || message.Friend.From != 7 || message.Friend.To != 8 || message.Friend.Msg != "hello" {
			t.Fatalf("unexpected friend event: %+v", message.Friend)
		}
	default:
		t.Fatal("expected friend event to be delivered")
	}

	select {
	case duplicate := <-client.send:
		t.Fatalf("unexpected duplicate friend event: %s", duplicate)
	default:
	}
}

func TestFriendHandlerNotifyFriendEventWithoutHubIsSafe(t *testing.T) {
	handler := NewFriendHandler(nil)
	handler.notifyFriendEvent("removed", 7, 8, "", 7, 8)
}

func TestFriendHandlerAcceptAndRejectEventsUseActionTargetAsSender(t *testing.T) {
	const (
		ownerUID     int64 = 7
		applicantUID int64 = 9
		agentUID     int64 = 43
	)

	tests := []struct {
		name       string
		action     string
		path       string
		handle     func(*FriendHandler, http.ResponseWriter, *http.Request)
		storedPair func(*realtimeFriendEventStore) [2]int64
	}{
		{
			name:   "accepted",
			action: "accepted",
			path:   "/api/friends/accept",
			handle: func(handler *FriendHandler, w http.ResponseWriter, r *http.Request) {
				handler.HandleAcceptRequest(w, r)
			},
			storedPair: func(db *realtimeFriendEventStore) [2]int64 {
				return db.accepted
			},
		},
		{
			name:   "rejected",
			action: "rejected",
			path:   "/api/friends/reject",
			handle: func(handler *FriendHandler, w http.ResponseWriter, r *http.Request) {
				handler.HandleRejectRequest(w, r)
			},
			storedPair: func(db *realtimeFriendEventStore) [2]int64 {
				return db.rejected
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &realtimeFriendEventStore{
				users: map[int64]*types.User{
					agentUID: {
						ID:          agentUID,
						AccountType: types.AccountBot,
					},
				},
				owners: map[int64]int64{agentUID: ownerUID},
			}
			hub, clients := newFriendEventTestHub(ownerUID, applicantUID, agentUID)
			handler := NewFriendHandler(db, hub)
			req := httptest.NewRequest(
				http.MethodPost,
				tc.path,
				bytes.NewBufferString(`{"agent_uid":43,"user_id":9}`),
			)
			req = req.WithContext(context.WithValue(req.Context(), uidKey, ownerUID))
			rec := httptest.NewRecorder()

			tc.handle(handler, rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got, want := tc.storedPair(db), [2]int64{applicantUID, agentUID}; got != want {
				t.Fatalf("unexpected persistence arguments: got=%v want=%v", got, want)
			}
			for _, recipientUID := range []int64{ownerUID, applicantUID, agentUID} {
				assertFriendEvent(t, clients[recipientUID], tc.action, agentUID, applicantUID)
				assertNoFriendEvent(t, clients[recipientUID])
			}
		})
	}
}

func TestFriendHandlerRemoveNotifiesTargetAgentOwner(t *testing.T) {
	const (
		actorUID int64 = 9
		ownerUID int64 = 7
		agentUID int64 = 43
	)

	db := &realtimeFriendEventStore{
		users: map[int64]*types.User{
			agentUID: {
				ID:          agentUID,
				AccountType: types.AccountBot,
			},
		},
		owners: map[int64]int64{agentUID: ownerUID},
	}
	hub, clients := newFriendEventTestHub(actorUID, ownerUID, agentUID)
	handler := NewFriendHandler(db, hub)
	req := httptest.NewRequest(http.MethodDelete, "/api/friends/remove?user_id=43", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, actorUID))
	rec := httptest.NewRecorder()

	handler.HandleRemoveFriend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got, want := db.removed, [2]int64{actorUID, agentUID}; got != want {
		t.Fatalf("unexpected persistence arguments: got=%v want=%v", got, want)
	}
	for _, recipientUID := range []int64{actorUID, ownerUID, agentUID} {
		assertFriendEvent(t, clients[recipientUID], "removed", actorUID, agentUID)
		assertNoFriendEvent(t, clients[recipientUID])
	}
}

func TestFriendHandlerBlockHidesBlockedActionFromTargetAndAgentOwner(t *testing.T) {
	const (
		actorUID int64 = 9
		ownerUID int64 = 7
		agentUID int64 = 43
	)

	db := &realtimeFriendEventStore{
		users: map[int64]*types.User{
			agentUID: {
				ID:          agentUID,
				AccountType: types.AccountBot,
			},
		},
		owners: map[int64]int64{agentUID: ownerUID},
	}
	hub, clients := newFriendEventTestHub(actorUID, ownerUID, agentUID)
	handler := NewFriendHandler(db, hub)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/friends/block",
		bytes.NewBufferString(`{"user_id":43}`),
	)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, actorUID))
	rec := httptest.NewRecorder()

	handler.HandleBlock(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got, want := db.blocked, [2]int64{actorUID, agentUID}; got != want {
		t.Fatalf("unexpected persistence arguments: got=%v want=%v", got, want)
	}
	assertFriendEvent(t, clients[actorUID], "blocked", actorUID, agentUID)
	assertNoFriendEvent(t, clients[actorUID])
	for _, recipientUID := range []int64{ownerUID, agentUID} {
		assertFriendEvent(t, clients[recipientUID], "removed", actorUID, agentUID)
		assertNoFriendEvent(t, clients[recipientUID])
	}
}
