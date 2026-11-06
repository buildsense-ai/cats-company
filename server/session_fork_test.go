package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type forkTestStore struct {
	store.Store
	record       *types.SessionFork
	apiKey       string
	completeBody string
}

func (s *forkTestStore) GetUser(id int64) (*types.User, error) {
	account := types.AccountHuman
	if id == 9 {
		account = types.AccountBot
	}
	return &types.User{ID: id, AccountType: account}, nil
}
func (s *forkTestStore) GetBotByAPIKey(key string) (int64, error) {
	if key == s.apiKey && key != "" {
		return 9, nil
	}
	return 0, errors.New("invalid key")
}
func (s *forkTestStore) GetSessionFork(ctx context.Context, uid int64, request, topic string) (*types.SessionFork, error) {
	if s.record != nil && s.record.ActorUserID == uid {
		return s.record, nil
	}
	return nil, store.ErrSessionForkNotFound
}
func (s *forkTestStore) CreateSessionFork(context.Context, types.SessionForkRequest) (*types.SessionFork, error) {
	panic("unexpected create")
}
func (s *forkTestStore) GetBotSessionFork(_ context.Context, uid int64, topic string) (*types.SessionFork, error) {
	if s.record != nil && s.record.AgentUID == uid && s.record.TargetTopicID == topic {
		return s.record, nil
	}
	return nil, store.ErrSessionForkNotFound
}
func (s *forkTestStore) CompleteSessionFork(_ context.Context, uid int64, req types.SessionForkCompletion) (*types.SessionFork, error) {
	s.completeBody = req.BodyID
	return s.GetBotSessionFork(context.Background(), uid, req.TargetTopicID)
}
func (s *forkTestStore) SessionForkWritable(context.Context, string) (bool, error) { return false, nil }

func (s *forkTestStore) CancelSessionFork(_ context.Context, uid int64, request string) (*types.SessionFork, error) {
	if s.record == nil || s.record.ActorUserID != uid || s.record.RequestID != request {
		return nil, store.ErrSessionForkNotFound
	}
	if s.record.State != "pending" {
		return s.record, nil
	}
	s.record.State, s.record.Error = "failed", types.SessionForkErrorCancelled
	return s.record, nil
}

