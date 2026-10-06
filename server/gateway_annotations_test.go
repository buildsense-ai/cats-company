// Package server — gateway artifact annotation ingestion tests. The suites
// cover the bounded schema, server-canonical identity authorization, the HTTP
// and WebSocket ingestion paths, durable persistence, and the readable context
// the Agent actually receives at fanout.
package server

import (
	"context"
	"encoding/json"
	"errors"

	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func withTestUID(uid int64) context.Context {
	return context.WithValue(context.Background(), uidKey, uid)
}

func jsonUnmarshal(t *testing.T, body string) interface{} {
	t.Helper()
	var value interface{}
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return value
}

func gatewayAnnotationRequest(appID string, agentUID float64, build func(value map[string]interface{})) map[string]interface{} {
	value := map[string]interface{}{
		"contract_version": GatewayAnnotationsContractV1,
		"agent_uid":        agentUID,
		"app_id":           appID,
		"page":             map[string]interface{}{"path": "/board"},
		"annotations": []interface{}{
			map[string]interface{}{
				"id":    "a1",
				"kind":  "element",
				"label": "发布按钮",
				"body":  "这个按钮需要改成蓝色",
				"target": map[string]interface{}{
					"element_id": "submit-btn",
					"selector":   "button#submit",
				},
			},
		},
	}
	if build != nil {
		build(value)
	}
	return value
}

func TestNormalizeGatewayAnnotationsAcceptsEveryKind(t *testing.T) {
	cases := []struct {
		name  string
		value map[string]interface{}
	}{
		{
			name:  "element",
			value: gatewayAnnotationRequest("board", 9, nil),
		},
		{
			name: "element with auxiliary evidence",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				annotation := value["annotations"].([]interface{})[0].(map[string]interface{})
				annotation["target"].(map[string]interface{})["rect"] = map[string]interface{}{
					"x": 0.1, "y": 0.2, "width": 0.3, "height": 0.4,
				}
				annotation["target"].(map[string]interface{})["viewport"] = map[string]interface{}{
					"width": 1280.0, "height": 720.0, "scroll_x": 0.0, "scroll_y": 40.0,
				}
				annotation["target"].(map[string]interface{})["coordinate_space"] = "viewport"
			}),
		},
		{
			name: "text",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				annotation := value["annotations"].([]interface{})[0].(map[string]interface{})
				annotation["kind"] = "text"
				annotation["target"] = map[string]interface{}{
					"text":   "确认弹窗会吞掉第二个请求",
					"prefix": "保存后,",
					"suffix": ",导致数据丢失",
				}
			}),
		},
		{
			name: "region",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				annotation := value["annotations"].([]interface{})[0].(map[string]interface{})
				annotation["kind"] = "region"
				annotation["target"] = map[string]interface{}{
					"rect":             map[string]interface{}{"x": 0.05, "y": 0.1, "width": 0.6, "height": 0.3},
					"coordinate_space": "viewport",
					"viewport":         map[string]interface{}{"width": 1440.0, "height": 900.0, "scroll_x": 0.0, "scroll_y": 0.0},
				}
			}),
		},
		{
			name: "revision",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["page"].(map[string]interface{})["revision"] = "build-r7"
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document, err := normalizeGatewayAnnotations(tc.value)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if document == nil {
				t.Fatal("normalize returned no document")
			}
			if document.AppID != "board" || document.AgentUID != 9 {
				t.Fatalf("identity = (%q, %d), want (board, 9)", document.AppID, document.AgentUID)
			}
			if document.Page.Path != "/board" {
				t.Fatalf("path = %q, want /board", document.Page.Path)
			}
			if len(document.Annotations) != 1 {
				t.Fatalf("annotations = %d, want 1", len(document.Annotations))
			}
			// The canonical value must be persistable as-is.
			if _, err := document.restamped(); err != nil {
				t.Fatalf("restamp: %v", err)
			}
		})
	}
}

