package server

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openchat/openchat/server/store/types"
)

// Exercise the real HTTP auth/launch/bound-message/history handlers and normal
// metadata/idempotency store interface, not an endpoint that only records JSON.
type openBindingTestStore struct {
	*gatewayAnnotationFakeStore
	mu        sync.Mutex
	durable   []*types.Message
	clientIDs map[string]int64
	replies   map[int64]int64
}

func (s *openBindingTestStore) CreateTopic(id, kind string, owner int64) error { return nil }
func (s *openBindingTestStore) SaveMessageWithMetadata(topic string, uid int64, content string, blocks []types.ContentBlock, mode, role, kind string, reply int64, clientID string, metadata map[string]interface{}) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s/%d/%s", topic, uid, clientID)
	if id, ok := s.clientIDs[key]; ok {
		return id, true, nil
	}
	id := int64(len(s.durable) + 1)
	if s.clientIDs == nil {
		s.clientIDs = make(map[string]int64)
		s.replies = make(map[int64]int64)
	}
	s.clientIDs[key], s.replies[id] = id, reply
	s.durable = append(s.durable, &types.Message{ID: id, TopicID: topic, FromUID: uid, Content: content, ContentBlocks: blocks, Mode: mode, Role: role, MsgType: kind, Metadata: metadata})
	return id, false, nil
}
func (s *openBindingTestStore) GetLatestMessages(topic string, limit, offset int) ([]*types.Message, error) {
	return s.GetMessages(topic, limit, offset)
}

func (s *openBindingTestStore) GetMessages(topic string, limit, offset int) ([]*types.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []*types.Message
	for _, msg := range s.durable {
		if msg.TopicID == topic {
			result = append(result, msg)
		}
	}
	return result, nil
}

type openBindingFixture struct {
	db           *openBindingTestStore
	hub          *Hub
	bindings     *ArtifactOpenBindingHandler
	launch       *ArtifactLaunchHandler
	platform     *httptest.Server
	mux          *http.ServeMux
	gateway      *httptest.Server
	token        string
	otherSession string
	otherActor   string
	appOwner     string
	appURL       string
	launchApp    string
	launchNext   string
	launchCalls  int
	appCalls     int
	onApps       func()
	now          time.Time
}

func newOpenBindingFixture(t *testing.T) *openBindingFixture {
	t.Helper()
	f := &openBindingFixture{appOwner: "9", launchApp: "board", launchNext: "/board/", now: time.Now().UTC()}
	f.db = &openBindingTestStore{gatewayAnnotationFakeStore: &gatewayAnnotationFakeStore{
		users:        map[int64]*types.User{7: gatewayAnnotationHuman(7), 8: gatewayAnnotationHuman(8), 9: gatewayAnnotationBot(9), 11: gatewayAnnotationBot(11)},
		botOwners:    map[int64]int64{9: 7, 11: 7},
		groupMembers: map[string]bool{}, groupMuted: map[string]bool{}, groups: map[int64]*types.Group{}, members: map[int64][]*types.GroupMember{},
	}}
	f.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-test" {
			t.Error("gateway control authentication missing")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case artifactAppsGatewayPath:
			f.appCalls++
			if f.onApps != nil {
				f.onApps()
			}
			writeJSON(w, 200, map[string]interface{}{"apps": []artifactApp{{ID: "board", Agent: f.appOwner, URL: f.appURL}, {ID: "other", Agent: "11", URL: f.gateway.URL + "/other/"}}})
		case artifactLaunchPath:
			f.launchCalls++
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "aob_") || strings.Contains(string(body), "auth_session") {
				t.Error("binding secret leaked to gateway")
			}
			writeJSON(w, 201, ArtifactLaunchResult{AppID: f.launchApp, Code: "one-time", ExpiresAt: f.now.Add(time.Minute).Format(time.RFC3339), LaunchURL: f.gateway.URL + "/_launch/one-time?next=" + f.launchNext})
		default:
			w.WriteHeader(404)
		}
	}))
	f.appURL = f.gateway.URL + "/board/"
	f.hub = NewHub(f.db, nil)
	apps := &ArtifactAppsHandler{gatewayURL: f.gateway.URL, token: "control-test", httpClient: f.gateway.Client()}
	f.hub.SetGatewayAnnotationsAppResolver(apps)
	messages := NewMessageHandler(f.db, f.hub)
	f.bindings = NewArtifactOpenBindingHandler(f.hub, messages, apps)
	f.bindings.now = func() time.Time { return f.now }
	f.launch = &ArtifactLaunchHandler{gatewayURL: f.gateway.URL, launchOrigins: []string{f.gateway.URL}, controlToken: "control-test", httpClient: f.gateway.Client()}
	f.launch.SetOpenBindingHandler(f.bindings)
	mux := http.NewServeMux()
	f.mux = mux
	auth := JWTAuthMiddlewareWithDB(f.db)
	mux.HandleFunc("POST /api/artifacts/launch", auth(f.launch.HandleLaunch))
	mux.HandleFunc("POST /api/artifacts/annotations", auth(f.bindings.HandleAnnotations))
	mux.HandleFunc("DELETE /api/artifacts/open-bindings/{open_ref}", auth(f.bindings.HandleRevoke))
	mux.HandleFunc("GET /api/messages", AuthMiddlewareWithDB(f.db)(messages.HandleGetMessages))
	mux.HandleFunc("POST /api/messages/send", auth(messages.HandleSendMessage))
	uploads := NewUploadHandler(t.TempDir(), "/uploads")
	f.bindings.SetUploadHandler(uploads)
	mux.HandleFunc("POST /api/upload", auth(uploads.HandleUpload))
	mux.HandleFunc("/uploads/", uploads.HandleServeFile)
	f.platform = httptest.NewServer(mux)
	var err error
	f.token, err = GenerateToken(7, "human7", "")
	if err != nil {
		t.Fatal(err)
	}
	f.otherSession, err = GenerateToken(7, "human7", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.otherSession == f.token {
		t.Fatal("distinct login sessions minted identical tokens")
	}
	f.otherActor, err = GenerateToken(8, "human8", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.platform.Close)
	t.Cleanup(f.gateway.Close)
	return f
}

