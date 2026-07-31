package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type messageSearchTestStore struct {
	store.Store
	results    []*store.MessageSearchResult
	err        error
	calls      int
	viewerUID  int64
	query      string
	searchType string
	limit      int
}

func (s *messageSearchTestStore) SearchMessages(viewerUID int64, query, searchType string, limit int) ([]*store.MessageSearchResult, error) {
	s.calls++
	s.viewerUID = viewerUID
	s.query = query
	s.searchType = searchType
	s.limit = limit
	return s.results, s.err
}

type messageSearchUnsupportedStore struct {
	store.Store
}

func TestHandleSearchMessagesValidatesRequest(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantAllow  string
	}{
		{name: "method", method: http.MethodPost, target: "/api/messages/search?q=hello", wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodGet},
		{name: "missing query", method: http.MethodGet, target: "/api/messages/search", wantStatus: http.StatusBadRequest},
		{name: "blank query", method: http.MethodGet, target: "/api/messages/search?q=%20%20", wantStatus: http.StatusBadRequest},
		{name: "one character query", method: http.MethodGet, target: "/api/messages/search?q=a", wantStatus: http.StatusBadRequest},
		{name: "invalid type", method: http.MethodGet, target: "/api/messages/search?q=hello&type=other", wantStatus: http.StatusBadRequest},
		{name: "zero limit", method: http.MethodGet, target: "/api/messages/search?q=hello&limit=0", wantStatus: http.StatusBadRequest},
		{name: "negative limit", method: http.MethodGet, target: "/api/messages/search?q=hello&limit=-1", wantStatus: http.StatusBadRequest},
		{name: "large limit", method: http.MethodGet, target: "/api/messages/search?q=hello&limit=101", wantStatus: http.StatusBadRequest},
		{name: "non numeric limit", method: http.MethodGet, target: "/api/messages/search?q=hello&limit=nope", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &messageSearchTestStore{}
			handler := NewMessageHandler(db, nil)
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
			rec := httptest.NewRecorder()

			handler.HandleSearchMessages(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Fatalf("Allow=%q, want %q", got, tc.wantAllow)
			}
			if db.calls != 0 {
				t.Fatalf("SearchMessages calls=%d, want 0", db.calls)
			}
		})
	}
}