func TestNormalizeGatewayAnnotationsRejectsInvalidValues(t *testing.T) {
	body := strings.Repeat("字", gatewayAnnotationsMaxBodyRunes+1)
	cases := []struct {
		name  string
		value map[string]interface{}
	}{
		{
			name:  "wrong contract version",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) { value["contract_version"] = "catsco.gateway-annotations.v2" }),
		},
		{
			name:  "non-integer agent",
			value: gatewayAnnotationRequest("board", 9.5, nil),
		},
		{
			name:  "zero agent",
			value: gatewayAnnotationRequest("board", 0, nil),
		},
		{
			name:  "app id uppercase",
			value: gatewayAnnotationRequest("Board", 9, nil),
		},
		{
			name:  "app id empty",
			value: gatewayAnnotationRequest("", 9, nil),
		},
		{
			name:  "page missing",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) { delete(value, "page") }),
		},
		{
			name: "path without leading slash",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["page"].(map[string]interface{})["path"] = "board/index.html"
			}),
		},
		{
			name: "path with query",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["page"].(map[string]interface{})["path"] = "/board?token=leak"
			}),
		},
		{
			name:  "path with fragment",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) { value["page"].(map[string]interface{})["path"] = "/board#section" }),
		},
		{
			name: "revision too long",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["page"].(map[string]interface{})["revision"] = strings.Repeat("r", gatewayAnnotationsMaxRevisionRunes+1)
			}),
		},
		{
			name:  "annotations not an array",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) { value["annotations"] = "none" }),
		},
		{
			name: "too many annotations",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				many := make([]interface{}, gatewayAnnotationsMaxAnnotations+1)
				for index := range many {
					many[index] = map[string]interface{}{
						"id":     "a" + strconv.Itoa(index),
						"kind":   "element",
						"body":   "review",
						"target": map[string]interface{}{"element_id": "e" + strconv.Itoa(index)},
					}
				}
				value["annotations"] = many
			}),
		},
		{
			name: "body too long",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["body"] = body
			}),
		},
		{
			name: "body empty",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["body"] = "  "
			}),
		},
		{
			name: "label too long",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["label"] = strings.Repeat("字", gatewayAnnotationsMaxLabelRunes+1)
			}),
		},
		{
			name: "missing id",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				delete(value["annotations"].([]interface{})[0].(map[string]interface{}), "id")
			}),
		},
		{
			name: "unknown kind",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["kind"] = "dom"
			}),
		},
		{
			name: "element without anchor",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"] = map[string]interface{}{}
			}),
		},
		{
			name: "text without text",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["kind"] = "text"
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"] = map[string]interface{}{
					"prefix": "前",
				}
			}),
		},
		{
			name: "region without viewport",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["kind"] = "region"
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"] = map[string]interface{}{
					"rect":             map[string]interface{}{"x": 0.1, "y": 0.1, "width": 0.2, "height": 0.2},
					"coordinate_space": "viewport",
				}
			}),
		},
		{
			name: "region without coordinate space",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["kind"] = "region"
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"] = map[string]interface{}{
					"rect":     map[string]interface{}{"x": 0.1, "y": 0.1, "width": 0.2, "height": 0.2},
					"viewport": map[string]interface{}{"width": 800.0, "height": 600.0},
				}
			}),
		},
		{
			name: "rect exceeds viewport",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"].(map[string]interface{})["rect"] = map[string]interface{}{
					"x": 0.8, "y": 0.1, "width": 0.5, "height": 0.2,
				}
			}),
		},
		{
			name: "rect negative",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"].(map[string]interface{})["rect"] = map[string]interface{}{
					"x": -0.1, "y": 0.1, "width": 0.2, "height": 0.2,
				}
			}),
		},
		{
			name: "rect empty area",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"].(map[string]interface{})["rect"] = map[string]interface{}{
					"x": 0.1, "y": 0.1, "width": 0, "height": 0.2,
				}
			}),
		},
		{
			name: "viewport zero size",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"].(map[string]interface{})["viewport"] = map[string]interface{}{
					"width": 0, "height": 600,
				}
			}),
		},
		{
			name: "selector too long",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"].(map[string]interface{})["selector"] =
					strings.Repeat("s", gatewayAnnotationsMaxSelectorRunes+1)
			}),
		},
		{
			name: "id too long",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["id"] =
					strings.Repeat("a", gatewayAnnotationsMaxIDRunes+1)
			}),
		},
		{
			name: "text too long",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"].(map[string]interface{})["text"] =
					strings.Repeat("字", gatewayAnnotationsMaxTextRunes+1)
			}),
		},
		{
			name: "prefix too long",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				value["annotations"].([]interface{})[0].(map[string]interface{})["target"].(map[string]interface{})["prefix"] =
					strings.Repeat("字", gatewayAnnotationsMaxAffixRunes+1)
			}),
		},
		{
			name: "duplicate ids",
			value: gatewayAnnotationRequest("board", 9, func(value map[string]interface{}) {
				annotation := value["annotations"].([]interface{})[0]
				value["annotations"] = []interface{}{annotation, annotation}
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document, err := normalizeGatewayAnnotations(tc.value)
			if err == nil {
				t.Fatalf("expected rejection, got document %+v", document)
			}
		})
	}
}

// TestNormalizeGatewayAnnotationsRejectsBreaksInIdentifiers verifies path
// control characters are bounded.
func TestNormalizeGatewayAnnotationsRejectsBreaksInIdentifiers(t *testing.T) {
	value := gatewayAnnotationRequest("board", 9, nil)
	value["page"].(map[string]interface{})["path"] = "/a\nb"
	if _, err := normalizeGatewayAnnotations(value); err == nil {
		t.Fatal("expected a line break inside the path to be rejected")
	}
}

// TestGatewayAnnotationsTotalBytesRejectsSixteenKiBPlus builds ~20 annotations
// with long bodies so the canonical value crosses the total budget.
func TestGatewayAnnotationsTotalBytesRejectsSixteenKiBPlus(t *testing.T) {
	annotations := make([]interface{}, 0, gatewayAnnotationsMaxAnnotations)
	for index := 0; index < 10; index++ {
		annotations = append(annotations, map[string]interface{}{
			"id":     "a" + strconv.Itoa(index),
			"kind":   "text",
			"body":   strings.Repeat("字", 900),
			"target": map[string]interface{}{"text": strings.Repeat("字", 900)},
		})
	}
	value := map[string]interface{}{
		"contract_version": GatewayAnnotationsContractV1,
		"agent_uid":        9.0,
		"app_id":           "board",
		"page":             map[string]interface{}{"path": "/board"},
		"annotations":      annotations,
	}
	if _, err := normalizeGatewayAnnotations(value); err == nil {
		t.Fatal("expected the total byte budget to reject values over 16KiB")
	}
}

// gatewayAnnotationFakeStore carries the pieces the annotation paths touch:
// account types, bot ownership/friendship and persisted message metadata.
type gatewayAnnotationFakeStore struct {
	store.Store
	users        map[int64]*types.User
	botOwners    map[int64]int64
	friendPairs  map[string]bool
	groupMembers map[string]bool
	groupMuted   map[string]bool
	groups       map[int64]*types.Group
	members      map[int64][]*types.GroupMember
	topics       []string
	saved        []gatewayAnnotationSavedMessage
}

type gatewayAnnotationSavedMessage struct {
	topicID     string
	fromUID     int64
	blocks      []types.ContentBlock
	mode        string
	role        string
	msgType     string
	clientMsgID string
	metadata    map[string]interface{}
}