func (f *openBindingFixture) request(t *testing.T, method, path, token string, body interface{}, want int) map[string]interface{} {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, f.platform.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := f.platform.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.StatusCode, want, data)
	}
	if path != "/api/messages" && !strings.HasPrefix(path, "/api/messages?") && response.Header.Get("Cache-Control") != "no-store" && want != 401 && want != 403 {
		t.Errorf("binding response must be no-store: %v", response.Header)
	}
	var out map[string]interface{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (f *openBindingFixture) open(t *testing.T, topic string) ArtifactOpenBinding {
	t.Helper()
	result := f.request(t, "POST", "/api/artifacts/launch", f.token, map[string]interface{}{"app": "board", "topic_id": topic}, 200)
	raw, _ := json.Marshal(result["open_binding"])
	var binding ArtifactOpenBinding
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatal(err)
	}
	if binding.ContractVersion != ArtifactOpenBindingContractV1 || !strings.HasPrefix(binding.OpenRef, "aob_") || binding.TopicID != topic || binding.AgentUID != 9 || binding.AppID != "board" || binding.AppOrigin != f.gateway.URL {
		t.Fatalf("bad binding: %+v", binding)
	}
	if strings.Contains(result["launch_url"].(string), binding.OpenRef) {
		t.Fatal("open_ref in launch URL")
	}
	return binding
}
func boundPayload(binding ArtifactOpenBinding, id string) map[string]interface{} {
	return map[string]interface{}{"open_ref": binding.OpenRef, "client_msg_id": id, "content": "请调整这个按钮", "reply_to": 3,
		"gateway_annotations": gatewayAnnotationRequest(binding.AppID, float64(binding.AgentUID), nil)}
}