func TestHandleSearchMessagesUsesAuthenticatedViewerAndDefaults(t *testing.T) {
	db := &messageSearchTestStore{results: nil}
	handler := NewMessageHandler(db, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/messages/search?q=%20Hello%20", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
	rec := httptest.NewRecorder()

	handler.HandleSearchMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.calls != 1 || db.viewerUID != 42 || db.query != "Hello" || db.searchType != store.MessageSearchAll || db.limit != defaultMessageSearchLimit {
		t.Fatalf("unexpected search call: calls=%d uid=%d query=%q type=%q limit=%d", db.calls, db.viewerUID, db.query, db.searchType, db.limit)
	}
	var body struct {
		Results []store.MessageSearchResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Results == nil || len(body.Results) != 0 {
		t.Fatalf("results=%#v, want non-nil empty array", body.Results)
	}
}

func TestHandleSearchMessagesIgnoresUndocumentedCategoryParameter(t *testing.T) {
	db := &messageSearchTestStore{results: []*store.MessageSearchResult{}}
	handler := NewMessageHandler(db, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/messages/search?q=report&category=artifact&limit=7", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleSearchMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.searchType != store.MessageSearchAll || db.limit != 7 {
		t.Fatalf("type=%q limit=%d, want all and 7", db.searchType, db.limit)
	}
}

func TestHandleSearchMessagesUnavailableAndStoreError(t *testing.T) {
	t.Run("optional store unavailable", func(t *testing.T) {
		handler := NewMessageHandler(&messageSearchUnsupportedStore{}, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/messages/search?q=hello", nil)
		rec := httptest.NewRecorder()

		handler.HandleSearchMessages(rec, req)

		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("status=%d body=%s, want 501", rec.Code, rec.Body.String())
		}
	})

	t.Run("store error is generic", func(t *testing.T) {
		db := &messageSearchTestStore{err: errors.New("secret database detail")}
		handler := NewMessageHandler(db, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/messages/search?q=hello", nil)
		rec := httptest.NewRecorder()

		handler.HandleSearchMessages(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s, want 500", rec.Code, rec.Body.String())
		}
		if body := rec.Body.String(); body == "" || strings.Contains(body, "secret database detail") {
			t.Fatalf("response leaked store error: %s", body)
		}
	})
}

type messageAroundTestStore struct {
	*identityMessageStore
	aroundMessages []*types.Message
	aroundErr      error
	aroundCalls    int
	aroundTopicID  string
	aroundID       int64
	aroundLimit    int
}

func (s *messageAroundTestStore) GetMessagesAround(topicID string, messageID int64, limit int) ([]*types.Message, error) {
	s.aroundCalls++
	s.aroundTopicID = topicID
	s.aroundID = messageID
	s.aroundLimit = limit
	return s.aroundMessages, s.aroundErr
}

func TestHandleGetMessagesAroundDeniesUnreadableTopicBeforeStore(t *testing.T) {
	db := &messageAroundTestStore{identityMessageStore: &identityMessageStore{
		users: map[int64]*types.User{7: {ID: 7, AccountType: types.AccountHuman}},
	}}
	hub := NewHub(db, nil)
	handler := NewMessageHandler(db, hub)
	req := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=grp_80&around_id=31", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleGetMessages(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if db.aroundCalls != 0 {
		t.Fatalf("GetMessagesAround calls=%d, want 0", db.aroundCalls)
	}
}

func TestHandleGetMessagesAroundAllowsReadableTopic(t *testing.T) {
	db := &messageAroundTestStore{
		identityMessageStore: &identityMessageStore{
			users:        map[int64]*types.User{7: {ID: 7, Username: "alice", AccountType: types.AccountHuman}},
			groupMembers: []*types.GroupMember{{GroupID: 80, UserID: 7}},
		},
		aroundMessages: []*types.Message{{ID: 31, TopicID: "grp_80", FromUID: 7, Content: "hello", MsgType: "text"}},
	}
	hub := NewHub(db, nil)
	handler := NewMessageHandler(db, hub)
	req := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=grp_80&around_id=31", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleGetMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.aroundCalls != 1 || db.aroundTopicID != "grp_80" || db.aroundID != 31 || db.aroundLimit != defaultMessageAroundLimit {
		t.Fatalf("unexpected around call: calls=%d topic=%q id=%d limit=%d", db.aroundCalls, db.aroundTopicID, db.aroundID, db.aroundLimit)
	}
	var body struct {
		Messages []map[string]interface{} `json:"messages"`
		AroundID int64                    `json:"around_id"`
		TopicID  string                   `json:"topic_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.AroundID != 31 || body.TopicID != "grp_80" || len(body.Messages) != 1 {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestHandleGetMessagesAroundRejectsTargetOutsideRequestedTopic(t *testing.T) {
	tests := []struct {
		name           string
		aroundMessages []*types.Message
	}{
		{
			name: "target id does not exist",
			aroundMessages: []*types.Message{
				{ID: 30, TopicID: "grp_80", FromUID: 7, Content: "before", MsgType: "text"},
				{ID: 32, TopicID: "grp_80", FromUID: 7, Content: "after", MsgType: "text"},
			},
		},
		{
			name: "target id belongs to another topic",
			aroundMessages: []*types.Message{
				{ID: 31, TopicID: "grp_81", FromUID: 7, Content: "other topic", MsgType: "text"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &messageAroundTestStore{
				identityMessageStore: &identityMessageStore{
					users:        map[int64]*types.User{7: {ID: 7, Username: "alice", AccountType: types.AccountHuman}},
					groupMembers: []*types.GroupMember{{GroupID: 80, UserID: 7}},
				},
				aroundMessages: tc.aroundMessages,
			}
			handler := NewMessageHandler(db, NewHub(db, nil))
			req := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=grp_80&around_id=31", nil)
			req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
			rec := httptest.NewRecorder()

			handler.HandleGetMessages(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleGetMessagesAroundValidatesParameters(t *testing.T) {
	for _, target := range []string{
		"/api/messages?topic_id=grp_80&around_id=0",
		"/api/messages?topic_id=grp_80&around_id=nope",
		"/api/messages?topic_id=grp_80&around_id=31&limit=101",
	} {
		t.Run(target, func(t *testing.T) {
			db := &messageAroundTestStore{identityMessageStore: &identityMessageStore{}}
			handler := NewMessageHandler(db, NewHub(db, nil))
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()

			handler.HandleGetMessages(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			if db.aroundCalls != 0 {
				t.Fatalf("GetMessagesAround calls=%d, want 0", db.aroundCalls)
			}
		})
	}
}