func (s *gatewayAnnotationFakeStore) GetUser(id int64) (*types.User, error) {
	user, ok := s.users[id]
	if !ok {
		return nil, errNotFoundForTest()
	}
	return user, nil
}

func (s *gatewayAnnotationFakeStore) GetBotOwner(botUID int64) (int64, error) {
	owner, ok := s.botOwners[botUID]
	if !ok {
		return 0, errNotFoundForTest()
	}
	return owner, nil
}

func (s *gatewayAnnotationFakeStore) AreFriends(uid1, uid2 int64) (bool, error) {
	return s.friendPairs[friendKeyForAnnotations(uid1, uid2)] || s.friendPairs[friendKeyForAnnotations(uid2, uid1)], nil
}

func (s *gatewayAnnotationFakeStore) IsGroupMember(groupID int64, uid int64) (bool, error) {
	return s.groupMembers[groupMemberKeyForAnnotations(groupID, uid)], nil
}

func (s *gatewayAnnotationFakeStore) IsMemberMuted(groupID int64, uid int64) (bool, error) {
	return s.groupMuted[groupMemberKeyForAnnotations(groupID, uid)], nil
}

func (s *gatewayAnnotationFakeStore) GetGroup(groupID int64) (*types.Group, error) {
	group, ok := s.groups[groupID]
	if !ok {
		return nil, errNotFoundForTest()
	}
	return group, nil
}

func (s *gatewayAnnotationFakeStore) GetGroupMembers(groupID int64) ([]*types.GroupMember, error) {
	return s.members[groupID], nil
}

func (s *gatewayAnnotationFakeStore) CreateTopic(id string, topicType string, ownerID int64) error {
	s.topics = append(s.topics, id)
	return nil
}

func (s *gatewayAnnotationFakeStore) SaveMessageWithMetadata(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string, metadata map[string]interface{}) (int64, bool, error) {
	s.saved = append(s.saved, gatewayAnnotationSavedMessage{
		topicID: topicID, fromUID: fromUID, blocks: blocks, mode: mode, role: role,
		msgType: msgType, clientMsgID: clientMsgID, metadata: metadata,
	})
	return int64(len(s.saved)), false, nil
}

func friendKeyForAnnotations(uid1, uid2 int64) string {
	return strconv.FormatInt(uid1, 10) + ":" + strconv.FormatInt(uid2, 10)
}

func groupMemberKeyForAnnotations(groupID int64, uid int64) string {
	return strconv.FormatInt(groupID, 10) + ":" + strconv.FormatInt(uid, 10)
}

func errNotFoundForTest() error { return errors.New("not found") }

func gatewayAnnotationHuman(id int64) *types.User {
	return &types.User{ID: id, Username: "human" + strconv.FormatInt(id, 10), DisplayName: "用户" + strconv.FormatInt(id, 10), AccountType: types.AccountHuman}
}

func gatewayAnnotationBot(id int64) *types.User {
	return &types.User{ID: id, Username: "agent" + strconv.FormatInt(id, 10), DisplayName: "代理" + strconv.FormatInt(id, 10), AccountType: types.AccountBot}
}

// staticAppResolver answers app ownership from a fixed table; a nil entry
// keeps the (not found) semantics of the gateway list.
type staticAppResolver struct {
	apps  map[string]string
	err   error
	calls int
}

func (r *staticAppResolver) GatewayAppOwner(ctx context.Context, appID string) (string, bool, error) {
	r.calls++
	if r.err != nil {
		return "", false, r.err
	}
	owner, ok := r.apps[appID]
	if !ok {
		return "", false, nil
	}
	return owner, true, nil
}

func newAnnotationHub(t *testing.T, storeData *gatewayAnnotationFakeStore, resolver GatewayAnnotationsAppResolver) *Hub {
	t.Helper()
	hub := NewHub(storeData, nil)
	hub.SetGatewayAnnotationsAppResolver(resolver)
	return hub
}

func TestValidateGatewayAnnotationsMetadataAuthorizesServerSideIdentity(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})

	metadata := map[string]interface{}{
		"gateway_annotations": gatewayAnnotationRequest("board", 9.0, nil),
	}
	validated, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", metadata)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	value := validated["gateway_annotations"].(map[string]interface{})
	if value["agent_uid"].(float64) != 9 || value["app_id"] != "board" {
		t.Fatalf("validated identity: %+v", value)
	}

	// A forged agent_uid for another agent must be rejected outright.
	forged := map[string]interface{}{
		"gateway_annotations": gatewayAnnotationRequest("board", 11.0, nil),
	}
	hub.SetGatewayAnnotationsAppResolver(&staticAppResolver{apps: map[string]string{"board": "11"}})
	if _, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", forged); err == nil {
		t.Fatal("expected agent mismatch to fail")
	}

	// An app owned by a stranger must not pass even with a matching claim.
	hub.SetGatewayAnnotationsAppResolver(&staticAppResolver{apps: map[string]string{"board": "42"}})
	if _, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", metadata); err == nil {
		t.Fatal("expected foreign app ownership to fail")
	}

	// A broken registry fails closed.
	hub.SetGatewayAnnotationsAppResolver(&staticAppResolver{err: errNotFoundForTest()})
	if _, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", metadata); err == nil {
		t.Fatal("expected registry failure to fail closed")
	}

	// No resolver at all also fails closed.
	hub.SetGatewayAnnotationsAppResolver(nil)
	if _, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", metadata); err == nil {
		t.Fatal("expected missing registry to fail closed")
	}
}