func TestArtifactOpenBindingHTTPOriginalTopicIsolationIdempotencyDelivery(t *testing.T) {
	f := newOpenBindingFixture(t)
	sender := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 16)}
	agent := &Client{uid: 9, accountType: types.AccountBot, send: make(chan []byte, 16)}
	otherAgent := &Client{uid: 11, accountType: types.AccountBot, send: make(chan []byte, 16)}
	f.hub.addClient(sender)
	f.hub.addClient(agent)
	f.hub.addClient(otherAgent)
	a := f.open(t, "p2p_7_9")
	a2 := f.open(t, "p2p_7_9")
	if a.OpenRef == a2.OpenRef {
		t.Fatal("separate opens reused reference")
	}
	// A second tab can open another topic even with the same gateway viewer
	// identity; it never changes the routing of A's open reference.
	f.appOwner = "11"
	b := f.openForApp(t, "other", "p2p_7_11")
	f.appOwner = "9"
	payload := boundPayload(a, "client-a")
	payload["topic_id"] = b.TopicID // untrusted destination must not override A
	response := f.request(t, "POST", "/api/artifacts/annotations", f.token, payload, 200)
	if response["topic_id"] != a.TopicID || response["seq_id"] != float64(1) || response["duplicate"] != false {
		t.Fatalf("wrong response: %v", response)
	}
	duplicate := f.request(t, "POST", "/api/artifacts/annotations", f.token, payload, 200)
	if duplicate["seq_id"] != response["seq_id"] || duplicate["duplicate"] != true || len(f.db.durable) != 1 {
		t.Fatalf("not idempotent: %v", duplicate)
	}
	stored := f.db.durable[0]
	if stored.TopicID != a.TopicID || stored.FromUID != 7 || stored.Content != "请调整这个按钮" || f.db.replies[stored.ID] != 3 || strings.Contains(fmt.Sprint(stored.Metadata), a.OpenRef) || stored.Metadata[gatewayAnnotationsAgentContextKey] != nil {
		t.Fatalf("wrong canonical persistence: %+v", stored)
	}
	var delivered ServerMessage
	if err := json.Unmarshal(<-agent.send, &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Data.Topic != a.TopicID || !strings.Contains(xiaoBaEquivalentUserInput(delivered.Data), "Gateway 标注") || !strings.Contains(delivered.Data.Content.(string), "请调整这个按钮") {
		t.Fatalf("Agent not given readable original-topic text: %+v", delivered.Data)
	}
	if len(agent.send) != 0 || len(otherAgent.send) != 0 {
		t.Fatal("duplicate or wrong Agent fanout")
	}
	var human ServerMessage
	if err := json.Unmarshal(<-sender.send, &human); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(human.Data.Content), "Gateway 标注") {
		t.Fatal("Agent context leaked to human")
	}
	// Refreshing a frame does not mint/rebind a reference on the server.
	refreshed := boundPayload(a, "client-after-frame-refresh")
	f.request(t, "POST", "/api/artifacts/annotations", f.token, refreshed, 200)
	f.request(t, "POST", "/api/artifacts/annotations", f.token, boundPayload(b, "client-b"), 200)
	if f.db.durable[2].TopicID != b.TopicID {
		t.Fatal("second open crossed topics")
	}
	// Real HTTP read handlers rebuild the same Agent-only readable annotation.
	botToken, _ := GenerateToken(9, "agent9", "")
	agentHistory := f.request(t, "GET", "/api/messages?topic_id="+a.TopicID, botToken, nil, 200)
	humanHistory := f.request(t, "GET", "/api/messages?topic_id="+a.TopicID, f.token, nil, 200)
	if !strings.Contains(fmt.Sprint(agentHistory["messages"]), "Gateway 标注") || strings.Contains(fmt.Sprint(humanHistory["messages"]), "Gateway 标注") {
		t.Fatal("HTTP history lost target-only annotation context")
	}
	wsReplay := f.hub.historyMessageDataForRecipient(9, stored)
	if !strings.Contains(xiaoBaEquivalentUserInput(wsReplay), "Gateway 标注") || !strings.Contains(cloudRestoreEquivalentText(wsReplay), "Gateway 标注") {
		t.Fatal("WS replay lost readable context")
	}
}

