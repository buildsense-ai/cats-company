package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
	"golang.org/x/net/http2"
)

// A durable-store contract fake: unlike the historical push spy, writes are
// visible to subsequent HTTP requests and worker reads, and CAS is atomic.
type surfacePushStore struct {
	mu   sync.Mutex
	rows map[string]*types.PushSubscription
}

func (s *surfacePushStore) UpsertPushSubscription(_ context.Context, row *types.PushSubscription, limit int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, old := range s.rows {
		if old.UID == row.UID {
			count++
		}
	}
	if s.rows[row.Endpoint] == nil && count >= limit {
		return false, nil
	}
	copy := *row
	s.rows[row.Endpoint] = &copy
	return true, nil
}

func (s *surfacePushStore) ListPushSubscriptions(ctx context.Context, uid int64) ([]*types.PushSubscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []*types.PushSubscription{}
	for _, row := range s.rows {
		if row.UID == uid {
			copy := *row
			rows = append(rows, &copy)
		}
	}
	return rows, nil
}

func (s *surfacePushStore) DeletePushSubscription(_ context.Context, uid int64, endpoint, registration string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row := s.rows[endpoint]; row != nil && row.UID == uid && row.RegistrationID == registration {
		delete(s.rows, endpoint)
	}
	return nil
}

func (s *surfacePushStore) DeletePushSubscriptionsByEndpoint(ctx context.Context, uid int64, endpoint string) error {
	rows, _ := s.ListPushSubscriptions(ctx, uid)
	for _, row := range rows {
		if row.Endpoint == endpoint {
			return s.DeletePushSubscription(ctx, uid, endpoint, row.RegistrationID)
		}
	}
	return nil
}

func (s *surfacePushStore) DeletePushSubscriptionsByRegistrationID(ctx context.Context, uid int64, registration string) error {
	rows, _ := s.ListPushSubscriptions(ctx, uid)
	for _, row := range rows {
		if row.RegistrationID == registration {
			_ = s.DeletePushSubscription(ctx, uid, row.Endpoint, registration)
		}
	}
	return nil
}

func (s *surfacePushStore) CompareAndSwapPushSubscriptionAuth(_ context.Context, uid int64, endpoint, registration, metadata, oldAuth, newAuth string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[endpoint]
	if row == nil || row.UID != uid || row.RegistrationID != registration || row.P256DH != metadata || row.Auth != oldAuth {
		return false, nil
	}
	row.Auth = newAuth
	return true, nil
}

func (s *surfacePushStore) CompareAndSwapPushSubscriptionMetadata(_ context.Context, uid int64, endpoint, registration, oldMetadata, newMetadata, auth string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[endpoint]
	if row == nil || row.UID != uid || row.RegistrationID != registration || row.P256DH != oldMetadata || row.Auth != auth {
		return false, nil
	}
	row.P256DH = newMetadata
	return true, nil
}

func (s *surfacePushStore) DeletePushSubscriptionVersion(_ context.Context, uid int64, endpoint, registration, metadata, auth string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[endpoint]
	if row != nil && row.UID == uid && row.RegistrationID == registration && row.P256DH == metadata && row.Auth == auth {
		delete(s.rows, endpoint)
	}
	return nil
}

type surfaceAccountStore struct {
	conversationTestStore
	users        map[int64]*types.User
	sourceErr    error
	managed      bool
	runs         map[string]*types.ConversationTaskStatus
	readErr      error
	blockRead    func(context.Context) error
	groupFailure string
	groupReads   int
}

func (s *surfaceAccountStore) GetUser(uid int64) (*types.User, error) {
	return s.users[uid], nil
}

func (s *surfaceAccountStore) GetGroupMembers(int64) ([]*types.GroupMember, error) {
	return []*types.GroupMember{{UserID: 7}, {UserID: 8}}, nil
}

func (s *surfaceAccountStore) IsChannelManagedGroup(int64) (bool, error) {
	return s.managed, nil
}

func (s *surfaceAccountStore) GetConversationTaskStatusForSource(topic string, source int64) (*types.ConversationTaskStatus, error) {
	if s.sourceErr != nil {
		return nil, s.sourceErr
	}
	return s.conversationTestStore.GetConversationTaskStatusForSource(topic, source)
}