func TestValidateGatewayAnnotationsMetadataBoundsScenarios(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users: map[int64]*types.User{
			7: gatewayAnnotationHuman(7),
			9: gatewayAnnotationBot(9),
			8: gatewayAnnotationHuman(8),
		},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})

	// No annotations key: the metadata is returned untouched.
	plain := map[string]interface{}{"catsco_transient": true}
	if _, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", plain); err != nil {
		t.Fatalf("plain message: %v", err)
	}

	// Malformed value rejects the whole message.
	if _, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", map[string]interface{}{
		"gateway_annotations": "just a string",
	}); err == nil {
		t.Fatal("expected malformed value to reject the message")
	}

	// A peer human topic never resolves an Agent; annotations fail.
	if _, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_8", map[string]interface{}{
		"gateway_annotations": gatewayAnnotationRequest("board", 8.0, nil),
	}); err == nil {
		t.Fatal("expected human-to-human topic annotations to fail")
	}

	// A service account's claim is dropped, not persisted and not a rejection.
	storeData.users[99] = &types.User{ID: 99, Username: "svc", AccountType: types.AccountService}
	queue := map[string]interface{}{
		"gateway_annotations": gatewayAnnotationRequest("board", 9.0, nil),
	}
	metadata, err := hub.validateGatewayAnnotationsMetadata(99, "p2p_99_9", queue)
	if err != nil {
		t.Fatalf("service account: %v", err)
	}
	if hasGatewayAnnotationsMetadata(metadata) {
		t.Fatal("expected the service account claim to be dropped")
	}

	// An empty annotation list is dropped silently.
	emptyMetadata, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", map[string]interface{}{
		"gateway_annotations": gatewayAnnotationRequest("board", 9.0, func(value map[string]interface{}) {
			value["annotations"] = []interface{}{}
		}),
	})
	if err != nil {
		t.Fatalf("empty annotations: %v", err)
	}
	if hasGatewayAnnotationsMetadata(emptyMetadata) {
		t.Fatal("expected the empty annotation list to be dropped")
	}
}

func TestValidateGatewayAnnotationsMetadataMultiBotGroupFailsClosed(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users: map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9), 10: gatewayAnnotationBot(10)},
		groupMembers: map[string]bool{
			groupMemberKeyForAnnotations(5, 7): true, groupMemberKeyForAnnotations(5, 9): true, groupMemberKeyForAnnotations(5, 10): true,
		},
		members: map[int64][]*types.GroupMember{5: {
			{UserID: 7}, {UserID: 9, IsBot: true}, {UserID: 10, IsBot: true},
		}},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	if _, err := hub.validateGatewayAnnotationsMetadata(7, "grp_5", map[string]interface{}{
		"gateway_annotations": gatewayAnnotationRequest("board", 9.0, nil),
	}); err == nil {
		t.Fatal("expected multi-agent group annotations to fail")
	}
}

// TestHTTPSendMessageCarriesValidatedAnnotations goes through the real HTTP
// ingress with a real gateway relay and asserts both the API answer and the
// persisted metadata.
func TestHTTPSendMessageCarriesValidatedAnnotations(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(artifactApp{ID: "board", Agent: "9"})
	handler := gateway.handler()

	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, handler)
	messageHandler := NewMessageHandler(storeData, hub)

	payload := struct {
		TopicID  string                 `json:"topic_id"`
		Content  string                 `json:"content"`
		Metadata map[string]interface{} `json:"metadata"`
	}{TopicID: "p2p_7_9", Content: "看下这个按钮", Metadata: map[string]interface{}{
		"gateway_annotations": gatewayAnnotationRequest("board", 9.0, nil),
	}}
	body, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(body)))
	request = request.WithContext(withTestUID(7))
	recorder := httptest.NewRecorder()
	messageHandler.HandleSendMessage(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	if len(storeData.saved) != 1 {
		t.Fatalf("saved=%d, want 1", len(storeData.saved))
	}
	saved := storeData.saved[0].metadata["gateway_annotations"].(map[string]interface{})
	if saved["agent_uid"].(float64) != 9.0 || saved["app_id"] != "board" {
		t.Fatalf("persisted identity: %+v", saved)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response: %v", err)
	}
	if response["metadata"] == nil {
		t.Fatal("response must echo the persisted annotation metadata")
	}

	// A forged claim is rejected without persisting anything.
	storeData.saved = nil
	forged, _ := json.Marshal(struct {
		TopicID  string                 `json:"topic_id"`
		Content  string                 `json:"content"`
		Metadata map[string]interface{} `json:"metadata"`
	}{TopicID: "p2p_7_9", Content: "n", Metadata: map[string]interface{}{
		"gateway_annotations": gatewayAnnotationRequest("board", 11.0, nil),
	}})
	hub.SetGatewayAnnotationsAppResolver(&staticAppResolver{apps: map[string]string{"board": "9"}})
	request = httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(forged)))
	request = request.WithContext(withTestUID(7))
	recorder = httptest.NewRecorder()
	messageHandler.HandleSendMessage(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("forged status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(storeData.saved) != 0 {
		t.Fatal("a rejected annotated message must not persist")
	}
}

// TestHTTPSendMessageWithoutAnnotationsStaysNormal covers the invariant that
// ordinary messages keep behaving exactly as before.
func TestHTTPSendMessageWithoutAnnotationsStaysNormal(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := NewHub(storeData, nil)
	messageHandler := NewMessageHandler(storeData, hub)

	body, _ := json.Marshal(struct {
		TopicID string `json:"topic_id"`
		Content string `json:"content"`
	}{TopicID: "p2p_7_9", Content: "普通消息"})
	request := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(body)))
	request = request.WithContext(withTestUID(7))
	recorder := httptest.NewRecorder()
	messageHandler.HandleSendMessage(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(storeData.saved) != 1 || hasGatewayAnnotationsMetadata(storeData.saved[0].metadata) {
		t.Fatalf("unexpected persisted annotations: %+v", storeData.saved)
	}
}