func (f *openBindingFixture) openForApp(t *testing.T, app, topic string) ArtifactOpenBinding {
	t.Helper()
	oldApp, oldNext := f.launchApp, f.launchNext
	f.launchApp, f.launchNext = app, "/"+app+"/"
	result := f.request(t, "POST", "/api/artifacts/launch", f.token, map[string]interface{}{"app": app, "topic_id": topic}, 200)
	f.launchApp, f.launchNext = oldApp, oldNext
	raw, _ := json.Marshal(result["open_binding"])
	var binding ArtifactOpenBinding
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestArtifactOpenBindingHTTPRejectsForeignRevokedExpiredAndChangedPermissions(t *testing.T) {
	for _, scenario := range []string{"missing auth", "wrong actor", "wrong session", "revoked", "expired", "restart", "ACL removed", "agent disabled", "app owner changed", "app URL changed", "agent claim changed", "app claim changed", "empty annotations", "missing client ID", "nontext content", "oversized body"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOpenBindingFixture(t)
			binding := f.open(t, "p2p_7_9")
			payload := boundPayload(binding, "rejected")
			token, want := f.token, 403
			switch scenario {
			case "missing auth":
				token = ""
				want = 401
			case "wrong actor":
				token = f.otherActor
			case "wrong session":
				token = f.otherSession
			case "revoked":
				f.request(t, "DELETE", "/api/artifacts/open-bindings/"+binding.OpenRef, f.token, nil, 204)
				f.request(t, "DELETE", "/api/artifacts/open-bindings/"+binding.OpenRef, f.token, nil, 204)
			case "expired":
				f.now = binding.ExpiresAt
			case "restart":
				f.bindings.store = newArtifactOpenBindingStore()
			case "ACL removed":
				f.db.botOwners[9] = 8
			case "agent disabled":
				f.db.users[9].State = 1
			case "app owner changed":
				f.appOwner = "11"
			case "app URL changed":
				f.appURL = "https://evil.example/board/"
				want = 502
			case "agent claim changed":
				payload["gateway_annotations"] = gatewayAnnotationRequest("board", 11, nil)
			case "app claim changed":
				payload["gateway_annotations"] = gatewayAnnotationRequest("other", 9, nil)
			case "empty annotations":
				payload["gateway_annotations"].(map[string]interface{})["annotations"] = []interface{}{}
				want = 400
			case "missing client ID":
				delete(payload, "client_msg_id")
				want = 400
			case "nontext content":
				payload["content"] = map[string]interface{}{"type": "task_status"}
				want = 400
			case "oversized body":
				payload["content"] = strings.Repeat("x", artifactBoundSubmitMaxBody)
				want = 413
			}
			f.request(t, "POST", "/api/artifacts/annotations", token, payload, want)
			if len(f.db.durable) != 0 {
				t.Fatal("rejected binding persisted a message")
			}
		})
	}
}

func TestArtifactOpenBindingHTTPRevokeCannotCrossActorOrSession(t *testing.T) {
	f := newOpenBindingFixture(t)
	binding := f.open(t, "p2p_7_9")
	for _, token := range []string{f.otherActor, f.otherSession} {
		f.request(t, "DELETE", "/api/artifacts/open-bindings/"+binding.OpenRef, token, nil, 403)
	}
	f.request(t, "DELETE", "/api/artifacts/open-bindings/aob_unknown", f.token, nil, 403)
	f.request(t, "POST", "/api/artifacts/annotations", f.token, boundPayload(binding, "still-valid"), 200)
}

func TestArtifactOpenBindingHTTPLaunchPermissionAndCanonicalGateway(t *testing.T) {
	for _, scenario := range []string{"foreign topic", "unknown topic", "noncanonical topic", "wrong app owner", "foreign app URL", "wrong app URL path", "URL query", "upstream app mismatch", "upstream redirect mismatch", "permission store missing"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOpenBindingFixture(t)
			topic, want := "p2p_7_9", 403
			switch scenario {
			case "foreign topic":
				topic = "p2p_8_9"
				want = 400
			case "unknown topic":
				topic = "p2p_7_99"
			case "noncanonical topic":
				topic = "p2p_9_7"
				want = 400
			case "wrong app owner":
				f.appOwner = "11"
			case "foreign app URL":
				f.appURL = "https://evil.example/board/"
				want = 502
			case "wrong app URL path":
				f.appURL = f.gateway.URL + "/other/"
				want = 502
			case "URL query":
				f.appURL += "?open_ref=leak"
				want = 502
			case "upstream app mismatch":
				f.launchApp = "other"
				want = 502
			case "upstream redirect mismatch":
				f.launchNext = "/other/"
				want = 502
			case "permission store missing":
				f.launch.openBindings = nil
				want = 503
			}
			f.request(t, "POST", "/api/artifacts/launch", f.token, map[string]interface{}{"app": "board", "topic_id": topic}, want)
			if len(f.bindings.store.records) != 0 {
				t.Fatal("invalid launch minted a reference")
			}
			if scenario != "upstream app mismatch" && scenario != "upstream redirect mismatch" && f.launchCalls != 0 {
				t.Fatal("unauthorized launch contacted code issuer")
			}
		})
	}
	f := newOpenBindingFixture(t)
	standalone := f.request(t, "POST", "/api/artifacts/launch", f.token, map[string]interface{}{"app": "board"}, 200)
	if _, present := standalone["open_binding"]; present {
		t.Fatal("identity-only launch gained annotation binding")
	}
}