func (s *surfaceAccountStore) NativeSurfaceReader() store.NativeSurfaceReader { return s }
func (s *surfaceAccountStore) User(ctx context.Context, uid int64) (*types.User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.GetUser(uid)
}
func (s *surfaceAccountStore) GroupAccess(ctx context.Context, gid, uid int64) (bool, bool, error) {
	if s.blockRead != nil {
		if err := s.blockRead(ctx); err != nil {
			return false, false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	if s.readErr != nil {
		return false, false, s.readErr
	}
	if s.groupFailure != "" {
		s.groupReads++
		if s.groupReads == 2 && s.groupFailure == "managed-group" {
			return false, false, errors.New("managed-group lookup failed")
		}
	}
	managed, err := s.IsChannelManagedGroup(gid)
	if err != nil || managed {
		return managed, false, err
	}
	if s.groupFailure == "membership" && s.groupReads == 2 {
		return false, false, errors.New("membership lookup failed")
	}
	member, err := s.IsGroupMember(gid, uid)
	return managed, member, err
}
func (s *surfaceAccountStore) Recipients(ctx context.Context, gid int64) ([]int64, error) {
	members, err := s.GetGroupMembers(gid)
	var out []int64
	for _, member := range members {
		out = append(out, member.UserID)
	}
	return out, err
}
func (s *surfaceAccountStore) PeerAccess(ctx context.Context, uid, peer int64) (bool, error) {
	return NewConversationHandler(s, nil).canUpdateConversationNotificationPreference(uid, p2pTopicID(uid, peer))
}
func (s *surfaceAccountStore) CurrentTask(ctx context.Context, topic string) (*types.ConversationTaskStatus, error) {
	return s.taskStatuses[topic], s.sourceErr
}
func (s *surfaceAccountStore) TaskRun(ctx context.Context, topic string, source int64, run string) (*types.ConversationTaskStatus, error) {
	if s.sourceErr != nil {
		return nil, s.sourceErr
	}
	if s.runs != nil {
		return s.runs[run], nil
	}
	status, err := s.GetConversationTaskStatusForSource(topic, source)
	if status != nil && status.RunID != run {
		return nil, err
	}
	return status, err
}
func (s *surfaceAccountStore) Title(ctx context.Context, uid int64, topic string, gid, peer int64) (string, error) {
	return (&Hub{db: s}).pushNotificationTitle(uid, topic), nil
}
func (s *surfacePushStore) ReplacePushSubscription(ctx context.Context, row, previous *types.PushSubscription, limit int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.rows[row.Endpoint]
	if previous != nil {
		if old == nil || old.UID != previous.UID || old.RegistrationID != previous.RegistrationID || old.P256DH != previous.P256DH || old.Auth != previous.Auth || row.UID != old.UID {
			return false, nil
		}
	} else {
		if old != nil {
			return false, nil
		}
		count := 0
		for _, item := range s.rows {
			if item.UID == row.UID {
				count++
			}
		}
		if count >= limit {
			return false, nil
		}
	}
	copy := *row
	s.rows[row.Endpoint] = &copy
	return true, nil
}

func surfaceFixture(t *testing.T) (*NativeSurfaceHandler, *surfacePushStore, *surfaceAccountStore) {
	t.Helper()
	push, _ := nativePushFixture(t)
	rows := &surfacePushStore{rows: make(map[string]*types.PushSubscription)}
	push.store = rows
	expiry := time.Now().Add(time.Hour)
	status := &types.ConversationTaskStatus{TopicID: "p2p_7_8", RunID: "run-current", SourceUID: 8, State: "running", Summary: "secret summary", Error: "secret tool args", UpdatedAt: time.Now().UTC(), EventUpdatedAt: time.Now().UTC(), ExpiresAt: &expiry}
	peer := &types.User{ID: 8, Username: "private-agent", DisplayName: "private-title", AccountType: types.AccountService}
	db := &surfaceAccountStore{conversationTestStore: conversationTestStore{
		friends:       []*types.User{peer},
		taskStatuses:  map[string]*types.ConversationTaskStatus{"p2p_7_8": status},
		latestByTopic: map[string]*types.Message{"p2p_7_8": {Content: "secret message"}},
	}, users: map[int64]*types.User{7: {ID: 7, AccountType: types.AccountHuman}, 8: peer, 9: {ID: 9, AccountType: types.AccountHuman}}}
	hub := &Hub{db: db, nativePush: push}
	h := NewNativeSurfaceHandler(push, db, hub, NewAgentHandler(db, hub), NewConversationHandler(db, hub))
	return h, rows, db
}

func surfaceRequest(t *testing.T, handler http.HandlerFunc, method string, uid int64, body interface{}, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "/", strings.NewReader(string(data)))
	if uid > 0 {
		r = r.WithContext(context.WithValue(r.Context(), uidKey, uid))
	}
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func activityInput() activityRegistrationRequest {
	return activityRegistrationRequest{Token: strings.Repeat("ab", 32), Environment: "sandbox", RegistrationID: "12345678-1234-1234-1234-123456789abc", TopicID: "p2p_7_8", RunID: "run-current"}
}

func TestNativeSurfaceActivityEnrollmentIsolation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		uid    int64
		mutate func(*activityRegistrationRequest, *surfaceAccountStore)
		want   int
	}{
		{"valid", 7, func(*activityRegistrationRequest, *surfaceAccountStore) {}, 201},
		{"cross-account", 9, func(*activityRegistrationRequest, *surfaceAccountStore) {}, 403},
		{"run-mismatch", 7, func(in *activityRegistrationRequest, _ *surfaceAccountStore) { in.RunID = "old-run" }, 409},
		{"no-membership", 7, func(_ *activityRegistrationRequest, db *surfaceAccountStore) { db.friends = nil }, 403},
		{"disabled-account", 7, func(_ *activityRegistrationRequest, db *surfaceAccountStore) { db.users[7].State = 1 }, 401},
		{"service-account", 8, func(*activityRegistrationRequest, *surfaceAccountStore) {}, 401},
		{"invalid-env", 7, func(in *activityRegistrationRequest, _ *surfaceAccountStore) { in.Environment = "http://evil" }, 400},
		{"invalid-token", 7, func(in *activityRegistrationRequest, _ *surfaceAccountStore) { in.Token = strings.Repeat("z", 64) }, 400},
		{"terminal", 7, func(_ *activityRegistrationRequest, db *surfaceAccountStore) {
			db.taskStatuses["p2p_7_8"].State = "completed"
		}, 409},
		{"expired-task", 7, func(_ *activityRegistrationRequest, db *surfaceAccountStore) {
			past := time.Now().Add(-time.Minute)
			db.taskStatuses["p2p_7_8"].ExpiresAt = &past
		}, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, db := surfaceFixture(t)
			input := activityInput()
			tc.mutate(&input, db)
			w := surfaceRequest(t, h.HandleActivities, "POST", tc.uid, input, "")
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestNativeSurfaceActivityProviderLifecycle(t *testing.T) {
	for _, details := range []bool{false, true} {
		t.Run(fmt.Sprint(details), func(t *testing.T) {
			h, rows, db := surfaceFixture(t)
			input := activityInput()
			input.Details = details
			if w := surfaceRequest(t, h.HandleActivities, "POST", 7, input, ""); w.Code != 201 {
				t.Fatal(w.Code)
			}
			var events []string
			var timestamps []float64
			h.push.client.Transport = apnsRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.sandbox.push.apple.com" || r.Header.Get("apns-topic") != "cc.catsco.ios.push-type.liveactivity" || r.Header.Get("apns-push-type") != "liveactivity" {
					t.Error("incorrect liveactivity route/headers")
				}
				data, _ := io.ReadAll(r.Body)
				if !details && (strings.Contains(string(data), "private-") || strings.Contains(string(data), "secret")) {
					t.Fatal("redacted content leaked in transmitted bytes", string(data))
				}
				var payload struct {
					APS map[string]interface{} `json:"aps"`
				}
				if err := json.Unmarshal(data, &payload); err != nil {
					t.Fatal(err)
				}
				event := payload.APS["event"].(string)
				events = append(events, event)
				timestamps = append(timestamps, payload.APS["timestamp"].(float64))
				content := payload.APS["content-state"].(map[string]interface{})
				for _, key := range []string{"title", "agent", "state", "summary", "completed", "total", "updated", "details"} {
					if _, ok := content[key]; !ok {
						t.Error("missing Swift content state key", key)
					}
				}
				if content["completed"] != float64(0) || content["total"] != float64(0) || content["details"] != details {
					t.Error("invented progress/incorrect details")
				}
				if details && content["summary"] != "secret summary" {
					t.Error("explicit details missing")
				}
				if event == "update" && (payload.APS["stale-date"] == nil || r.Header.Get("apns-priority") != "5") {
					t.Error("missing stale date or wrong update priority")
				}
				if event == "end" && (payload.APS["dismissal-date"] == nil || r.Header.Get("apns-priority") != "10") {
					t.Error("missing dismissal date or wrong end priority")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			status := db.taskStatuses[input.TopicID]
			for range 2 {
				if err := h.push.sendActivityStatus(context.Background(), status); err != nil {
					t.Fatal(err)
				}
			}
			if len(events) != 1 {
				t.Fatal("duplicate running heartbeat", events)
			}
			status.State = "completed"
			status.UpdatedAt = status.UpdatedAt.Add(time.Millisecond)
			status.EventUpdatedAt = status.EventUpdatedAt.Add(time.Millisecond)
			for range 2 {
				if err := h.push.sendActivityStatus(context.Background(), status); err != nil {
					t.Fatal(err)
				}
			}
			if fmt.Sprint(events) != "[update end]" || timestamps[1] <= timestamps[0] {
				t.Fatal("incorrect lifecycle/order", events, timestamps)
			}
			active, _ := rows.ListPushSubscriptions(context.Background(), 7)
			if len(active) != 0 {
				t.Fatal("ended activity retained")
			}
		})
	}
}

func TestNativeSurfaceActivityRevocationExpiryAndSupersession(t *testing.T) {
	for _, reason := range []string{"revoked", "expired", "run-replaced", "membership-revoked", "account-disabled", "invalid-apns-token"} {
		t.Run(reason, func(t *testing.T) {
			h, rows, db := surfaceFixture(t)
			input := activityInput()
			surfaceRequest(t, h.HandleActivities, "POST", 7, input, "")
			calls := 0
			h.push.client.Transport = apnsRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 410, Body: io.NopCloser(strings.NewReader(`{"reason":"Unregistered"}`))}, nil
			})
			event := *db.taskStatuses[input.TopicID]
			switch reason {
			case "revoked":
				savedKey := h.push.key
				h.push.key = nil
				w := surfaceRequest(t, h.HandleActivities, "DELETE", 7, input, "")
				if w.Code != 200 {
					t.Fatal("APNs disabled prevented revoke", w.Code)
				}
				h.push.key = savedKey
			case "expired":
				for _, row := range rows.rows {
					var meta activityRegistrationMetadata
					_ = json.Unmarshal([]byte(row.P256DH), &meta)
					meta.Expires = time.Now().Add(-time.Second).Unix()
					data, _ := json.Marshal(meta)
					row.P256DH = string(data)
				}
			case "run-replaced":
				db.taskStatuses[input.TopicID].RunID = "new-run"
			case "membership-revoked":
				db.friends = nil
			case "account-disabled":
				db.users[7].State = 1
			}
			if err := h.push.sendActivitiesForUser(context.Background(), 7, &event); err != nil {
				t.Fatal(err)
			}
			wantCalls := 0
			if reason == "invalid-apns-token" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatal("unauthorized/stale delivery", calls)
			}
			active, _ := rows.ListPushSubscriptions(context.Background(), 7)
			if len(active) != 0 {
				t.Fatal("invalid activity retained")
			}
		})
	}
}