// TestHistoryAPIKeepsGatewayAnnotations verifies the annotation data survives
// storage round-trips so the conversation history can re-render them.
func TestHistoryAPIKeepsGatewayAnnotations(t *testing.T) {
	value, err := (&gatewayAnnotationsDocument{
		AgentUID: 9, AppID: "board",
		Page:        gatewayAnnotationsPage{Path: "/board"},
		Annotations: []gatewayAnnotation{{ID: "a1", Kind: "element", Label: "按钮", Body: "改色", Target: gatewayAnnotationTarget{ElementID: "submit"}}},
	}).restamped()
	if err != nil {
		t.Fatalf("restamp: %v", err)
	}
	message := &types.Message{
		ID: 3, TopicID: "p2p_7_9", FromUID: 7, Content: "hi", MsgType: "text",
		Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	data := (&Hub{}).historyMessageDataForRecipient(7, message)
	if data == nil || data.Metadata == nil {
		t.Fatal("history metadata missing")
	}
	if _, present := data.Metadata["gateway_annotations"]; !present {
		t.Fatal("history dropped gateway_annotations")
	}
	if _, present := data.Metadata[artifactRefMetadataKey]; present {
		t.Fatal("artifact refs must still be scrubbed from history")
	}
}

func gatewayAnnotationFanoutPayload(metadata map[string]interface{}) *normalizedMessagePayload {
	return &normalizedMessagePayload{
		StoredContent:  "请修复",
		DisplayContent: "请修复",
		StoredType:     "text",
		DisplayType:    "text",
		Metadata:       metadata,
	}
}

// TestFanoutDeliversReadableAnnotationsToAgentOnly asserts the fanout-time
// context block reaches exactly the annotateated Agent and contains everything
// the Agent needs in readable form.
func TestFanoutDeliversReadableAnnotationsToAgentOnly(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, err := (&gatewayAnnotationsDocument{
		AgentUID: 9, AppID: "board",
		Page:        gatewayAnnotationsPage{Path: "/board", Revision: "r7"},
		Annotations: []gatewayAnnotation{{ID: "a1", Kind: "element", Label: "发布按钮", Body: "改成蓝色", Target: gatewayAnnotationTarget{ElementID: "submit"}}},
	}).restamped()
	if err != nil {
		t.Fatalf("restamp: %v", err)
	}
	payload := gatewayAnnotationFanoutPayload(map[string]interface{}{
		"gateway_annotations": value,
	})

	agentMessage := hub.messageForRecipient(7, 9, "p2p_7_9", 0, payload, 12)
	context := agentMessage.Data.Metadata["catsco_gateway_annotation_context"].(map[string]interface{})
	if context["app_id"] != "board" || context["agent_uid"] != "usr9" {
		t.Fatalf("context identity: %+v", context)
	}
	summary := context["summary"].(string)
	if !strings.Contains(summary, "board") || !strings.Contains(summary, "/board") || !strings.Contains(summary, "改成蓝色") {
		t.Fatalf("summary is not readable: %q", summary)
	}
	if annotations, ok := context["annotations"].([]interface{}); !ok || len(annotations) != 1 {
		t.Fatalf("context annotations: %+v", context["annotations"])
	}
	if _, present := agentMessage.Data.Metadata["gateway_annotations"]; !present {
		t.Fatal("agent must also receive the structured annotations")
	}

	senderMessage := hub.messageForRecipient(7, 7, "p2p_7_9", 0, payload, 12)
	if _, present := senderMessage.Data.Metadata["catsco_gateway_annotation_context"]; present {
		t.Fatal("the sender must not receive the agent context block")
	}

	hub.SetGatewayAnnotationsAppResolver(nil)
	plainPayload := gatewayAnnotationFanoutPayload(nil)
	plain, err := json.Marshal(hub.messageForRecipient(7, 9, "p2p_7_9", 0, plainPayload, 13).Data)
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(plain, &decoded); err != nil {
		t.Fatalf("unmarshal plain: %v", err)
	}
	if hasGatewayAnnotationsMetadata(nil) {
		t.Fatal("nil metadata must not claim annotations")
	}
}

// TestGroupBroadcastDeliversAgentContext covers the group path, where
// per-recipient metadata is rebuilt inside the broadcaster.
func TestGroupBroadcastDeliversAgentContext(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users: map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		groupMembers: map[string]bool{
			groupMemberKeyForAnnotations(5, 7): true, groupMemberKeyForAnnotations(5, 9): true,
		},
		members: map[int64][]*types.GroupMember{5: {{UserID: 7}, {UserID: 9, IsBot: true}}},
		groups:  map[int64]*types.Group{5: {ID: 5, AgentIDs: []int64{9}}},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, err := (&gatewayAnnotationsDocument{
		AgentUID: 9, AppID: "board",
		Page:        gatewayAnnotationsPage{Path: "/board"},
		Annotations: []gatewayAnnotation{{ID: "a1", Kind: "text", Body: "这句话有歧义", Target: gatewayAnnotationTarget{Text: "歧义"}}},
	}).restamped()
	if err != nil {
		t.Fatalf("restamp: %v", err)
	}
	payload := gatewayAnnotationFanoutPayload(map[string]interface{}{"gateway_annotations": value})
	dataMsg := hub.messageForRecipient(7, 0, "grp_5", 0, payload, 21)
	dataMsg.Data.Mentions = []string{formatUID(9)}

	botClient := &Client{uid: 9, send: make(chan []byte, 4), accountType: types.AccountBot}
	hub.addClient(botClient)

	// broadcastToGroupWithMentions reports artifact task delivery, not the
	// annotation turn; the observable outcome is the agent's own message.
	hub.broadcastToGroupWithMentions(5, dataMsg, 7, []string{formatUID(9)}, 7, false)
	select {
	case raw := <-botClient.send:
		var received ServerMessage
		if err := json.Unmarshal(raw, &received); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		context, ok := received.Data.Metadata["catsco_gateway_annotation_context"].(map[string]interface{})
		if !ok {
			t.Fatalf("agent context missing: %+v", received.Data.Metadata)
		}
		if context["app_id"] != "board" || context["agent_uid"] != "usr9" {
			t.Fatalf("context identity: %+v", context)
		}
	default:
		t.Fatal("the agent member did not receive the annotated message")
	}
}

// TestWebSocketPubRejectsInvalidAnnotations exercises the WS ingress path
// directly, asserting the same bounded rejection semantics as HTTP.
func TestWebSocketPubRejectsInvalidAnnotations(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	client := &Client{uid: 7, send: make(chan []byte, 4), accountType: types.AccountHuman}
	hub.addClient(client)

	hub.handlePub(client, &MsgClientPub{
		ID:      "w1",
		Topic:   "p2p_7_9",
		Type:    "text",
		Content: json.RawMessage(`"看下这个"`),
		Metadata: map[string]interface{}{
			"gateway_annotations": map[string]interface{}{
				"contract_version": GatewayAnnotationsContractV1,
				"agent_uid":        9.0,
				"app_id":           "board",
				"page":             map[string]interface{}{"path": "/not-a-path-lol"},
				"annotations":      []interface{}{map[string]interface{}{"id": "x", "kind": "element", "body": "b", "target": map[string]interface{}{}}},
			},
		},
	})

	select {
	case raw := <-client.send:
		var reply ServerMessage
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if reply.Ctrl == nil || reply.Ctrl.Code != http.StatusBadRequest {
			t.Fatalf("reply = %+v, want ctrl 400", reply.Ctrl)
		}
	default:
		t.Fatal("the WS ingress must answer the invalid payload")
	}
}

// TestWebSocketPubDeliversValidatedAnnotations checks the happy path: the
// message persists and the agent receives both structured annotations and the
// readable context.
func TestWebSocketPubDeliversValidatedAnnotations(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(artifactApp{ID: "board", Agent: "9"})

	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, gateway.handler())

	client := &Client{uid: 7, send: make(chan []byte, 4), accountType: types.AccountHuman}
	agentClient := &Client{uid: 9, send: make(chan []byte, 4), accountType: types.AccountBot}
	hub.addClient(client)
	hub.addClient(agentClient)

	hub.handlePub(client, &MsgClientPub{
		ID:      "w2",
		Topic:   "p2p_7_9",
		Type:    "text",
		Content: json.RawMessage(`"请看"`),
		Metadata: map[string]interface{}{
			"gateway_annotations": gatewayAnnotationRequest("board", 9.0, nil),
		},
	})

	select {
	case raw := <-client.send:
		var reply ServerMessage
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("unmarshal ack: %v", err)
		}
		if reply.Ctrl == nil || reply.Ctrl.Code != 200 {
			t.Fatalf("ack = %+v", reply.Ctrl)
		}
	default:
		t.Fatal("the sender must be acknowledged")
	}
	select {
	case raw := <-agentClient.send:
		var delivered ServerMessage
		if err := json.Unmarshal(raw, &delivered); err != nil {
			t.Fatalf("unmarshal delivery: %v", err)
		}
		if delivered.Data == nil {
			t.Fatal("delivery without data")
		}
		if _, ok := delivered.Data.Metadata["gateway_annotations"]; !ok {
			t.Fatal("structured annotations missing on agent delivery")
		}
		if context, ok := delivered.Data.Metadata["catsco_gateway_annotation_context"].(map[string]interface{}); !ok {
			t.Fatalf("agent context missing: %+v", delivered.Data.Metadata)
		} else if context["app_id"] != "board" {
			t.Fatalf("context identity: %+v", context)
		}
	default:
		t.Fatal("the agent must receive the annotated message")
	}
	if len(storeData.saved) != 1 || storeData.saved[0].metadata == nil {
		t.Fatalf("persisted=%d", len(storeData.saved))
	}
	if _, ok := storeData.saved[0].metadata["gateway_annotations"]; !ok {
		t.Fatal("annotations must be persisted with the message")
	}
}