func TestArtifactOpenBindingHTTPGroupMembershipMuteAndAmbiguity(t *testing.T) {
	f := newOpenBindingFixture(t)
	f.db.groupMembers["2:7"] = true
	f.db.groupMembers["2:9"] = true
	f.db.groups[2] = &types.Group{ID: 2, AgentIDs: []int64{9}}
	f.db.members[2] = []*types.GroupMember{{UserID: 7}, {UserID: 9, IsBot: true}}
	binding := f.open(t, "grp_2")
	groupPayload := boundPayload(binding, "group")
	groupPayload["content_blocks"] = []types.ContentBlock{{Type: "text", Text: "user", Payload: map[string]interface{}{"mentions": []string{"all", "usr11"}}}}
	f.request(t, "POST", "/api/artifacts/annotations", f.token, groupPayload, 200)
	if fmt.Sprint(f.db.durable[0].ContentBlocks[0].Payload["mentions"]) != "[usr9]" {
		t.Fatal("client content blocks retained wrong Agent targets")
	}
	if fmt.Sprint(f.db.durable[0].Metadata["mentions"]) != "[usr9]" {
		t.Fatal("group message missing normal target mention")
	}
	f.db.groupMuted["2:7"] = true
	f.request(t, "POST", "/api/artifacts/annotations", f.token, boundPayload(binding, "muted"), 403)
	f.db.groupMuted["2:7"] = false
	f.db.groupMembers["2:9"] = false
	f.request(t, "POST", "/api/artifacts/annotations", f.token, boundPayload(binding, "agent-left"), 403)
	f.db.groupMembers["2:9"] = true
	f.db.groupMembers["2:11"] = true
	f.db.members[2] = append(f.db.members[2], &types.GroupMember{UserID: 11, IsBot: true})
	// Keep stale single-Agent config: actual membership must still be checked.
	f.db.groups[2].AgentIDs = []int64{9}
	f.request(t, "POST", "/api/artifacts/annotations", f.token, boundPayload(binding, "ambiguous"), 403)
	f.request(t, "POST", "/api/artifacts/launch", f.token, map[string]interface{}{"app": "board", "topic_id": "grp_2"}, 403)
	if len(f.db.durable) != 1 {
		t.Fatal("changed group permission persisted")
	}
}

func TestArtifactOpenBindingStoreCapacityTTLAndRevokeGate(t *testing.T) {
	s := newArtifactOpenBindingStore()
	s.maxEntries = 1
	now := time.Now()
	fingerprint := sha256.Sum256([]byte("session"))
	record := artifactOpenBindingRecord{ArtifactOpenBinding: ArtifactOpenBinding{ExpiresAt: now.Add(time.Minute)}, actorUID: 7, authSessionFingerprint: fingerprint}
	binding, err := s.issue(record, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.issue(record, now); err == nil {
		t.Fatal("store exceeded capacity")
	}
	if _, ok := s.lookup(binding.OpenRef, 7, sha256.Sum256([]byte("other")), now, true); ok {
		t.Fatal("foreign session revoke")
	}
	release, ok := s.persistenceGate(binding.OpenRef, 7, fingerprint, func() time.Time { return now })
	if !ok {
		t.Fatal("valid gate rejected")
	}
	completed := make(chan struct{})
	go func() { s.lookup(binding.OpenRef, 7, fingerprint, now, true); close(completed) }()
	select {
	case <-completed:
		t.Fatal("revoke passed in-progress save")
	case <-time.After(10 * time.Millisecond):
	}
	release()
	<-completed
	if _, ok := s.persistenceGate(binding.OpenRef, 7, fingerprint, func() time.Time { return now }); ok {
		t.Fatal("revoked gate accepted")
	}
	record.ExpiresAt = now.Add(2 * time.Minute)
	if _, err := s.issue(record, now.Add(time.Minute)); err != nil {
		t.Fatal("expired entries did not release capacity")
	}
}

func TestArtifactOpenBindingLateRevokeExpiryAndPermissionsBeforeSave(t *testing.T) {
	for _, scenario := range []string{"revoke", "TTL expiry", "actor disabled", "ACL removed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOpenBindingFixture(t)
			binding := f.open(t, "p2p_7_9")
			initialCalls := f.appCalls
			f.onApps = func() {
				// The second app lookup is the normal message ingestion validator;
				// mutate while it is waiting, before the final save gate.
				if f.appCalls != initialCalls+2 {
					return
				}
				switch scenario {
				case "revoke":
					f.bindings.store.lookup(binding.OpenRef, 7, sha256.Sum256([]byte(f.token)), f.now, true)
				case "TTL expiry":
					f.now = binding.ExpiresAt
				case "actor disabled":
					f.db.users[7].State = 1
				case "ACL removed":
					f.db.botOwners[9] = 8
				}
			}
			f.request(t, "POST", "/api/artifacts/annotations", f.token, boundPayload(binding, "late"), 403)
			if len(f.db.durable) != 0 {
				t.Fatal("late invalidation reached persistence")
			}
		})
	}
}