func TestNativeSurfaceCapabilitySnapshotAndRevocation(t *testing.T) {
	h, rows, db := surfaceFixture(t)
	input := surfaceRegistrationRequest{AccessToken: strings.Repeat("ab", 32), RegistrationID: activityInput().RegistrationID, Scope: strings.Repeat("cd", 32)}
	w := surfaceRequest(t, h.HandleSurfaces, "POST", 7, input, "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var response struct {
		Token   string `json:"access_token"`
		Expires string `json:"expires_at"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if response.Token != "7."+input.AccessToken {
		t.Fatal("incorrect canonical capability", response.Token)
	}
	expires, _ := time.Parse(time.RFC3339, response.Expires)
	if !expires.After(time.Now()) || expires.After(time.Now().Add(surfaceTTL)) {
		t.Fatal("unbounded expiry")
	}
	for _, row := range rows.rows {
		if strings.Contains(row.Endpoint+row.Auth+row.P256DH, input.AccessToken) {
			t.Fatal("raw capability persisted")
		}
	}
	repeated := surfaceRequest(t, h.HandleSurfaces, "POST", 7, input, "")
	if repeated.Body.String() != w.Body.String() || len(rows.rows) != 1 {
		t.Fatal("enrollment is not stable")
	}
	w = surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface "+response.Token)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || len(w.Body.Bytes()) > maxSurfaceResponse {
		t.Fatal("snapshot failed", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), input.Scope) || !strings.Contains(w.Body.String(), `"state":"running"`) {
		t.Fatal("missing scope/task status", w.Body)
	}
	if strings.Contains(w.Body.String(), "secret") || len(db.requestedIDs) != 0 {
		t.Fatal("snapshot accessed/disclosed messages or private status fields", w.Body)
	}
	for _, bad := range []string{"Bearer " + response.Token, "Surface 9." + input.AccessToken, "Surface 7." + strings.Repeat("00", 32), "Surface 007." + input.AccessToken, ""} {
		if w := surfaceRequest(t, h.HandleSnapshot, "GET", 7, nil, bad); w.Code != 401 {
			t.Fatal("capability authentication bypass", bad, w.Code)
		}
	}
	wrongScope := input
	wrongScope.Scope = strings.Repeat("ef", 32)
	if w := surfaceRequest(t, h.HandleSurfaces, "DELETE", 7, wrongScope, ""); w.Code != 403 {
		t.Fatal("scope mismatch permitted", w.Code)
	}
	// Another account can revoke only its own namespace.
	surfaceRequest(t, h.HandleSurfaces, "DELETE", 9, input, "")
	if w := surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface "+response.Token); w.Code != 200 {
		t.Fatal("cross-account revocation affected owner", w.Code)
	}
	db.friends = nil
	w = surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface "+response.Token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"conversations":[]`) {
		t.Fatal("membership not revalidated", w.Code, w.Body)
	}
	db.users[7].State = 1
	if w := surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface "+response.Token); w.Code != 401 {
		t.Fatal("disabled account retained capability")
	}
	db.users[7].State = 0
	h.push.key = nil
	if w := surfaceRequest(t, h.HandleSurfaces, "DELETE", 7, input, ""); w.Code != 200 {
		t.Fatal("disabled APNs prevented capability revoke")
	}
	if w := surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface "+response.Token); w.Code != 401 {
		t.Fatal("revoked capability accepted")
	}
}