func (s *forkTestStore) ExpireSessionForks(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

type forkCreationTestStore struct {
	*forkTestStore
	store.ConversationTaskStatusStore
	status  string
	owner   int64
	friend  bool
	group   *types.Group
	member  bool
	created []types.SessionForkRequest
}

func (s *forkCreationTestStore) AreFriends(int64, int64) (bool, error)    { return s.friend, nil }
func (s *forkCreationTestStore) GetGroup(int64) (*types.Group, error)     { return s.group, nil }
func (s *forkCreationTestStore) IsGroupMember(int64, int64) (bool, error) { return s.member, nil }

func (s *forkCreationTestStore) GetBotOwner(int64) (int64, error) {
	return s.owner, nil
}

func (s *forkCreationTestStore) GetConversationTaskStatuses([]string) (map[string]*types.ConversationTaskStatus, error) {
	return map[string]*types.ConversationTaskStatus{
		"p2p_7_9": {TopicID: "p2p_7_9", SourceUID: 9, State: s.status},
	}, nil
}

func (s *forkCreationTestStore) CreateSessionFork(_ context.Context, req types.SessionForkRequest) (*types.SessionFork, error) {
	s.created = append(s.created, req)
	return &types.SessionFork{
		ActorUserID: req.ActorUserID, AgentUID: req.AgentUID, BodyID: req.BodyID,
		RequestID: req.RequestID, SourceTopicID: req.SourceTopicID,
		TargetTopicID: "grp_20", State: "pending",
	}, nil
}

func TestSessionForkCloudStatusGate(t *testing.T) {
	for _, tc := range []struct {
		name, status, wantError string
		actor, owner            int64
		offline                 bool
		wantCode                int
	}{
		{"stale reaches runtime", "stale", "", 7, 7, false, 200},
		{"completed reaches runtime", "completed", "", 7, 7, false, 200},
		{"running stays busy", "running", "session_fork_source_busy", 7, 7, false, 409},
		{"waiting stays busy", "waiting", "session_fork_source_busy", 7, 7, false, 409},
		{"stale requires online body", "stale", "session_fork_agent_offline", 7, 7, true, 409},
		{"unshared agent rejected", "stale", "session_fork_forbidden", 7, 8, false, 403},
		{"stale requires human", "stale", "session_fork_requires_human", 9, 7, false, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &forkCreationTestStore{forkTestStore: &forkTestStore{}, status: tc.status, owner: tc.owner}
			hub := NewHub(db, nil)
			client := &Client{uid: 9, accountType: types.AccountBot, bodyID: "body-test", connectionID: "connection-test", send: make(chan []byte, 4)}
			if !tc.offline {
				if _, err := hub.bodyLeases.acquire(9, client.bodyID, client.connectionID); err != nil {
					t.Fatal(err)
				}
				hub.addRegisteredClient(client)
			}
			r := httptest.NewRequest(http.MethodPost, "/api/conversations/fork", strings.NewReader(`{"request_id":"one","source_topic_id":"p2p_7_9"}`))
			r = r.WithContext(context.WithValue(r.Context(), uidKey, tc.actor))
			w := httptest.NewRecorder()
			NewConversationHandler(db, hub).HandleFork(w, r)
			if w.Code != tc.wantCode {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			if tc.wantError != "" {
				if len(db.created) != 0 || !strings.Contains(w.Body.String(), `"error":"`+tc.wantError+`"`) {
					t.Fatalf("rejected request created a fork or returned wrong error: %s", w.Body.String())
				}
				select {
				case raw := <-client.send:
					t.Fatalf("rejected request dispatched: %s", raw)
				default:
				}
				return
			}
			if len(db.created) != 1 || db.created[0].ActorUserID != 7 || db.created[0].AgentUID != 9 || db.created[0].BodyID != client.bodyID {
				t.Fatalf("creation lost actor/agent/current body: %+v", db.created)
			}
			if !strings.Contains(w.Body.String(), `"state":"pending"`) {
				t.Fatalf("fork must await runtime validation: %s", w.Body.String())
			}
			select {
			case raw := <-client.send:
				var envelope ServerMessage
				if err := json.Unmarshal(raw, &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.SessionFork == nil || envelope.SessionFork.SourceTopicID != "p2p_7_9" || envelope.SessionFork.TargetTopicID != "grp_20" || envelope.SessionFork.RequestID != "one" {
					t.Fatalf("missing runtime fork control: %s", raw)
				}
			default:
				t.Fatal("missing runtime dispatch for local idle/interruption checks")
			}
		})
	}
}

func TestSessionForkSharedAgentAccess(t *testing.T) {
	for _, tc := range []struct {
		name, topic    string
		friend, member bool
		group          *types.Group
		allowed        bool
	}{
		{"friend direct", "p2p_7_9", true, false, nil, true},
		{"revoked friend", "p2p_7_9", false, false, nil, false},
		{"another user direct", "p2p_8_9", true, false, nil, false},
		{"human direct", "p2p_7_8", true, false, nil, false},
		{"friend task", "grp_20", true, true, &types.Group{ID: 20, OwnerID: 7, Kind: types.GroupKindAgentTask, MemberCount: 2, AgentIDs: []int64{9}}, true},
		{"ordinary two participant group", "grp_20", true, true, &types.Group{ID: 20, OwnerID: 9, MemberCount: 2, AgentIDs: []int64{9}}, true},
		{"not a member", "grp_20", true, false, &types.Group{ID: 20, MemberCount: 2, AgentIDs: []int64{9}}, false},
		{"multiple humans", "grp_20", true, true, &types.Group{ID: 20, MemberCount: 3, AgentIDs: []int64{9}}, false},
		{"multiple bots", "grp_20", true, true, &types.Group{ID: 20, MemberCount: 3, AgentIDs: []int64{8, 9}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &forkCreationTestStore{forkTestStore: &forkTestStore{}, owner: 8, friend: tc.friend, group: tc.group, member: tc.member}
			agent, err := NewConversationHandler(db, nil).forkSourceAgent(7, tc.topic)
			if tc.allowed {
				if err != nil || agent != 9 {
					t.Fatalf("agent=%d error=%v", agent, err)
				}
			} else if !errors.Is(err, store.ErrSessionForkForbidden) {
				t.Fatalf("unexpected permission: %d %v", agent, err)
			}
		})
	}
}

func TestSessionForkGetIsUserScoped(t *testing.T) {
	db := &forkTestStore{record: &types.SessionFork{ActorUserID: 7, RequestID: "one", TargetTopicID: "grp_20", State: "pending"}}
	h := NewConversationHandler(db, nil)
	for _, tc := range []struct {
		uid  int64
		code int
	}{{7, 200}, {8, 404}} {
		r := httptest.NewRequest(http.MethodGet, "/api/conversations/fork?request_id=one", nil)
		r = r.WithContext(context.WithValue(r.Context(), uidKey, tc.uid))
		w := httptest.NewRecorder()
		h.HandleFork(w, r)
		if w.Code != tc.code {
			t.Fatalf("uid %d: %d %s", tc.uid, w.Code, w.Body.String())
		}
	}
}

func TestSessionForkCancelClosesOnlyAPendingCopyOfTheCaller(t *testing.T) {
	for _, tc := range []struct {
		name, request, state, initialError, wantState, wantError string
		uid                                                      int64
		wantCode                                                 int
	}{
		{"owner cancels pending", "one", "pending", "", "failed", types.SessionForkErrorCancelled, 7, 200},
		{"another user cannot cancel", "one", "pending", "", "pending", "", 8, 404},
		{"unknown request", "missing", "pending", "", "pending", "", 7, 404},
		{"ready is terminal", "one", "ready", "", "ready", "", 7, 200},
		{"failed keeps its reason", "one", "failed", "already_failed", "failed", "already_failed", 7, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &forkTestStore{record: &types.SessionFork{ActorUserID: 7, AgentUID: 9, RequestID: "one",
				SourceTopicID: "p2p_7_9", TargetTopicID: "grp_20", State: tc.state, Error: tc.initialError}}
			r := httptest.NewRequest(http.MethodDelete, "/api/conversations/fork?request_id="+tc.request, nil)
			r = r.WithContext(context.WithValue(r.Context(), uidKey, tc.uid))
			w := httptest.NewRecorder()
			NewConversationHandler(db, nil).HandleFork(w, r)
			if w.Code != tc.wantCode {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			if db.record.State != tc.wantState || db.record.Error != tc.wantError {
				t.Fatalf("record %q/%q want %q/%q", db.record.State, db.record.Error, tc.wantState, tc.wantError)
			}
		})
	}
}

func TestSessionForkCancelRequiresRequestID(t *testing.T) {
	db := &forkTestStore{record: &types.SessionFork{ActorUserID: 7, RequestID: "one", TargetTopicID: "grp_20", State: "pending"}}
	r := httptest.NewRequest(http.MethodDelete, "/api/conversations/fork", nil)
	r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
	w := httptest.NewRecorder()
	NewConversationHandler(db, nil).HandleFork(w, r)
	if w.Code != http.StatusBadRequest || db.record.State != "pending" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
}

func TestSessionForkAckTimeoutConfig(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"", sessionForkAckTimeoutDefault},
		{"90", 90 * time.Second},
		{"0", sessionForkAckTimeoutDefault},
		{"-5", sessionForkAckTimeoutDefault},
		{"abc", sessionForkAckTimeoutDefault},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("CATS_SESSION_FORK_ACK_TIMEOUT_SECONDS", tc.raw)
			if got := sessionForkAckTimeout(); got != tc.want {
				t.Fatalf("timeout=%v want %v", got, tc.want)
			}
		})
	}
}