// Opt-in cross-repo integration imports and runs the actual gateway registry
// and code implementation. Ordinary Go HTTP handlers and durable test store are
// unchanged. This is test-only; there is no production fixture endpoint.
func TestArtifactOpenBindingActualGatewayHTTP(t *testing.T) {
	if os.Getenv("CATSCO_GATEWAY_TEST_ROOT") == "" {
		t.Skip("set CATSCO_GATEWAY_TEST_ROOT to the actual gateway repo")
	}
	command := exec.Command("node", "testdata/open-binding-real-gateway.mjs")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		if err := command.Wait(); err != nil {
			t.Errorf("gateway fixture: %v %s", err, stderr.String())
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("gateway failed to start: %s", stderr.String())
	}
	var endpoint struct {
		ControlURL   string `json:"control_url"`
		PublicOrigin string `json:"public_origin"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &endpoint); err != nil {
		t.Fatal(err)
	}
	f := newOpenBindingFixture(t)
	f.launch.gatewayURL = endpoint.ControlURL
	f.launch.controlToken = "control-test-0123456789abcdef-0123456789"
	f.bindings.apps.token = f.launch.controlToken
	f.launch.launchOrigins = []string{endpoint.PublicOrigin}
	f.bindings.apps.gatewayURL = endpoint.ControlURL
	f.launch.SetOpenBindingHandler(f.bindings)
	a := f.openForApp(t, "board", "p2p_7_9")
	b := f.openForApp(t, "other", "p2p_7_11")
	if a.OpenRef == b.OpenRef || a.AppOrigin != endpoint.PublicOrigin || a.TopicID == b.TopicID {
		t.Fatal("real gateway opens crossed identity")
	}
	agent := &Client{uid: 9, accountType: types.AccountBot, send: make(chan []byte, 8)}
	otherAgent := &Client{uid: 11, accountType: types.AccountBot, send: make(chan []byte, 8)}
	f.hub.addClient(agent)
	f.hub.addClient(otherAgent)
	payload := boundPayload(a, "actual-gateway-a")
	payload["topic_id"] = b.TopicID
	first := f.request(t, "POST", "/api/artifacts/annotations", f.token, payload, 200)
	retry := f.request(t, "POST", "/api/artifacts/annotations", f.token, payload, 200)
	if first["topic_id"] != a.TopicID || retry["seq_id"] != first["seq_id"] || retry["duplicate"] != true || len(f.db.durable) != 1 {
		t.Fatal("real gateway binding/idempotency failed")
	}
	f.request(t, "POST", "/api/artifacts/annotations", f.otherSession, payload, 403)
	f.request(t, "POST", "/api/artifacts/annotations", f.token, boundPayload(b, "actual-gateway-b"), 200)
	if len(agent.send) != 1 || len(otherAgent.send) != 1 || f.db.durable[1].TopicID != b.TopicID {
		t.Fatal("real gateway recipient pub route crossed Agent")
	}
	var delivered ServerMessage
	if err := json.Unmarshal(<-agent.send, &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Data.Topic != a.TopicID || !strings.Contains(xiaoBaEquivalentUserInput(delivered.Data), "Gateway 标注") {
		t.Fatal("real gateway target text delivery failed")
	}
	f.request(t, "DELETE", "/api/artifacts/open-bindings/"+a.OpenRef, f.token, nil, 204)
	f.request(t, "POST", "/api/artifacts/annotations", f.token, boundPayload(a, "after-revoke"), 403)
	if f.launchCalls != 0 || f.appCalls != 0 {
		t.Fatal("actual gateway test accidentally used fake registry/code fixture")
	}
}

func TestArtifactOpenBindingAuthExpiryAtSaveAndJWTExpiryBoundsTTL(t *testing.T) {
	f := newOpenBindingFixture(t)
	claims := &JWTClaims{UID: 7, Username: "human7", TokenType: userTokenType, RegisteredClaims: jwt.RegisteredClaims{ID: "short-test", ExpiresAt: jwt.NewNumericDate(time.Now().Add(2 * time.Second))}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	f.token = token
	binding := f.open(t, "p2p_7_9")
	if !binding.ExpiresAt.Equal(claims.ExpiresAt.Time) {
		t.Fatal("open TTL outlived JWT")
	}
	// Hold the ordinary app lookup until actual JWT expiry. The fixture's
	// binding clock stays at launch time, so only auth revalidation can reject.
	initialCalls := f.appCalls
	f.onApps = func() {
		if f.appCalls == initialCalls+2 {
			time.Sleep(time.Until(claims.ExpiresAt.Time) + 50*time.Millisecond)
		}
	}
	f.request(t, "POST", "/api/artifacts/annotations", f.token, boundPayload(binding, "expired-auth"), 403)
	if len(f.db.durable) != 0 {
		t.Fatal("invalidated JWT persisted after gateway wait")
	}
}

// Start the actual Go HTTP fixture for a browser/gateway integration reviewer.
// Enable only explicitly in `go test`; no fixture API ships in server/cmd.
// Endpoint/token output goes to a caller-chosen /tmp file, mode 0600.
func TestArtifactOpenBindingDevHTTPFixture(t *testing.T) {
	output := os.Getenv("CATSCO_OPEN_BINDING_DEV_FIXTURE")
	if output == "" {
		t.Skip("set CATSCO_OPEN_BINDING_DEV_FIXTURE=/tmp/<file>.json")
	}
	if !strings.HasPrefix(output, "/tmp/") {
		t.Fatal("dev fixture output must be under /tmp")
	}
	control := os.Getenv("CATSCO_OPEN_BINDING_GATEWAY_CONTROL_URL")
	origin := os.Getenv("CATSCO_OPEN_BINDING_GATEWAY_PUBLIC_ORIGIN")
	secret := os.Getenv("CATSCO_OPEN_BINDING_GATEWAY_CONTROL_TOKEN")
	if control == "" || origin == "" || len(secret) < 32 {
		t.Fatal("explicit test gateway control URL/public origin/token are required")
	}
	f := newOpenBindingFixture(t)
	f.launch.gatewayURL = control
	f.launch.launchOrigins = []string{origin}
	f.launch.controlToken = secret
	f.bindings.apps.gatewayURL = control
	f.bindings.apps.token = secret
	f.launch.SetOpenBindingHandler(f.bindings)
	// httptest still owns the HTTP server; loopback address is fixed only when
	// requested so the integration host can explicitly allow-list this origin.
	if address := os.Getenv("CATSCO_OPEN_BINDING_DEV_LISTEN"); address != "" {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		f.platform.Close()
		fixed := httptest.NewUnstartedServer(f.mux)
		fixed.Listener.Close()
		fixed.Listener = listener
		fixed.Start()
		f.platform = fixed
		t.Cleanup(fixed.Close)
	}
	metadata := map[string]interface{}{"platform_url": f.platform.URL, "token": f.token, "same_actor_other_session_token": f.otherSession, "other_actor_token": f.otherActor, "actor_uid": 7, "topics": []string{"p2p_7_9", "p2p_7_11"}, "apps": []string{"board", "other"}, "gateway_public_origin": origin}
	data, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(output, data, 0600); err != nil {
		t.Fatal(err)
	}
	// Advance the test clock with real time while serving; never freeze TTL.
	f.bindings.now = time.Now
	duration := 5 * time.Minute
	if raw := os.Getenv("CATSCO_OPEN_BINDING_DEV_DURATION"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 || parsed > 15*time.Minute {
			t.Fatal("fixture duration must be >0 and <=15m")
		}
		duration = parsed
	}
	t.Logf("actual Go fixture ready: %s (%s)", output, f.platform.URL)
	<-time.After(duration)
}