func TestNativeSurfaceCapabilityExpiryLimitsAndRotation(t *testing.T) {
	h, rows, _ := surfaceFixture(t)
	input := surfaceRegistrationRequest{AccessToken: strings.Repeat("ab", 32), RegistrationID: activityInput().RegistrationID, Scope: strings.Repeat("cd", 32)}
	surfaceRequest(t, h.HandleSurfaces, "POST", 7, input, "")
	for _, row := range rows.rows {
		meta := surfaceRegistrationMetadata{Scope: input.Scope, Expires: time.Now().Add(-time.Second).Unix()}
		data, _ := json.Marshal(meta)
		row.P256DH = string(data)
	}
	if w := surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface 7."+input.AccessToken); w.Code != 401 {
		t.Fatal("expired capability accepted")
	}
	// Nine ordinary transports plus the expired capability still allow one new
	// activity after lazy cleanup; the combined ten-row limit remains intact.
	for i := range 9 {
		endpoint := fmt.Sprintf("https://example.test/%d", i)
		rows.rows[endpoint] = &types.PushSubscription{UID: 7, Endpoint: endpoint, RegistrationID: "browser-registration"}
	}
	if w := surfaceRequest(t, h.HandleActivities, "POST", 7, activityInput(), ""); w.Code != 201 {
		t.Fatal("expired capability did not release limit", w.Code)
	}
	if w := surfaceRequest(t, h.HandleSurfaces, "POST", 7, input, ""); w.Code != 409 {
		t.Fatal("combined limit weakened", w.Code)
	}
	h, _, _ = surfaceFixture(t)
	surfaceRequest(t, h.HandleSurfaces, "POST", 7, input, "")
	old := input
	input.AccessToken = strings.Repeat("ef", 32)
	surfaceRequest(t, h.HandleSurfaces, "POST", 7, input, "")
	if w := surfaceRequest(t, h.HandleSurfaces, "DELETE", 7, old, ""); w.Code != 403 {
		t.Fatal("old capability revoked rotated registration")
	}
	if w := surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface 7."+old.AccessToken); w.Code != 401 {
		t.Fatal("rotated capability still valid")
	}
}