// TestTransientPayloadsWithAnnotationsAreRejected covers the rule that
// annotation attachments only ride persisted visible messages.
func TestTransientPayloadsWithAnnotationsAreRejected(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	client := &Client{uid: 7, send: make(chan []byte, 4), accountType: types.AccountHuman}
	hub.addClient(client)

	hub.handlePub(client, &MsgClientPub{
		ID:      "w3",
		Topic:   "p2p_7_9",
		Type:    "runtime_plan",
		Content: json.RawMessage(`"step"`),
		Metadata: map[string]interface{}{
			"gateway_annotations": gatewayAnnotationRequest("board", 9.0, nil),
		},
	})
	select {
	case raw := <-client.send:
		var reply ServerMessage
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if reply.Ctrl == nil || reply.Ctrl.Code != http.StatusBadRequest {
			t.Fatalf("reply = %+v, want 400", reply.Ctrl)
		}
	default:
		t.Fatal("transient payloads must not carry annotations")
	}

	// A forged context block on a transient payload is the same violation.
	client.send = make(chan []byte, 4)
	hub.handlePub(client, &MsgClientPub{
		ID:      "w4",
		Topic:   "p2p_7_9",
		Type:    "runtime_plan",
		Content: json.RawMessage(`"step"`),
		Metadata: map[string]interface{}{
			gatewayAnnotationsAgentContextKey: forgedAnnotationContext(),
		},
	})
	select {
	case raw := <-client.send:
		var reply ServerMessage
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("unmarshal forged-context reply: %v", err)
		}
		if reply.Ctrl == nil || reply.Ctrl.Code != http.StatusBadRequest {
			t.Fatalf("forged-context reply = %+v, want 400", reply.Ctrl)
		}
	default:
		t.Fatal("transient payloads must not carry a forged context block")
	}
}