func TestSessionForkPendingRejectsHTTPAndWebSocket(t *testing.T) {
	db := &forkTestStore{}
	h := NewHub(db, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(`{"topic_id":"grp_20","content":"hello"}`))
	r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
	w := httptest.NewRecorder()
	NewMessageHandler(db, h).HandleSendMessage(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	client := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	h.handlePub(client, &MsgClientPub{ID: "m", Topic: "grp_20", Content: []byte(`"hello"`)})
	select {
	case response := <-client.send:
		if !strings.Contains(string(response), `"code":409`) {
			t.Fatalf("WS %s", response)
		}
	default:
		t.Fatal("missing rejection")
	}
}

func TestSessionForkAuthSeparationAndBotProvenance(t *testing.T) {
	db := &forkTestStore{apiKey: GenerateAPIKey(9), record: &types.SessionFork{ActorUserID: 7, AgentUID: 9, RequestID: "one", SourceTopicID: "p2p_7_9", TargetTopicID: "grp_20", State: "pending"}}
	h := NewConversationHandler(db, nil)
	jwt, err := GenerateToken(7, "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body, token, key string
		handler                              http.HandlerFunc
		want                                 int
	}{
		{"user JWT get", "GET", "/api/conversations/fork?request_id=one", "", jwt, "", JWTAuthMiddlewareWithDB(db)(h.HandleFork), 200},
		{"bot key denied user route", "GET", "/api/conversations/fork?request_id=one", "", "", db.apiKey, JWTAuthMiddlewareWithDB(db)(h.HandleFork), 401},
		{"JWT cannot complete", "POST", "/api/bot/session-forks/complete", `{"request_id":"one","target_topic_id":"grp_20","state":"ready"}`, jwt, "", BotAPIKeyMiddlewareWithDB(db)(h.HandleForkComplete), 401},
		{"bot completes", "POST", "/api/bot/session-forks/complete", `{"request_id":"one","target_topic_id":"grp_20","state":"ready"}`, "", db.apiKey, BotAPIKeyMiddlewareWithDB(db)(h.HandleForkComplete), 200},
		{"bot provenance", "GET", "/api/bot/session-forks?topic_id=grp_20", "", "", db.apiKey, BotAPIKeyMiddlewareWithDB(db)(h.HandleBotFork), 200},
		{"ordinary topic", "GET", "/api/bot/session-forks?topic_id=grp_21", "", "", db.apiKey, BotAPIKeyMiddlewareWithDB(db)(h.HandleBotFork), 404},
		{"JWT cannot get bot provenance", "GET", "/api/bot/session-forks?topic_id=grp_20", "", jwt, "", BotAPIKeyMiddlewareWithDB(db)(h.HandleBotFork), 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.key != "" {
				r.Header.Set("Authorization", "ApiKey "+tc.key)
			}
			r.Header.Set(botBodyIDHeader, "body-test")
			w := httptest.NewRecorder()
			tc.handler(w, r)
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.want == 404 && !strings.Contains(w.Body.String(), `"error":"session_fork_not_found"`) {
				t.Fatalf("restore sentinel %s", w.Body.String())
			}
		})
	}
	if db.completeBody != "body-test" {
		t.Fatal("completion body header not forwarded")
	}
}