func TestNativeSurfaceProviderTokenErrors(t *testing.T) {
	h, _, _ := surfaceFixture(t)
	surfaceRequest(t, h.HandleActivities, "POST", 7, activityInput(), "")
	transportErr := errors.New("https://provider.invalid/secret-token")
	h.push.client.Transport = apnsRoundTripper(func(*http.Request) (*http.Response, error) { return nil, transportErr })
	err := h.push.sendActivityStatus(context.Background(), h.db.(*surfaceAccountStore).taskStatuses["p2p_7_8"])
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatal("provider token URL error leaked", err)
	}
}

func TestNativeSurfaceHTTP2RedirectAndDeadlineIntegration(t *testing.T) {
	for _, mode := range []string{"http2", "redirect", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			h, _, db := surfaceFixture(t)
			if w := surfaceRequest(t, h.HandleActivities, "POST", 7, activityInput(), ""); w.Code != 201 {
				t.Fatal(w.Code)
			}
			var calls atomic.Int32
			provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.ProtoMajor != 2 || r.Header.Get("apns-push-type") != "liveactivity" || !strings.HasPrefix(r.Header.Get("Authorization"), "bearer ") {
					t.Error("HTTP/2/provider authorization not used")
				}
				switch mode {
				case "redirect":
					w.Header().Set("Location", "https://untrusted.invalid/token")
					w.WriteHeader(302)
				case "timeout":
					<-r.Context().Done()
				default:
					w.WriteHeader(200)
				}
			})
			transport := &http2.Transport{DialTLSContext: func(ctx context.Context, network, address string, config *tls.Config) (net.Conn, error) {
				if address != "api.sandbox.push.apple.com:443" {
					return nil, errors.New("unexpected provider host")
				}
				clientConn, serverConn := net.Pipe()
				t.Cleanup(func() { clientConn.Close(); serverConn.Close() })
				go (&http2.Server{}).ServeConn(serverConn, &http2.ServeConnOpts{Handler: provider})
				return clientConn, nil
			}}
			t.Cleanup(transport.CloseIdleConnections)
			h.push.client.Transport = apnsRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.sandbox.push.apple.com" {
					return nil, errors.New("provider redirect followed")
				}
				return transport.RoundTrip(r)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			h.push.client.Timeout = 50 * time.Millisecond
			err := h.push.sendActivityStatus(ctx, db.taskStatuses["p2p_7_8"])
			if mode == "http2" && err != nil {
				t.Fatal(err)
			}
			if mode != "http2" && err == nil {
				t.Fatal("provider failure accepted")
			}
			if mode == "redirect" && calls.Load() != 1 {
				t.Fatal("redirect retried/followed", calls.Load())
			}
			if mode == "timeout" && (calls.Load() < 1 || calls.Load() > maxPushProviderAttempts) {
				t.Fatal("timeout retry unbounded", calls.Load())
			}
		})
	}
}