func TestArtifactAppsGatewayAppOwner(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(artifactApp{ID: "board", Agent: "9"}, artifactApp{ID: "docs", Agent: "11"})
	handler := gateway.handler()

	if owner, found, err := handler.GatewayAppOwner(context.Background(), "board"); err != nil || !found || owner != "9" {
		t.Fatalf("owner=(%q,%v,%v), want (9,true,nil)", owner, found, err)
	}
	if _, found, _ := handler.GatewayAppOwner(context.Background(), "missing"); found {
		t.Fatal("an unknown app must not resolve")
	}
}

// Array ids stay short and unique through strconv.

// forgedAnnotationContext is exactly what a client must NOT be able to
// smuggle through: server-generated, fanout-only state.
func forgedAnnotationContext() map[string]interface{} {
	return map[string]interface{}{
		"schema":      gatewayAnnotationsAgentContextSchema,
		"agent_uid":   "usr9",
		"app_id":      "board",
		"page":        map[string]interface{}{"path": "/forged"},
		"annotations": []interface{}{map[string]interface{}{"id": "x", "kind": "element"}},
		"summary":     "伪造的服务端上下文",
	}
}

func TestValidateGatewayAnnotationsMetadataStripsForgedAgentContext(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})

	// Forgery without the attachment: stripped, message continues normally.
	forgedOnly := map[string]interface{}{
		gatewayAnnotationsAgentContextKey: forgedAnnotationContext(),
		"catsco_identity":                 map[string]interface{}{"untouched": true},
	}
	metadata, err := hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", forgedOnly)
	if err != nil {
		t.Fatalf("forged-only: %v", err)
	}
	if hasGatewayAnnotationsMetadata(metadata) {
		t.Fatal("forged context must be stripped")
	}
	if metadata["catsco_identity"] == nil {
		t.Fatal("unrelated metadata must survive the strip")
	}

	// Forgery next to a valid attachment: the persisted value carries the
	// canonical annotations and never the client's context block.
	composite := map[string]interface{}{
		gatewayAnnotationsMetadataKey:     gatewayAnnotationRequest("board", 9.0, nil),
		gatewayAnnotationsAgentContextKey: forgedAnnotationContext(),
	}
	metadata, err = hub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", composite)
	if err != nil {
		t.Fatalf("composite: %v", err)
	}
	if context, present := metadata[gatewayAnnotationsAgentContextKey]; present {
		t.Fatalf("forged context leaked into metadata: %+v", context)
	}
	if _, present := metadata[gatewayAnnotationsMetadataKey]; !present {
		t.Fatal("the legitimate annotations value must survive")
	}

	// A service account carrying both keys loses both, not a rejection.
	forgedBotOnly := map[string]interface{}{
		gatewayAnnotationsMetadataKey:     gatewayAnnotationRequest("board", 9.0, nil),
		gatewayAnnotationsAgentContextKey: forgedAnnotationContext(),
	}
	metadata, err = hub.validateGatewayAnnotationsMetadata(9, "p2p_9_7", forgedBotOnly)
	if err != nil {
		t.Fatalf("bot sender: %v", err)
	}
	if hasGatewayAnnotationsMetadata(metadata) {
		t.Fatal("the bot claim must be dropped entirely")
	}

	// Unconfigured deployments strip without rejecting.
	bareHub := NewHub(nil, nil)
	metadata, err = bareHub.validateGatewayAnnotationsMetadata(7, "p2p_7_9", composite)
	if err != nil {
		t.Fatalf("unconfigured: %v", err)
	}
	if hasGatewayAnnotationsMetadata(metadata) {
		t.Fatal("unconfigured deployments must strip both keys")
	}
}