func TestSessionForkRetryReturnsTerminalWithoutSourceOrOnlineLookup(t *testing.T) {
	for _, state := range []string{"ready", "failed", "pending"} {
		t.Run(state, func(t *testing.T) {
			db := &forkTestStore{record: &types.SessionFork{ActorUserID: 7, AgentUID: 9, RequestID: "same", SourceTopicID: "p2p_7_9", TargetTopicID: "grp_20", State: state}}
			r := httptest.NewRequest("POST", "/api/conversations/fork", strings.NewReader(`{"request_id":"same","source_topic_id":"p2p_7_9"}`))
			r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
			w := httptest.NewRecorder()
			NewConversationHandler(db, nil).HandleFork(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"`+state+`"`) {
				t.Fatalf("retry %d %s", w.Code, w.Body.String())
			}
			r = httptest.NewRequest("POST", "/api/conversations/fork", strings.NewReader(`{"request_id":"same","source_topic_id":"p2p_7_9","name":"different"}`))
			r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
			w = httptest.NewRecorder()
			NewConversationHandler(db, nil).HandleFork(w, r)
			if w.Code != 409 {
				t.Fatalf("payload conflict %d", w.Code)
			}
		})
	}
}

func TestSessionForkControlIsNotAChatMessage(t *testing.T) {
	db := &forkTestStore{}
	hub := NewHub(db, nil)
	if _, err := hub.bodyLeases.acquire(9, "body-test", "connection-test"); err != nil {
		t.Fatal(err)
	}
	client := &Client{uid: 9, accountType: types.AccountBot, bodyID: "body-test", connectionID: "connection-test", send: make(chan []byte, 4)}
	hub.clients[9] = map[*Client]struct{}{client: {}}
	f := &types.SessionFork{ActorUserID: 7, AgentUID: 9, BodyID: "body-test", RequestID: "one", SourceTopicID: "p2p_7_9", TargetTopicID: "grp_20", State: "pending", SourceLastSeq: 31, MessageIDMap: map[string]int64{"30": 40, "31": 41}}
	NewConversationHandler(db, hub).dispatchSessionFork(f)
	select {
	case raw := <-client.send:
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope) != 1 || envelope["session_fork"] == nil {
			t.Fatalf("chat side effect: %s", raw)
		}
		var control types.SessionForkControl
		if err := json.Unmarshal(envelope["session_fork"], &control); err != nil {
			t.Fatal(err)
		}
		if control.SourceLastSeq != 31 || control.MessageIDMap["30"] != 40 || control.ActorUserID != 7 || control.AgentUID != 9 {
			t.Fatalf("wire contract %#v", control)
		}
	default:
		t.Fatal("missing bot control")
	}
	f.BodyID = "another-body"
	NewConversationHandler(db, hub).dispatchSessionFork(f)
	select {
	case <-client.send:
		t.Fatal("control sent to wrong body")
	default:
	}
}

func TestSessionForkFenceCoversTransientAndStreamPublish(t *testing.T) {
	db := &forkTestStore{}
	hub := NewHub(db, nil)
	for _, kind := range []string{"stream_delta", "stream_cancel", "task_status", "runtime_plan"} {
		t.Run(kind, func(t *testing.T) {
			client := &Client{uid: 9, accountType: types.AccountBot, send: make(chan []byte, 4)}
			hub.handlePub(client, &MsgClientPub{ID: "x", Topic: "grp_20", Type: kind, Content: []byte(`"ignored"`), Metadata: map[string]interface{}{"stream_id": "stream"}})
			select {
			case raw := <-client.send:
				if !strings.Contains(string(raw), `"code":409`) {
					t.Fatalf("WS %s", raw)
				}
			default:
				t.Fatal("missing rejection")
			}
			body := `{"topic_id":"grp_20","content":"ignored","type":"` + kind + `"}`
			r := httptest.NewRequest("POST", "/api/messages/send", strings.NewReader(body))
			r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(9)))
			w := httptest.NewRecorder()
			NewMessageHandler(db, hub).HandleSendMessage(w, r)
			if w.Code != 409 {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSessionForkHistoryExposesRemappedReplies(t *testing.T) {
	hub := NewHub(&forkTestStore{}, nil)
	message := &types.Message{ID: 101, TopicID: "grp_20", FromUID: 9, Content: "reply", MsgType: "text", ReplyTo: 100}
	ws := hub.historyMessageDataForRecipient(7, message)
	if ws.ReplyTo != 100 {
		t.Fatalf("WebSocket reply = %d", ws.ReplyTo)
	}
	httpMessage := hub.historyAPIMessageForRecipient(7, message)
	if httpMessage["reply_to"] != int(100) {
		t.Fatalf("HTTP reply = %#v", httpMessage["reply_to"])
	}
}

// The HTTP status is what the client keys its wording off: a 4xx means the copy
// never began, so the notice can say so and drop the stored request instead of
// reporting an unconfirmed result. Each reason therefore has to map to the status
// the client expects, and an anchor verdict in particular must stay a 409 rather
// than falling through to the generic 500.
func TestSessionForkWriteMapsEveryReasonToItsStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
		body string
	}{
		{"anchor is a rejection", store.ErrSessionForkAnchor, 409, "session_fork_anchor_not_turn_end"},
		{"source unavailable", store.ErrSessionForkSource, 409, "session_fork_source_unavailable"},
		{"request conflict", store.ErrSessionForkConflict, 409, "session_fork_request_conflict"},
		{"forbidden", store.ErrSessionForkForbidden, 403, "session_fork_forbidden"},
		{"not found", store.ErrSessionForkNotFound, 404, "session_fork_not_found"},
		{"unknown stays generic", errors.New("boom"), 500, "session_fork_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeSessionFork(w, nil, tc.err)
			if w.Code != tc.code {
				t.Fatalf("status = %d, want %d", w.Code, tc.code)
			}
			if !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("body %q does not carry %q", w.Body.String(), tc.body)
			}
		})
	}
}