func TestNativeSurfaceAllStatusHooksAreQueuedBeforeAlertFilter(t *testing.T) {
	for _, state := range []string{"running", "waiting", "completed", "failed", "cancelled", "stale", "idle"} {
		t.Run(state, func(t *testing.T) {
			h, _, db := surfaceFixture(t)
			surfaceRequest(t, h.HandleActivities, "POST", 7, activityInput(), "")
			status := db.taskStatuses["p2p_7_8"]
			status.State = state
			started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			h.push.client.Transport = apnsRoundTripper(func(r *http.Request) (*http.Response, error) {
				close(started)
				<-release
				close(finished)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			returned := make(chan struct{})
			go func() {
				h.hub.observeAgentPushTaskStatus(status)
				close(returned)
			}()
			select {
			case <-returned:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("task handler blocked on provider")
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("status filtered before activity dispatch", state)
			}
			close(release)
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("worker did not complete")
			}
			close(h.push.queue)
			close(h.push.activityQueue)
		})
	}
	h, _, db := surfaceFixture(t)
	h.push.workers.Do(func() {}) // Keep the bounded queue deterministic.
	for range maxQueuedPushDeliveries {
		if !h.push.enqueueActivityStatus(db.taskStatuses["p2p_7_8"]) {
			t.Fatal("queue filled early")
		}
	}
	if h.push.enqueueActivityStatus(db.taskStatuses["p2p_7_8"]) {
		t.Fatal("queue is unbounded")
	}
}

func TestNativeSurfaceActivityCrossAccountAndGenerationDeletion(t *testing.T) {
	h, rows, _ := surfaceFixture(t)
	input := activityInput()
	surfaceRequest(t, h.HandleActivities, "POST", 7, input, "")
	surfaceRequest(t, h.HandleActivities, "DELETE", 9, input, "")
	active, _ := rows.ListPushSubscriptions(context.Background(), 7)
	if len(active) != 1 {
		t.Fatal("cross-account activity deletion")
	}
	old := input
	input.RegistrationID = "12345678-4321-4321-4321-123456789abc"
	surfaceRequest(t, h.HandleActivities, "POST", 7, input, "")
	surfaceRequest(t, h.HandleActivities, "DELETE", 7, old, "")
	active, _ = rows.ListPushSubscriptions(context.Background(), 7)
	if len(active) != 1 || active[0].RegistrationID != input.RegistrationID {
		t.Fatal("late teardown removed new generation")
	}
}

func TestNativeSurfaceEnrollmentPreservesWatermarkAndExpiry(t *testing.T) {
	h, rows, db := surfaceFixture(t)
	input := activityInput()
	surfaceRequest(t, h.HandleActivities, "POST", 7, input, "")
	h.push.client.Transport = apnsRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err := h.push.sendActivityStatus(context.Background(), db.taskStatuses[input.TopicID]); err != nil {
		t.Fatal(err)
	}
	old, _ := rows.ListPushSubscriptions(context.Background(), 7)
	if len(old) != 1 || old[0].Auth == "activity-v1" {
		t.Fatal("missing durable delivery watermark")
	}
	for _, details := range []bool{false, true} {
		input.Details = details
		if w := surfaceRequest(t, h.HandleActivities, "POST", 7, input, ""); w.Code != 201 {
			t.Fatal(w.Code)
		}
		current, _ := rows.ListPushSubscriptions(context.Background(), 7)
		var before, after activityRegistrationMetadata
		_ = json.Unmarshal([]byte(old[0].P256DH), &before)
		_ = json.Unmarshal([]byte(current[0].P256DH), &after)
		if current[0].Auth != old[0].Auth || before.Expires != after.Expires || after.Details != details {
			t.Fatal("refresh lost watermark, extended expiry, or ignored privacy choice")
		}
	}
}

func TestNativeSurfaceJWTAndCapabilityHTTPBoundaryIntegration(t *testing.T) {
	h, _, db := surfaceFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/push/ios/surfaces", JWTAuthMiddlewareWithDB(db)(h.HandleSurfaces))
	mux.HandleFunc("/api/push/ios/activities", JWTAuthMiddlewareWithDB(db)(h.HandleActivities))
	mux.HandleFunc("/api/push/ios/snapshot", h.HandleSnapshot)
	jwt, err := GenerateToken(7, "user", "")
	if err != nil {
		t.Fatal(err)
	}
	input := surfaceRegistrationRequest{AccessToken: strings.Repeat("ab", 32), RegistrationID: activityInput().RegistrationID, Scope: strings.Repeat("cd", 32)}
	request := func(method, path, authorization string, body interface{}) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		r.Header.Set("Authorization", authorization)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/api/push/ios/surfaces", "Bearer "+jwt, input); w.Code != 201 {
		t.Fatal("JWT enrollment failed", w.Code, w.Body)
	}
	if w := request("GET", "/api/push/ios/snapshot", "Surface 7."+input.AccessToken, nil); w.Code != 200 || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("capability snapshot failed or issued session cookie", w.Code, w.Body)
	}
	if w := request("GET", "/api/push/ios/snapshot", "Bearer "+jwt, nil); w.Code != 401 {
		t.Fatal("JWT incorrectly accepted for widget snapshot")
	}
	for _, path := range []string{"/api/push/ios/surfaces", "/api/push/ios/activities"} {
		if w := request("POST", path, "Surface 7."+input.AccessToken, input); w.Code != 401 {
			t.Fatal("read-only capability authorized a write", path, w.Code)
		}
	}
}