// TestHTTPIngestionStripsForgedAgentContext covers the user-facing HTTP path:
// a forged context block is dropped before anything is stored or echoed.
func TestHTTPIngestionStripsForgedAgentContext(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	messageHandler := NewMessageHandler(storeData, hub)

	send := func(metadata map[string]interface{}) *httptest.ResponseRecorder {
		body, _ := json.Marshal(struct {
			TopicID  string                 `json:"topic_id"`
			Content  string                 `json:"content"`
			Metadata map[string]interface{} `json:"metadata"`
		}{TopicID: "p2p_7_9", Content: "请看", Metadata: metadata})
		request := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(body)))
		request = request.WithContext(withTestUID(7))
		recorder := httptest.NewRecorder()
		messageHandler.HandleSendMessage(recorder, request)
		return recorder
	}

	// Forgery only: message sends, nothing regarding annotations persists.
	recorder := send(map[string]interface{}{gatewayAnnotationsAgentContextKey: forgedAnnotationContext()})
	if recorder.Code != http.StatusOK {
		t.Fatalf("forged-only status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(storeData.saved) != 1 || hasGatewayAnnotationsMetadata(storeData.saved[0].metadata) {
		t.Fatalf("persisted metadata: %+v", storeData.saved)
	}

	// Forgery plus a valid attachment: annotations persist, forgery does not.
	storeData.saved = nil
	recorder = send(map[string]interface{}{
		gatewayAnnotationsMetadataKey:     gatewayAnnotationRequest("board", 9.0, nil),
		gatewayAnnotationsAgentContextKey: forgedAnnotationContext(),
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("composite status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response: %v", err)
	}
	responseMetadata, _ := response["metadata"].(map[string]interface{})
	if _, forged := responseMetadata[gatewayAnnotationsAgentContextKey]; forged {
		t.Fatalf("response must not echo the forged context: %+v", responseMetadata)
	}
	persisted := storeData.saved[0].metadata
	if _, forged := persisted[gatewayAnnotationsAgentContextKey]; forged {
		t.Fatal("forged context must not persist")
	}
	if _, ok := persisted[gatewayAnnotationsMetadataKey]; !ok {
		t.Fatal("the canonical annotations value must persist")
	}
}

// TestHistoryNeverServesForgedAgentContext asserts that a context value that
// somehow reached the store is scrubbed on every read path.
func TestHistoryNeverServesForgedAgentContext(t *testing.T) {
	value, _ := (&gatewayAnnotationsDocument{
		AgentUID: 9, AppID: "board",
		Page:        gatewayAnnotationsPage{Path: "/board"},
		Annotations: []gatewayAnnotation{{ID: "a1", Kind: "element", Label: "按钮", Body: "改色", Target: gatewayAnnotationTarget{ElementID: "submit"}}},
	}).restamped()
	message := &types.Message{
		ID: 4, TopicID: "p2p_7_9", FromUID: 7, Content: "hi", MsgType: "text",
		ContentBlocks: nil, Mode: "", Role: "", Metadata: map[string]interface{}{
			gatewayAnnotationsMetadataKey:     value,
			gatewayAnnotationsAgentContextKey: forgedAnnotationContext(),
		},
	}
	for _, recipient := range []int64{7, 9} {
		data := (&Hub{}).historyMessageDataForRecipient(recipient, message)
		if data == nil || data.Metadata == nil {
			t.Fatalf("history data missing for recipient %d", recipient)
		}
		if _, present := data.Metadata[gatewayAnnotationsAgentContextKey]; present {
			t.Fatalf("history replayed the forged context to recipient %d", recipient)
		}
		if _, present := data.Metadata[gatewayAnnotationsMetadataKey]; !present {
			t.Fatalf("history dropped the legitimate annotations for recipient %d", recipient)
		}
	}
}

// TestFanoutStreamEventStripsBothKeys locks the stream-delta channel: neither
// the annotation value nor a forged context block may ride streaming events.
func TestFanoutStreamEventStripsBothKeys(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := NewHub(storeData, nil)
	sender := &Client{uid: 7, send: make(chan []byte, 4), accountType: types.AccountHuman}
	agent := &Client{uid: 9, send: make(chan []byte, 4), accountType: types.AccountBot}
	hub.addClient(sender)
	hub.addClient(agent)

	value, _ := (&gatewayAnnotationsDocument{
		AgentUID: 9, AppID: "board",
		Page:        gatewayAnnotationsPage{Path: "/board"},
		Annotations: []gatewayAnnotation{{ID: "a1", Kind: "element", Body: "评论", Target: gatewayAnnotationTarget{ElementID: "submit"}}},
	}).restamped()
	hub.fanoutStreamEvent(7, "p2p_7_9", "stream_delta", "chunk", map[string]interface{}{
		gatewayAnnotationsMetadataKey:     value,
		gatewayAnnotationsAgentContextKey: forgedAnnotationContext(),
	}, nil)

	for name, client := range map[string]*Client{"agent": agent, "sender": sender} {
		select {
		case raw := <-client.send:
			var received ServerMessage
			if err := json.Unmarshal(raw, &received); err != nil {
				t.Fatalf("%s unmarshal: %v", name, err)
			}
			if received.Data == nil {
				t.Fatalf("%s delivery without data", name)
			}
			if _, forged := received.Data.Metadata[gatewayAnnotationsAgentContextKey]; forged {
				t.Fatalf("%s stream forwarded the forged context", name)
			}
			if _, ok := received.Data.Metadata[gatewayAnnotationsMetadataKey]; ok {
				t.Fatalf("%s stream forwarded the annotation value", name)
			}
			if received.Data.Metadata["stream_event"] != "delta" {
				t.Fatalf("%s stream event missing: %+v", name, received.Data.Metadata)
			}
		default:
			t.Fatalf("%s did not receive the stream event", name)
		}
	}
}

// TestTargetFreeTextAllowsBreaksAndTabs keeps multi-line UI selections working
// while identifiers and paths still reject control characters.
func TestTargetFreeTextAllowsBreaksAndTabs(t *testing.T) {
	value := gatewayAnnotationRequest("board", 9, nil)
	annotation := value["annotations"].([]interface{})[0].(map[string]interface{})
	annotation["kind"] = "text"
	annotation["target"] = map[string]interface{}{
		"text":   "一共三行\n选到了\t制表符\r\n结尾",
		"prefix": "前缀\n换行",
		"suffix": "后缀\t制表",
	}
	document, err := normalizeGatewayAnnotations(value)
	if err != nil {
		t.Fatalf("multi-line selection must normalize: %v", err)
	}
	target := document.Annotations[0].Target
	if !strings.Contains(target.Text, "\n") || !strings.Contains(target.Text, "\t") {
		t.Fatalf("text lost its breaks: %q", target.Text)
	}
	if !strings.Contains(target.Prefix, "\n") || !strings.Contains(target.Suffix, "\t") {
		t.Fatalf("affixes lost their breaks: %q / %q", target.Prefix, target.Suffix)
	}

	// Identifiers stay strict.
	strict := gatewayAnnotationRequest("board", 9, nil)
	strict["annotations"].([]interface{})[0].(map[string]interface{})["target"].(map[string]interface{})["element_id"] = "has\nbreak"
	if _, err := normalizeGatewayAnnotations(strict); err == nil {
		t.Fatal("line breaks must stay rejected inside element_id")
	}
	strictPath := gatewayAnnotationRequest("board", 9, nil)
	strictPath["page"].(map[string]interface{})["path"] = "/path\ntrimmed"
	if _, err := normalizeGatewayAnnotations(strictPath); err == nil {
		t.Fatal("line breaks must stay rejected inside page path")
	}
}