func TestNativeSurfaceSnapshotBoundsAndStrictRequests(t *testing.T) {
	h, _, db := surfaceFixture(t)
	input := surfaceRegistrationRequest{AccessToken: strings.Repeat("ab", 32), RegistrationID: activityInput().RegistrationID, Scope: strings.Repeat("cd", 32)}
	surfaceRequest(t, h.HandleSurfaces, "POST", 7, input, "")
	db.friends = nil
	db.ownerBots = nil
	for i := int64(10); i < 40; i++ {
		peer := &types.User{ID: i, Username: strings.Repeat("name", 100), AccountType: types.AccountService}
		db.users[i] = peer
		db.friends = append(db.friends, peer)
		db.ownerBots = append(db.ownerBots, map[string]interface{}{"id": i, "username": "agent", "display_name": strings.Repeat("agent", 100), "api_key": "secret"})
	}
	w := surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface 7."+input.AccessToken)
	var snapshot struct {
		Agents        []AgentSummary              `json:"agents"`
		Conversations []types.ConversationSummary `json:"conversations"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || len(snapshot.Agents) != 20 || len(snapshot.Conversations) != 20 || len(w.Body.Bytes()) > maxSurfaceResponse || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("unbounded or private snapshot", w.Code, w.Body)
	}
	for _, conversation := range snapshot.Conversations {
		if len(conversation.Name) > 160 {
			t.Fatal("unbounded name")
		}
	}
	for _, body := range []string{`{"extra":true}`, strings.Repeat(" ", maxPushRequestBody+1) + `{}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
		w := httptest.NewRecorder()
		h.HandleSurfaces(w, r)
		if w.Code != 400 {
			t.Fatal("unbounded/unknown request accepted", w.Code)
		}
	}
}

func TestNativeSurfaceActivityStoreFailureDoesNotRevoke(t *testing.T) {
	h, rows, db := surfaceFixture(t)
	surfaceRequest(t, h.HandleActivities, "POST", 7, activityInput(), "")
	db.sourceErr = errors.New("temporarily unavailable")
	if err := h.push.sendActivityStatus(context.Background(), db.taskStatuses["p2p_7_8"]); err == nil {
		t.Fatal("task store failure not reported")
	}
	active, _ := rows.ListPushSubscriptions(context.Background(), 7)
	if len(active) != 1 {
		t.Fatal("temporary store failure retired valid registration")
	}
}

func TestNativeSurfaceGroupMembershipAndManagedPrivacy(t *testing.T) {
	h, _, db := surfaceFixture(t)
	status := *db.taskStatuses["p2p_7_8"]
	status.TopicID = "grp_80"
	db.taskStatuses[status.TopicID] = &status
	db.groups = []*types.Group{{ID: 80, Name: "Group task", AgentIDs: []int64{8}}}
	db.groupMembership = map[int64]bool{80: true}
	input := activityInput()
	input.TopicID = status.TopicID
	if w := surfaceRequest(t, h.HandleActivities, "POST", 7, input, ""); w.Code != 201 {
		t.Fatal("group enrollment rejected", w.Code)
	}
	db.groupMembership[80] = false
	if w := surfaceRequest(t, h.HandleActivities, "POST", 7, input, ""); w.Code != 403 {
		t.Fatal("former member enrolled activity", w.Code)
	}
	db.groupMembership[80] = true
	db.managed = true
	if w := surfaceRequest(t, h.HandleActivities, "POST", 7, input, ""); w.Code != 403 {
		t.Fatal("managed group exposed to human", w.Code)
	}
	capability := surfaceRegistrationRequest{AccessToken: strings.Repeat("ab", 32), RegistrationID: input.RegistrationID, Scope: strings.Repeat("cd", 32)}
	surfaceRequest(t, h.HandleSurfaces, "POST", 7, capability, "")
	w := surfaceRequest(t, h.HandleSnapshot, "GET", 0, nil, "Surface 7."+capability.AccessToken)
	if w.Code != 200 || strings.Contains(w.Body.String(), "grp_80") {
		t.Fatal("managed group included in widget snapshot", w.Code, w.Body)
	}
}

func TestNativeSurfaceHeartbeatRefreshAndTerminalBurst(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			h, rows, db := surfaceFixture(t)
			input := activityInput()
			surfaceRequest(t, h.HandleActivities, "POST", 7, input, "")
			calls := 0
			h.push.client.Transport = apnsRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			status := db.taskStatuses[input.TopicID]
			if err := h.push.sendActivityStatus(context.Background(), status); err != nil {
				t.Fatal(err)
			}
			for _, row := range rows.rows {
				event, _, digest := activityVersion(row.Auth)
				timestamp := time.Now().Add(-3 * time.Minute).Unix()
				if terminal {
					timestamp = time.Now().Add(30 * time.Second).Unix()
				}
				row.Auth = fmt.Sprintf("activity-v1:%d:%d:%s", event, timestamp, digest)
			}
			if terminal {
				status.State = "completed"
				status.EventUpdatedAt = status.EventUpdatedAt.Add(time.Millisecond)
			}
			if err := h.push.sendActivityStatus(context.Background(), status); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatal("fresh heartbeat or terminal end suppressed", calls)
			}
			if err := h.push.sendActivityStatus(context.Background(), status); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatal("heartbeat/end duplicate", calls)
			}
		})
	}
}

func TestNativeSurfaceNamespacesNeverReachBrowserTransport(t *testing.T) {
	db := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{UID: 7, Endpoint: "apns://production/bundle/token", Auth: "apns-v1"},
		{UID: 7, Endpoint: "activitykit://sandbox/bundle/token", Auth: "activity-v1"},
		{UID: 7, Endpoint: "surface://7/registration-current", Auth: "surface-v1:hash"},
	}}
	service := enabledPushService(db)
	result, err := service.sendToUserFiltered(context.Background(), 7, PushNotification{Body: "ordinary browser message"}, nil, nil)
	if err != nil || result.Attempted != 0 {
		t.Fatal("background namespace reached browser transport", result, err)
	}
}
