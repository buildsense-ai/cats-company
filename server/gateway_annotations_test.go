// Package server — gateway artifact annotation ingestion tests. The suites
// cover the bounded schema, server-canonical identity authorization, the HTTP
// and WebSocket ingestion paths, durable persistence, and the readable context
// the Agent actually receives at fanout.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

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
	users         map[int64]*types.User
	botOwners     map[int64]int64
	friendPairs   map[string]bool
	groupMembers  map[string]bool
	groupMuted    map[string]bool
	groups        map[int64]*types.Group
	members       map[int64][]*types.GroupMember
	topics        []string
	storedHistory map[string][]*types.Message
	saved         []gatewayAnnotationSavedMessage
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

func (s *gatewayAnnotationFakeStore) IsChannelManagedGroup(groupID int64) (bool, error) {
	// Non-channel-owned groups let every visible member receive the fanout.
	return false, nil
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

// xiaoBaEquivalentUserInput mirrors the XiaoBa-CLI parseMessage rule for
// turning a delivered message into the text that reaches the model: every
// content_blocks entry with type "text" and non-blank text is joined with a
// blank line, falling back to the top-level content only when no text block
// exists (blockText || content). Asserting against this helper keeps the
// server contract coupled to what the actual agent client consumes.
func xiaoBaEquivalentUserInput(data *MsgServerData) string {
	if data == nil {
		return ""
	}
	parts := []string{}
	for _, block := range data.ContentBlocks {
		// Exact lowercase "text" — the live XiaoBa parseMessage merge rule.
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	return normalizeContentText(data.Content)
}

// cloudRestoreEquivalentText mirrors the offline XiaoBa
// cloud-session-restore rule (cloudMessageText), faithfully to the real JS:
// a non-empty string content wins outright; rich file/image/voice always
// render "[历史X：name]" + description (independent ternaries on name and the
// JS-truthy text||description, with the payload defaulting to the rich object
// itself when message.payload is missing); any other object renders its
// trimmed description or falls back to trimmed text blocks.
func cloudRestoreEquivalentText(data *MsgServerData) string {
	if data == nil {
		return ""
	}
	if text, ok := data.Content.(string); ok && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	rich, ok := data.Content.(map[string]interface{})
	if !ok {
		return cloudContentBlocksEquivalentText(data.ContentBlocks)
	}
	payload, ok := rich["payload"].(map[string]interface{})
	if !ok {
		// JS: rich.payload && typeof rich.payload === 'object' ? rich.payload : rich
		payload = rich
	}
	contentType := strings.TrimSpace(fmt.Sprint(rich["type"]))
	name, _ := payload["name"].(string)
	description, _ := payload["text"].(string)
	if description == "" {
		description, _ = payload["description"].(string)
	}
	// payload.text wins over payload.description with raw JS truthiness: a
	// " " string is truthy and shadows the description field.
	switch contentType {
	case "file":
		return "[历史文件" + cloudNameClause(name) + "]" + cloudDescriptionClause(description)
	case "image":
		return "[历史图片" + cloudNameClause(name) + "]" + cloudDescriptionClause(description)
	case "voice":
		return "[历史语音]" + cloudDescriptionClause(description)
	default:
		if strings.TrimSpace(description) != "" {
			return strings.TrimSpace(description)
		}
		return cloudContentBlocksEquivalentText(data.ContentBlocks)
	}
}

func cloudNameClause(name string) string {
	if name != "" {
		return "：" + name
	}
	return ""
}

func cloudDescriptionClause(description string) string {
	if description != "" {
		return " " + description
	}
	return ""
}

func cloudContentBlocksEquivalentText(blocks []types.ContentBlock) string {
	parts := []string{}
	for _, block := range blocks {
		switch strings.TrimSpace(block.Type) {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, strings.TrimSpace(block.Text))
			}
		case "image":
			parts = append(parts, "[历史图片]")
		case "file":
			parts = append(parts, "[历史文件]")
		}
	}
	return strings.Join(parts, "\n")
}

func annotationDocForModelText() *gatewayAnnotationsDocument {
	return &gatewayAnnotationsDocument{
		AgentUID: 9, AppID: "board",
		Page:        gatewayAnnotationsPage{Path: "/board", Revision: "r7"},
		Annotations: []gatewayAnnotation{{ID: "a1", Kind: "element", Label: "发布按钮", Body: "改成蓝色", Target: gatewayAnnotationTarget{ElementID: "submit-btn", Selector: "button#submit"}}},
	}
}

// TestP2PFanoutAgentSeesAnnotationTextInContentBlocks reproduces the XiaoBa
// consumption contract end-to-end at fanout: the target Agent's message
// carries the annotation as text blocks so the model text contains page,
// target and body.
func TestP2PFanoutAgentSeesAnnotationTextInContentBlocks(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()

	t.Run("message already has text blocks", func(t *testing.T) {
		payload := &normalizedMessagePayload{
			StoredContent:  "看下这个按钮",
			DisplayContent: "看下这个按钮",
			StoredType:     "text",
			DisplayType:    "text",
			ContentBlocks:  []types.ContentBlock{{Type: "text", Text: "看下这个按钮"}},
			Metadata:       map[string]interface{}{gatewayAnnotationsMetadataKey: value},
		}
		agentMessage := hub.messageForRecipient(7, 9, "p2p_7_9", 0, payload, 31)
		modelText := xiaoBaEquivalentUserInput(agentMessage.Data)
		for _, fragment := range []string{"看下这个按钮", "[Gateway 标注", "board", "/board", "revision r7", "发布按钮", "改成蓝色", "submit-btn", "button#submit"} {
			if !strings.Contains(modelText, fragment) {
				t.Fatalf("agent model text misses %q:\n%s", fragment, modelText)
			}
		}

		// The sender's own copy keeps the original blocks only.
		senderMessage := hub.messageForRecipient(7, 7, "p2p_7_9", 0, payload, 31)
		if senderText := xiaoBaEquivalentUserInput(senderMessage.Data); senderText != "看下这个按钮" {
			t.Fatalf("sender copy should be unchanged: %q", senderText)
		}
		if len(senderMessage.Data.ContentBlocks) != 1 {
			t.Fatalf("sender blocks mutated: %+v", senderMessage.Data.ContentBlocks)
		}
	})

	t.Run("message has only top-level content", func(t *testing.T) {
		// blockText || content: without a text block the annotation block would
		// shadow the user text, so the server must materialize it too.
		payload := &normalizedMessagePayload{
			StoredContent:  "看下这里",
			DisplayContent: "看下这里",
			StoredType:     "text",
			DisplayType:    "text",
			ContentBlocks:  []types.ContentBlock{{Type: "image", Payload: map[string]interface{}{"url": "/uploads/a.png"}}},
			Metadata:       map[string]interface{}{gatewayAnnotationsMetadataKey: value},
		}
		agentMessage := hub.messageForRecipient(7, 9, "p2p_7_9", 0, payload, 32)
		modelText := xiaoBaEquivalentUserInput(agentMessage.Data)
		if !strings.Contains(modelText, "看下这里") {
			t.Fatalf("user text lost when only a top-level content existed:\n%s", modelText)
		}
		if !strings.Contains(modelText, "改成蓝色") {
			t.Fatalf("annotation text missing:\n%s", modelText)
		}

		// The original payload slice stays untouched for persistence.
		if len(payload.ContentBlocks) != 1 || payload.ContentBlocks[0].Type != "image" {
			t.Fatalf("payload blocks mutated: %+v", payload.ContentBlocks)
		}
	})

	t.Run("renders only server-canonical annotations", func(t *testing.T) {
		// A client-forged context block on the payload is ignored entirely.
		payload := &normalizedMessagePayload{
			StoredContent:  "n",
			DisplayContent: "n",
			StoredType:     "text",
			DisplayType:    "text",
			ContentBlocks:  []types.ContentBlock{{Type: "text", Text: "n"}},
			Metadata: map[string]interface{}{
				gatewayAnnotationsMetadataKey:     value,
				gatewayAnnotationsAgentContextKey: forgedAnnotationContext(),
			},
		}
		agentMessage := hub.messageForRecipient(7, 9, "p2p_7_9", 0, payload, 33)
		modelText := xiaoBaEquivalentUserInput(agentMessage.Data)
		if strings.Contains(modelText, "/forged") {
			t.Fatalf("forged context leaked into model text:\n%s", modelText)
		}
	})
}

// TestP2PFanoutAgentSeesAnnotationsWithoutSharedBlockMutation guards the
// shared-slice rule: the annotation block must be copy-on-write so the
// persisted payload and other recipients are untouched.
func TestP2PFanoutAgentAnnotationsDoNotMutateSharedState(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()
	payload := &normalizedMessagePayload{
		StoredContent:  "看下这个按钮",
		DisplayContent: "看下这个按钮",
		StoredType:     "text",
		DisplayType:    "text",
		ContentBlocks:  []types.ContentBlock{{Type: "text", Text: "看下这个按钮"}},
		Metadata:       map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}

	hub.messageForRecipient(7, 9, "p2p_7_9", 0, payload, 41)
	if len(payload.ContentBlocks) != 1 {
		t.Fatalf("payload blocks mutated in place: %+v", payload.ContentBlocks)
	}
	// And the persisted store keeps the original metadata plus original blocks.
	if len(storeData.saved) != 0 {
		t.Fatal("messageForRecipient must not persist anything")
	}
}

// TestGroupBroadcastAgentTextBlocksPerMember covers the multicast path: the
// agent member sees the annotation text, humans do not, and the shared
// template blocks survive the fan-out without pollution.
func TestGroupBroadcastAgentTextBlocksPerMember(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users: map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9), 8: gatewayAnnotationHuman(8)},
		groupMembers: map[string]bool{
			groupMemberKeyForAnnotations(5, 7): true, groupMemberKeyForAnnotations(5, 8): true, groupMemberKeyForAnnotations(5, 9): true,
		},
		members: map[int64][]*types.GroupMember{5: {{UserID: 7}, {UserID: 8}, {UserID: 9, IsBot: true}}},
		groups:  map[int64]*types.Group{5: {ID: 5, AgentIDs: []int64{9}}},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()
	payload := gatewayAnnotationFanoutPayload(map[string]interface{}{gatewayAnnotationsMetadataKey: value})
	template := hub.messageForRecipient(7, 0, "grp_5", 0, payload, 51)
	template.Data.Mentions = []string{formatUID(9)}
	originalTemplateBlocks := make([]types.ContentBlock, len(template.Data.ContentBlocks))
	copy(originalTemplateBlocks, template.Data.ContentBlocks)

	agentClient := &Client{uid: 9, send: make(chan []byte, 4), accountType: types.AccountBot}
	humanClient := &Client{uid: 8, send: make(chan []byte, 4), accountType: types.AccountHuman}
	hub.addClient(agentClient)
	hub.addClient(humanClient)

	hub.broadcastToGroupWithMentions(5, template, 7, []string{formatUID(9)}, 7, false)

	gotAgent := false
	gotHuman := false
	for name, client := range map[string]*Client{"agent": agentClient, "human": humanClient} {
		select {
		case raw := <-client.send:
			var delivered ServerMessage
			if err := json.Unmarshal(raw, &delivered); err != nil {
				t.Fatalf("%s unmarshal: %v", name, err)
			}
			modelText := xiaoBaEquivalentUserInput(delivered.Data)
			if name == "agent" {
				gotAgent = true
				if !strings.Contains(modelText, "改成蓝色") || !strings.Contains(modelText, "submit-btn") || !strings.Contains(modelText, "/board") {
					t.Fatalf("agent text misses annotation content:\n%s", modelText)
				}
			} else {
				gotHuman = true
				if strings.Contains(modelText, "[Gateway 标注") {
					t.Fatalf("human copy must not carry the annotation block:\n%s", modelText)
				}
			}
		default:
			t.Fatalf("%s did not receive the broadcast", name)
		}
	}
	if !gotAgent || !gotHuman {
		t.Fatalf("deliveries incomplete: agent=%v human=%v", gotAgent, gotHuman)
	}
	if len(template.Data.ContentBlocks) != len(originalTemplateBlocks) {
		t.Fatal("the shared template content blocks were polluted")
	}
}

// TestPersistedGatewayAnnotationsKeepOriginalBlocks proves the fanout-only
// text-block attachment never reaches the durable store.
func TestPersistedGatewayAnnotationsKeepOriginalBlocks(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(artifactApp{ID: "board", Agent: "9"})
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, gateway.handler())
	// An online agent client so the fanout path that injects blocks runs.
	agentClient := &Client{uid: 9, send: make(chan []byte, 4), accountType: types.AccountBot}
	hub.addClient(agentClient)
	messageHandler := NewMessageHandler(storeData, hub)

	body, _ := json.Marshal(struct {
		TopicID  string                 `json:"topic_id"`
		Content  string                 `json:"content"`
		Metadata map[string]interface{} `json:"metadata"`
	}{TopicID: "p2p_7_9", Content: "看下这个按钮", Metadata: map[string]interface{}{
		gatewayAnnotationsMetadataKey: gatewayAnnotationRequest("board", 9.0, nil),
	}})
	request := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(string(body)))
	request = request.WithContext(withTestUID(7))
	recorder := httptest.NewRecorder()
	messageHandler.HandleSendMessage(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	if len(storeData.saved) != 1 {
		t.Fatalf("saved=%d", len(storeData.saved))
	}
	saved := storeData.saved[0]
	if len(saved.blocks) != 0 && strings.Contains(savedContent(saved), "[Gateway 标注") {
		t.Fatalf("the annotation text block leaked into persistence:\n%s", savedContent(saved))
	}
	if saved.metadata["gateway_annotations"] == nil {
		t.Fatal("the annotations metadata must persist")
	}
	// The agent still received the readable block live.
	select {
	case raw := <-agentClient.send:
		var delivered ServerMessage
		if err := json.Unmarshal(raw, &delivered); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !strings.Contains(xiaoBaEquivalentUserInput(delivered.Data), "改成蓝色") {
			t.Fatal("live agent copy lacked the readable annotation block")
		}
	default:
		t.Fatal("agent did not receive the annotated message")
	}
}

func savedContent(saved gatewayAnnotationSavedMessage) string {
	if saved.blocks == nil {
		return ""
	}
	parts := []string{}
	for _, block := range saved.blocks {
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n")
}

// TestModelTextCarriesEveryContractAnnotationAtMetadataCeiling proves the
// model text block never truncates a contract-legal annotation: building a
// metadata value squeezed against the 16KiB budget with 20 full annotations
// plus region targets (with coordinate space and viewport evidence) and
// asserting every arrival body and target field survives the render byte for
// byte within the 32KiB safety cap.
func TestModelTextCarriesEveryContractAnnotationAtMetadataCeiling(t *testing.T) {
	annotations := make([]interface{}, 0, gatewayAnnotationsMaxAnnotations)
	bodies := make([]string, 0, gatewayAnnotationsMaxAnnotations)
	const accent = "评论第"
	for index := 0; index < gatewayAnnotationsMaxAnnotations; index++ {
		body := strings.Repeat("评", 190) + accent + strconv.Itoa(index)
		annotations = append(annotations, map[string]interface{}{
			"id":   "a" + strconv.Itoa(index),
			"kind": "region",
			"body": body,
			"target": map[string]interface{}{
				"rect":             map[string]interface{}{"x": 0.01, "y": 0.01, "width": 0.4, "height": 0.4},
				"coordinate_space": "viewport",
				"viewport":         map[string]interface{}{"width": 1280.0, "height": 720.0, "scroll_x": 24.0, "scroll_y": 88.0},
			},
		})
		bodies = append(bodies, body)
	}
	value := map[string]interface{}{
		"contract_version": GatewayAnnotationsContractV1,
		"agent_uid":        9.0,
		"app_id":           "board",
		"page":             map[string]interface{}{"path": "/board", "revision": "r7"},
		"annotations":      annotations,
	}
	if _, err := normalizeGatewayAnnotations(value); err != nil {
		t.Fatalf("fixture must stay within the 16KiB contract: %v", err)
	}
	text := gatewayAnnotationsModelText(mustDocument(t, value))
	if len(text) > gatewayAnnotationsMaxModelTextBytes {
		t.Fatalf("model text exceeded the safety cap: %d bytes", len(text))
	}
	if len(text) >= gatewayAnnotationsMaxModelTextBytes {
		t.Fatalf("safety-cap proof relies on headroom; got %d bytes", len(text))
	}
	for _, body := range bodies {
		if !strings.Contains(text, body) {
			t.Fatal("a contract-legal body was truncated in the model text")
		}
	}
	for _, fragment := range []string{"coordinate_space=viewport", "viewport(w=1280,h=720,scroll_x=24,scroll_y=88)"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("target evidence missing: %q", fragment)
		}
	}
}

func mustDocument(t *testing.T, value map[string]interface{}) *gatewayAnnotationsDocument {
	t.Helper()
	document, err := normalizeGatewayAnnotations(value)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if document == nil {
		t.Fatal("empty document")
	}
	return document
}

// TestExportXiaoBaParseFixture writes the real fanout deliveries (target Agent
// and human p2p copy, plus the group agent member) as a JSON fixture under
// /tmp, so the read-only XiaoBa parse-equivalence script
// (/tmp/xiaoba-annotation-parse-check.mjs) can drive them through the rules
// recovered from the actual XiaoBa source. Run with:
//
//	go test ./server/ -run 'TestExportXiaoBaParseFixture' && \
//	node /tmp/xiaoba-annotation-parse-check.mjs /tmp/xiaoba-annotation-fixture.json
func TestExportXiaoBaParseFixture(t *testing.T) {
	if os.Getenv("GATEWAY_ANNOTATIONS_EXPORT_FIXTURE") == "" {
		t.Skip("set GATEWAY_ANNOTATIONS_EXPORT_FIXTURE to write /tmp/xiaoba-annotation-fixture.json")
	}
	storeData := &gatewayAnnotationFakeStore{
		users: map[int64]*types.User{
			7: gatewayAnnotationHuman(7), 8: gatewayAnnotationHuman(8), 9: gatewayAnnotationBot(9),
		},
		botOwners:    map[int64]int64{9: 7},
		groupMembers: map[string]bool{groupMemberKeyForAnnotations(5, 7): true, groupMemberKeyForAnnotations(5, 9): true},
		members:      map[int64][]*types.GroupMember{5: {{UserID: 7}, {UserID: 9, IsBot: true}}},
		groups:       map[int64]*types.Group{5: {ID: 5, AgentIDs: []int64{9}}},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()

	p2pPayload := &normalizedMessagePayload{
		StoredContent:  "看下这个按钮",
		DisplayContent: "看下这个按钮",
		StoredType:     "text",
		DisplayType:    "text",
		ContentBlocks:  []types.ContentBlock{{Type: "text", Text: "看下这个按钮"}},
		Metadata:       map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	agentP2P := hub.messageForRecipient(7, 9, "p2p_7_9", 0, p2pPayload, 61)
	humanP2P := hub.messageForRecipient(7, 7, "p2p_7_9", 0, p2pPayload, 61)
	agentP2PTopLevelOnly := hub.messageForRecipient(7, 9, "p2p_7_9", 0, &normalizedMessagePayload{
		StoredContent:  "看下这里",
		DisplayContent: "看下这里",
		StoredType:     "text",
		DisplayType:    "text",
		ContentBlocks:  []types.ContentBlock{{Type: "image", Payload: map[string]interface{}{"url": "/uploads/a.png"}}},
		Metadata:       map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}, 62)

	groupPayload := gatewayAnnotationFanoutPayload(map[string]interface{}{gatewayAnnotationsMetadataKey: value})
	template := hub.messageForRecipient(7, 0, "grp_5", 0, groupPayload, 63)
	template.Data.Mentions = []string{formatUID(9)}
	agentClient := &Client{uid: 9, send: make(chan []byte, 4), accountType: types.AccountBot}
	hub.addClient(agentClient)
	hub.broadcastToGroupWithMentions(5, template, 7, []string{formatUID(9)}, 7, false)
	raw := <-agentClient.send
	var groupAgent ServerMessage
	if err := json.Unmarshal(raw, &groupAgent); err != nil {
		t.Fatalf("unmarshal group delivery: %v", err)
	}

	type modelMessage struct {
		Topic         string                   `json:"topic"`
		Content       interface{}              `json:"content"`
		ContentBlocks []map[string]interface{} `json:"content_blocks"`
	}
	normalizeBlocks := func(data *MsgServerData) []map[string]interface{} {
		blocks := []map[string]interface{}{}
		for _, block := range data.ContentBlocks {
			entry := map[string]interface{}{"type": block.Type, "text": block.Text}
			if block.Payload != nil {
				entry["payload"] = block.Payload
			}
			blocks = append(blocks, entry)
		}
		return blocks
	}
	fixture := []struct {
		Name    string       `json:"name"`
		Message modelMessage `json:"message"`
		Expect  []string     `json:"expect"`
		Forbid  []string     `json:"forbid"`
	}{
		{Name: "p2p agent copy sees annotation and user text",
			Message: modelMessage{Topic: "p2p_7_9", Content: agentP2P.Data.Content, ContentBlocks: normalizeBlocks(agentP2P.Data)},
			Expect:  []string{"看下这个按钮", "[Gateway 标注", "board", "/board", "revision r7", "发布按钮", "改成蓝色", "element_id=submit-btn", "selector=button#submit"},
			Forbid:  []string{"/forged"}},
		{Name: "p2p human copy stays untouched",
			Message: modelMessage{Topic: "p2p_7_9", Content: humanP2P.Data.Content, ContentBlocks: normalizeBlocks(humanP2P.Data)},
			Expect:  []string{"看下这个按钮"},
			Forbid:  []string{"[Gateway 标注", "改成蓝色"}},
		{Name: "p2p agent copy with top-level-only content keeps user text",
			Message: modelMessage{Topic: "p2p_7_9", Content: agentP2PTopLevelOnly.Data.Content, ContentBlocks: normalizeBlocks(agentP2PTopLevelOnly.Data)},
			Expect:  []string{"看下这里", "[Gateway 标注", "改成蓝色"},
			Forbid:  nil},
		{Name: "group agent member sees annotation, user text stays",
			Message: modelMessage{Topic: "grp_5", Content: groupAgent.Data.Content, ContentBlocks: normalizeBlocks(groupAgent.Data)},
			Expect:  []string{"改成蓝色", "submit-btn", "/board"},
			Forbid:  nil},
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile("/tmp/xiaoba-annotation-fixture.json", encoded, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Log("fixture written to /tmp/xiaoba-annotation-fixture.json")
}

// TestModelTextBlockMergeKeepsUserTextForNonExactTypes covers the real
// XiaoBa exact-type rule: only lowercase "text" participates in the block
// merge, so "TEXT"/" text " copies must still carry the user's own text via
// the materialized fallback block instead of being shadowed.
func TestModelTextBlockMergeKeepsUserTextForNonExactTypes(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()

	for _, kind := range []string{"TEXT", " text ", "Text"} {
		t.Run(kind, func(t *testing.T) {
			payload := &normalizedMessagePayload{
				StoredContent:  "original-user-body",
				DisplayContent: "original-user-body",
				StoredType:     "text",
				DisplayType:    "text",
				ContentBlocks:  []types.ContentBlock{{Type: kind, Text: "ignored-noncanonical-block"}},
				Metadata:       map[string]interface{}{gatewayAnnotationsMetadataKey: value},
			}
			agentMessage := hub.messageForRecipient(7, 9, "p2p_7_9", 0, payload, 71)
			modelText := xiaoBaEquivalentUserInput(agentMessage.Data)
			if !strings.Contains(modelText, "original-user-body") {
				t.Fatalf("actual XiaoBa text lost original-user-body for kind %q:\n%s", kind, modelText)
			}
			if !strings.Contains(modelText, "[Gateway 标注") || !strings.Contains(modelText, "改成蓝色") {
				t.Fatalf("annotation context missing for kind %q:\n%s", kind, modelText)
			}
			// The human copy keeps the payload blocks byte-identical.
			humanMessage := hub.messageForRecipient(7, 7, "p2p_7_9", 0, payload, 71)
			if xiaoBaEquivalentUserInput(humanMessage.Data) != "original-user-body" {
				t.Fatalf("human copy changed for kind %q: %q", kind, xiaoBaEquivalentUserInput(humanMessage.Data))
			}
		})
	}
}

// TestAgentHistoryCopyCarriesAnnotationContextForCloudRestore covers the
// offline read path: the authorized Agent's history copy carries the
// annotation context inside the content string (the field cloud-session-restore
// actually reads), while human reads and the stored message stay untouched.
func TestAgentHistoryCopyCarriesAnnotationContextForCloudRestore(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()
	message := &types.Message{
		ID: 72, TopicID: "p2p_7_9", FromUID: 7, Content: "original-user-body", MsgType: "text",
		Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}

	agentRead := hub.historyMessageDataForRecipient(9, message)
	restoredText := cloudRestoreEquivalentText(agentRead)
	for _, fragment := range []string{"original-user-body", "[Gateway 标注", "board", "/board", "revision r7", "改成蓝色", "element_id=submit-btn", "selector=button#submit"} {
		if !strings.Contains(restoredText, fragment) {
			t.Fatalf("actual cloud history content omits annotation body/target: %q", fragment)
		}
	}
	// Human reader and the stored message stay untouched.
	humanRead := hub.historyMessageDataForRecipient(7, message)
	if humanRead.Content != "original-user-body" {
		t.Fatalf("human history copy changed: %v", humanRead.Content)
	}
	if len(humanRead.ContentBlocks) != 0 {
		t.Fatalf("human history blocks changed: %+v", humanRead.ContentBlocks)
	}
	if message.Content != "original-user-body" || len(message.ContentBlocks) != 0 {
		t.Fatal("the stored message was mutated")
	}
	// The author reading its own history gets no injected copy either.
	if authorRead := hub.historyMessageDataForRecipient(7, message); authorRead.Content != "original-user-body" {
		t.Fatalf("author copy changed: %v", authorRead.Content)
	}
	// The history API passes the same per-recipient copy through.
	apiRead := hub.historyAPIMessageForRecipient(9, message)
	if apiContent, _ := apiRead["content"].(string); !strings.Contains(apiContent, "[Gateway 标注") {
		t.Fatalf("history API lost the annotation context: %q", apiContent)
	}

	t.Run("rich image content appends to the rendered description", func(t *testing.T) {
		richMessage := &types.Message{
			ID: 73, TopicID: "p2p_7_9", FromUID: 7, MsgType: "image",
			Content:  `{"type":"image","payload":{"url":"/uploads/a.png","name":"a.png","description":"截图"}}`,
			Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value},
		}
		agentRead := hub.historyMessageDataForRecipient(9, richMessage)
		restoredText := cloudRestoreEquivalentText(agentRead)
		if !strings.Contains(restoredText, "截图") || !strings.Contains(restoredText, "[Gateway 标注") {
			t.Fatalf("rich description lost the annotation: %q", restoredText)
		}
		if richMessage.Content == "" || strings.Contains(richMessage.Content, "[Gateway 标注") {
			t.Fatal("the stored rich content was mutated")
		}
		humanRead := hub.historyMessageDataForRecipient(7, richMessage)
		if restored := cloudRestoreEquivalentText(humanRead); strings.Contains(restored, "[Gateway 标注") {
			t.Fatalf("human rich copy changed: %q", restored)
		}
	})

	t.Run("rich content with payload text keeps url and appends there", func(t *testing.T) {
		// payload.text wins over payload.description in cloudMessageText, so
		// the agent's copy must append into the winning field.
		textMessage := &types.Message{
			ID: 76, TopicID: "p2p_7_9", FromUID: 7, MsgType: "file",
			Content:  `{"type":"file","payload":{"url":"/uploads/report.xlsx","name":"report.xlsx","text":"季度报表"}}`,
			Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value},
		}
		agentRead := hub.historyMessageDataForRecipient(9, textMessage)
		restoredText := cloudRestoreEquivalentText(agentRead)
		if !strings.Contains(restoredText, "[历史文件：report.xlsx]") || !strings.Contains(restoredText, "季度报表") || !strings.Contains(restoredText, "[Gateway 标注") || !strings.Contains(restoredText, "改成蓝色") {
			t.Fatalf("agent rich copy lost the annotation in payload.text: %q", restoredText)
		}
		if strings.Contains(textMessage.Content, "[Gateway 标注") {
			t.Fatal("the stored rich content was mutated")
		}
		humanRead := hub.historyMessageDataForRecipient(7, textMessage)
		if restored := cloudRestoreEquivalentText(humanRead); strings.Contains(restored, "[Gateway 标注") {
			t.Fatalf("human rich copy changed: %q", restored)
		}
	})

	t.Run("rich content without description carries the annotation in description", func(t *testing.T) {
		// cloudMessageText never falls back to content_blocks for rich
		// file/image/voice content, so the agent's copy gains a description.
		bareMessage := &types.Message{
			ID: 74, TopicID: "p2p_7_9", FromUID: 7, MsgType: "image",
			Content:  `{"type":"image","payload":{"url":"/uploads/a.png"}}`,
			Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value},
		}
		agentRead := hub.historyMessageDataForRecipient(9, bareMessage)
		restoredText := cloudRestoreEquivalentText(agentRead)
		if !strings.Contains(restoredText, "[历史图片]") || !strings.Contains(restoredText, "[Gateway 标注") || !strings.Contains(restoredText, "改成蓝色") {
			t.Fatalf("agent rich copy lost the annotation: %q", restoredText)
		}
		if strings.Contains(bareMessage.Content, "[Gateway 标注") {
			t.Fatal("the stored rich content was mutated")
		}
		humanRead := hub.historyMessageDataForRecipient(7, bareMessage)
		if restored := cloudRestoreEquivalentText(humanRead); strings.Contains(restored, "[Gateway 标注") {
			t.Fatalf("human rich copy changed: %q", restored)
		}
	})

	t.Run("foreign agent history reads stay clean", func(t *testing.T) {
		// A reader that is not the annotated Agent gets no annotation text.
		other := &types.Message{
			ID: 75, TopicID: "p2p_7_10", FromUID: 7, Content: "plain", MsgType: "text",
			Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value},
		}
		if read := hub.historyMessageDataForRecipient(9, other); read.Content != "plain" {
			t.Fatalf("foreign topic reader copy changed: %v", read.Content)
		}
	})
}

// TestExportXiaoBaHistoryFixture writes the offline history deliveries (the
// authorized Agent's read copy plus the human reader copy) for every rich
// payload shape, so the reviewer's actual XiaoBa cloudMessageText AST script
// can re-verify the current code path:
//
//	GATEWAY_ANNOTATIONS_EXPORT_FIXTURE=1 go test ./server/ -run 'TestExportXiaoBaHistoryFixture' && \
//	node /tmp/pr575-actual-xiaoba-cloud.cjs /tmp/xiaoba-annotation-history-fixture.json
func TestExportXiaoBaHistoryFixture(t *testing.T) {
	if os.Getenv("GATEWAY_ANNOTATIONS_EXPORT_FIXTURE") == "" {
		t.Skip("set GATEWAY_ANNOTATIONS_EXPORT_FIXTURE to write /tmp/xiaoba-annotation-history-fixture.json")
	}
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()

	type modelMessage struct {
		Content       interface{}              `json:"content"`
		ContentBlocks []map[string]interface{} `json:"content_blocks"`
		Metadata      map[string]interface{}   `json:"metadata"`
		FromUID       int64                    `json:"from_uid"`
	}
	type entry struct {
		Name    string       `json:"name"`
		Message modelMessage `json:"message"`
		Expect  []string     `json:"expect"`
		Forbid  []string     `json:"forbid"`
	}
	entries := []entry{}

	appendRead := func(name string, message *types.Message, recipientUID int64, expect, forbid []string) {
		read := hub.historyMessageDataForRecipient(recipientUID, message)
		blocks := []map[string]interface{}{}
		for _, block := range read.ContentBlocks {
			blocks = append(blocks, map[string]interface{}{"type": block.Type, "text": block.Text})
		}
		entries = append(entries, entry{
			Name: name,
			Message: modelMessage{
				Content: read.Content, ContentBlocks: blocks, Metadata: read.Metadata,
				FromUID: message.FromUID,
			},
			Expect: expect, Forbid: forbid,
		})
	}

	agentExpect := []string{"改成蓝色", "submit-btn", "/board"}
	richShapes := []struct {
		msgType string
		payload string
	}{
		{"file", `{"type":"file","payload":{"name":"f.pdf","url":"/uploads/f.pdf"}}`},
		{"file", `{"type":"file","payload":{"name":"f.pdf","url":"/uploads/f.pdf","text":"original-rich-text"}}`},
		{"file", `{"type":"file","payload":{"name":"f.pdf","url":"/uploads/f.pdf","description":"original-rich-description"}}`},
		{"image", `{"type":"image","payload":{"name":"f.pdf","url":"/uploads/f.pdf"}}`},
		{"image", `{"type":"image","payload":{"name":"f.pdf","url":"/uploads/f.pdf","text":"original-rich-text"}}`},
		{"image", `{"type":"image","payload":{"name":"f.pdf","url":"/uploads/f.pdf","description":"original-rich-description"}}`},
		{"voice", `{"type":"voice","payload":{"name":"f.pdf","url":"/uploads/f.pdf"}}`},
		{"voice", `{"type":"voice","payload":{"name":"f.pdf","url":"/uploads/f.pdf","text":"original-rich-text"}}`},
		{"voice", `{"type":"voice","payload":{"name":"f.pdf","url":"/uploads/f.pdf","description":"original-rich-description"}}`},
	}
	for _, shape := range richShapes {
		message := &types.Message{
			ID: 81, TopicID: "p2p_7_9", FromUID: 7, MsgType: shape.msgType,
			Content:  shape.payload,
			Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value},
		}
		appendRead("agent "+shape.msgType+" "+shape.payload, message, 9, agentExpect, nil)
		// Human expectations follow the original payload only.
		humanExpect := []string{}
		if strings.Contains(shape.payload, "original-rich-text") {
			humanExpect = append(humanExpect, "original-rich-text")
		}
		if strings.Contains(shape.payload, "original-rich-description") {
			humanExpect = append(humanExpect, "original-rich-description")
		}
		appendRead("human "+shape.msgType+" "+shape.payload, message, 7, humanExpect, []string{"Gateway 标注", "改成蓝色"})
	}

	plain := &types.Message{
		ID: 82, TopicID: "p2p_7_9", FromUID: 7, Content: "original-user-body", MsgType: "text",
		Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	appendRead("agent plain", plain, 9, []string{"original-user-body", "改成蓝色", "submit-btn"}, nil)
	appendRead("human plain", plain, 7, []string{"original-user-body"}, []string{"Gateway 标注"})

	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile("/tmp/xiaoba-annotation-history-fixture.json", encoded, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Log("history fixture written to /tmp/xiaoba-annotation-history-fixture.json")
}

// ---------- Round-2 regressions (review /tmp/catsco-pr575-review-round2.md,
// P1-4/P1-5/P2-2): the annotated Agent's read copy must satisfy BOTH real
// XiaoBa consumers of the same message — the live parse merge (blocks win:
// mergedText = exact "text" blocks || top-level string content) and the cloud
// restore reader (non-empty string content wins; rich file/image/voice always
// render payload.text || payload.description with JS truthiness) — while
// human reads and the stored message stay untouched. ----------

func (s *gatewayAnnotationFakeStore) GetMessages(topicID string, limit, offset int) ([]*types.Message, error) {
	if s.storedHistory == nil {
		return nil, nil
	}
	return s.storedHistory[topicID], nil
}

func (s *gatewayAnnotationFakeStore) GetMessagesSince(topicID string, sinceID int64, limit int) ([]*types.Message, error) {
	if s.storedHistory == nil {
		return nil, nil
	}
	return s.storedHistory[topicID], nil
}

func TestR2BlocksOnlyCloudHistoryKeepsOriginalAndAnnotation(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()
	message := &types.Message{
		ID: 1, TopicID: "p2p_7_9", FromUID: 7, MsgType: "text", Content: "",
		ContentBlocks: []types.ContentBlock{{Type: "text", Text: "original-blocks-only"}},
		Metadata:      map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	read := hub.historyMessageDataForRecipient(9, message)
	actual := cloudRestoreEquivalentText(read)
	for _, fragment := range []string{"original-blocks-only", "改成蓝色"} {
		if !strings.Contains(actual, fragment) {
			t.Errorf("missing %q actual=%q", fragment, actual)
		}
	}
	if message.Content != "" || len(message.ContentBlocks) != 1 {
		t.Fatal("stored message mutated")
	}
	human := hub.historyMessageDataForRecipient(7, message)
	if human.Content != "" || len(human.ContentBlocks) != 1 || human.ContentBlocks[0].Text != "original-blocks-only" {
		t.Fatalf("human polluted: %+v", human)
	}
	if strings.Contains(cloudRestoreEquivalentText(human), "Gateway 标注") {
		t.Fatal("human copy gained the annotation")
	}
}

func TestR2HistoryWSReplayReadableInActualLiveBlockMerge(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()
	message := &types.Message{
		ID: 1, TopicID: "p2p_7_9", FromUID: 7, MsgType: "text", Content: "original",
		ContentBlocks: []types.ContentBlock{{Type: "text", Text: "original"}},
		Metadata:      map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	read := hub.historyMessageDataForRecipient(9, message)
	actual := xiaoBaEquivalentUserInput(read)
	for _, fragment := range []string{"original", "改成蓝色"} {
		if !strings.Contains(actual, fragment) {
			t.Errorf("missing %q actual=%q", fragment, actual)
		}
	}
	// The same read copy also satisfies the cloud string-content preference.
	restored := cloudRestoreEquivalentText(read)
	if !strings.Contains(restored, "original") || !strings.Contains(restored, "改成蓝色") {
		t.Fatalf("cloud channel lost fragments: %q", restored)
	}
}

// TestR2AgentReaderShapeMatrix locks the tricky history shapes: JS truthiness
// on rich payload fields, missing payload (flat rich), unknown objects and
// blocks-only content, verified against the real consumer semantics.
func TestR2AgentReaderShapeMatrix(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()
	cases := []struct {
		name                  string
		content               string
		blocks                []types.ContentBlock
		expectedCloudOriginal bool // does the real cloud consumer render the original text?
		expectedHumanOriginal bool // does the human's copy render it?
	}{
		{"object text field", `{"text":"original"}`, nil, true, true},
		{"rich whitespace text", `{"type":"file","payload":{"url":"/uploads/a","text":" ","description":"original"}}`, nil, false, false},
		{"rich missing payload", `{"type":"file","name":"f.pdf","url":"/uploads/f.pdf","text":"original"}`, nil, true, true},
		{"rich no payload at all", `{"type":"image","name":"p.png","url":"/uploads/p.png"}`, nil, false, false},
		{"plain string", "original", nil, true, true},
		{"blocks-only", "", []types.ContentBlock{{Type: "text", Text: "original"}}, true, true},
		{"blocks with attachment", "", []types.ContentBlock{{Type: "image", Payload: map[string]interface{}{"url": "/uploads/p.png"}}, {Type: "text", Text: "original"}}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := &types.Message{
				ID: 1, TopicID: "p2p_7_9", FromUID: 7, MsgType: "text", Content: tc.content,
				ContentBlocks: tc.blocks,
				Metadata:      map[string]interface{}{gatewayAnnotationsMetadataKey: value},
			}
			read := hub.historyMessageDataForRecipient(9, message)
			// Cloud channel (HTTP history consumer): the annotation must be
			// readable, and the original text must survive wherever the real
			// consumer would have rendered it (a " " payload.text carries no
			// substantive original text, so only the annotation is required
			// there — the JS-native text-over-description shadowing stays).
			restored := cloudRestoreEquivalentText(read)
			if !strings.Contains(restored, "改成蓝色") || !strings.Contains(restored, "element_id=submit-btn") {
				t.Fatalf("cloud consumer missing annotation: %q", restored)
			}
			if tc.expectedCloudOriginal && !strings.Contains(restored, "original") {
				t.Fatalf("cloud consumer lost original text: %q", restored)
			}
			// Live/WS replay channel (block merge consumer): annotation always
			// visible; the original text assertion applies to the shapes whose
			// live semantics carried text (string content or text blocks) —
			// object/rich shapes have no live text semantics to preserve.
			merged := xiaoBaEquivalentUserInput(read)
			if !strings.Contains(merged, "改成蓝色") {
				t.Fatalf("live consumer missing annotation: %q", merged)
			}
			if tc.blocks != nil && !strings.Contains(merged, "original") {
				t.Fatalf("live consumer lost original text: %q", merged)
			}
			// Human copy and stored value untouched.
			human := hub.historyMessageDataForRecipient(7, message)
			if strings.Contains(cloudRestoreEquivalentText(human), "Gateway 标注") || strings.Contains(xiaoBaEquivalentUserInput(human), "Gateway 标注") {
				t.Fatalf("human copy polluted: %q / %q", cloudRestoreEquivalentText(human), xiaoBaEquivalentUserInput(human))
			}
			if tc.expectedHumanOriginal && !strings.Contains(cloudRestoreEquivalentText(human), "original") {
				t.Fatalf("human copy lost original: %q", cloudRestoreEquivalentText(human))
			}
		})
	}
}

// TestR2HTTPHistoryAPIAgentCopyThroughRealHandler drives the real HTTP
// history API: the authorized Agent's response carries original text plus
// annotation in every shape, human responses stay clean, attachments survive.
func TestR2HTTPHistoryAPIAgentCopyThroughRealHandler(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()
	message := &types.Message{
		ID: 7, TopicID: "p2p_7_9", FromUID: 7, MsgType: "text", Content: "original",
		ContentBlocks: []types.ContentBlock{{Type: "text", Text: "original"}},
		Metadata:      map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	blocksOnly := &types.Message{
		ID: 8, TopicID: "p2p_7_9", FromUID: 7, MsgType: "text", Content: "",
		ContentBlocks: []types.ContentBlock{{Type: "text", Text: "original-blocks-only"}},
		Metadata:      map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	richMessage := &types.Message{
		ID: 9, TopicID: "p2p_7_9", FromUID: 7, MsgType: "file",
		Content:  `{"type":"file","payload":{"name":"f.pdf","url":"/uploads/f.pdf","text":" "}}`,
		Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	storeData.storedHistory = map[string][]*types.Message{"p2p_7_9": {message, blocksOnly, richMessage}}
	handler := NewMessageHandler(storeData, hub)

	request := httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=p2p_7_9&limit=10", nil)
	request = request.WithContext(withTestUID(9))
	recorder := httptest.NewRecorder()
	handler.HandleGetMessages(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("agent history status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("messages=%d", len(payload.Messages))
	}
	joined := ""
	for _, entry := range payload.Messages {
		entryJSON, _ := json.Marshal(entry)
		joined += string(entryJSON) + "\n"
	}
	for _, fragment := range []string{"original", "original-blocks-only", "改成蓝色", "element_id=submit-btn", "f.pdf", "/uploads/f.pdf"} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("agent HTTP history missing %q in:\n%s", fragment, joined)
		}
	}

	// The human reader gets clean copies of the same rows.
	request = httptest.NewRequest(http.MethodGet, "/api/messages?topic_id=p2p_7_9&limit=10", nil)
	request = request.WithContext(withTestUID(7))
	recorder = httptest.NewRecorder()
	handler.HandleGetMessages(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("human history status=%d", recorder.Code)
	}
	// Decode into a fresh struct: reusing the agent-response value would let
	// json.Unmarshal keep stale keys on messages whose human copy omits
	// content_blocks (null leaves existing maps untouched).
	var humanPayload struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &humanPayload); err != nil {
		t.Fatalf("human unmarshal: %v", err)
	}
	joined = ""
	for _, entry := range humanPayload.Messages {
		entryContent, _ := json.Marshal(map[string]interface{}{
			"content": entry["content"], "content_blocks": entry["content_blocks"],
		})
		if strings.Contains(string(entryContent), "Gateway 标注") {
			t.Fatalf("human HTTP history content polluted: %s", entryContent)
		}
		joined += string(entryContent) + "\n"
	}
	if strings.Contains(joined, "Gateway 标注") {
		t.Fatalf("human HTTP history content polluted: %s", joined)
	}
	for _, fragment := range []string{"original", "original-blocks-only", "f.pdf"} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("human HTTP history lost %q", fragment)
		}
	}
	if message.Content != "original" || len(message.ContentBlocks) != 1 || strings.Contains(message.Content, "Gateway 标注") {
		t.Fatal("stored messages mutated by the history read")
	}
}

// TestR2WSHistoryGetThroughRealHandler drives the real WS history handler and
// asserts the delivered data messages are readable through the actual live
// block-merge semantics while humans get clean replays.
func TestR2WSHistoryGetThroughRealHandler(t *testing.T) {
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()
	message := &types.Message{
		ID: 7, TopicID: "p2p_7_9", FromUID: 7, MsgType: "text", Content: "original",
		ContentBlocks: []types.ContentBlock{{Type: "text", Text: "original"}},
		Metadata:      map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	blocksOnly := &types.Message{
		ID: 8, TopicID: "p2p_7_9", FromUID: 7, MsgType: "text", Content: "",
		ContentBlocks: []types.ContentBlock{{Type: "text", Text: "original-blocks-only"}},
		Metadata:      map[string]interface{}{gatewayAnnotationsMetadataKey: value},
	}
	storeData.storedHistory = map[string][]*types.Message{"p2p_7_9": {message, blocksOnly}}

	agentClient := &Client{uid: 9, send: make(chan []byte, 8), accountType: types.AccountBot}
	hub.addClient(agentClient)
	hub.handleGet(agentClient, &MsgClientGet{ID: "h1", What: "history", Topic: "p2p_7_9", SeqID: 0})

	annotationSeen := 0
	originalSeen := 0
	for {
		select {
		case raw := <-agentClient.send:
			var delivered ServerMessage
			if err := json.Unmarshal(raw, &delivered); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if delivered.Data != nil {
				merged := xiaoBaEquivalentUserInput(delivered.Data)
				if strings.Contains(merged, "original") {
					originalSeen++
				}
				if strings.Contains(merged, "改成蓝色") {
					annotationSeen++
				}
			}
			if delivered.Ctrl != nil && delivered.Ctrl.Code == 200 {
				// history complete
				if annotationSeen != 2 || originalSeen != 2 {
					t.Fatalf("ws history: originals=%d annotations=%d", originalSeen, annotationSeen)
				}
				return
			}
		default:
			t.Fatalf("ws history incomplete: originals=%d annotations=%d", originalSeen, annotationSeen)
		}
	}
}

// ---------- Real-consumer fixtures: the matrix below is exported for the
// vendored XiaoBa AST runner (server/testdata/xiaoba-consumer-ast-runner.cjs,
// configured via XIAOBA_ROOT) so the actual XiaoBa source — not a Go
// approximation — validates live parseMessage and cloud cloudMessageText on
// every legal shape. ----------

// TestR2ExportConsumerFixtures writes both pipelines' fixture files:
// /tmp/xiaoba-r2-live-fixture.json (WS replay / live fanout through
// parseMessage) and /tmp/xiaoba-r2-cloud-fixture.json (HTTP history / session
// rebuild through cloudMessageText). Shapes cover plain string content,
// blocks-only, object {text}, rich whitespace text, rich text, rich
// description, flat rich without payload, and rich without any text field;
// every agent entry expects original text (where the consumer would render
// it) plus the annotation, keeps name/url, and human copies forbid both.
func TestR2ExportConsumerFixtures(t *testing.T) {
	if os.Getenv("GATEWAY_ANNOTATIONS_EXPORT_FIXTURE") == "" {
		t.Skip("set GATEWAY_ANNOTATIONS_EXPORT_FIXTURE to write /tmp/xiaoba-r2-{live,cloud}-fixture.json")
	}
	storeData := &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7},
	}
	hub := newAnnotationHub(t, storeData, &staticAppResolver{apps: map[string]string{"board": "9"}})
	value, _ := annotationDocForModelText().restamped()

	shapes := []struct {
		name    string
		content string
		blocks  []types.ContentBlock
	}{
		{"plain string", "original", nil},
		{"blocks-only", "", []types.ContentBlock{{Type: "text", Text: "original-blocks-only"}}},
		{"object text field", `{"text":"original"}`, nil},
		{"rich whitespace text", `{"type":"file","payload":{"url":"/uploads/f.pdf","name":"f.pdf","text":" ","description":"original"}}`, nil},
		{"rich text", `{"type":"file","payload":{"url":"/uploads/f.pdf","name":"f.pdf","text":"original-rich-text"}}`, nil},
		{"rich description", `{"type":"file","payload":{"url":"/uploads/f.pdf","name":"f.pdf","description":"original-description"}}`, nil},
		{"rich missing payload", `{"type":"file","name":"f.pdf","url":"/uploads/f.pdf","text":"original"}`, nil},
		{"rich no text fields", `{"type":"image","name":"p.png","url":"/uploads/p.png"}`, nil},
	}
	agentRead := func(message *types.Message) *MsgServerData {
		return hub.historyMessageDataForRecipient(9, message)
	}
	humanRead := func(message *types.Message) *MsgServerData {
		return hub.historyMessageDataForRecipient(7, message)
	}

	type modelMessage struct {
		Content       interface{}              `json:"content"`
		ContentBlocks []map[string]interface{} `json:"content_blocks"`
		Metadata      map[string]interface{}   `json:"metadata"`
		TopicID       string                   `json:"topic_id"`
		FromUID       int64                    `json:"from_uid"`
		Type          string                   `json:"type"`
		MsgType       string                   `json:"msg_type"`
		SeqID         int64                    `json:"seq_id"`
	}
	type fixtureEntry struct {
		Name          string       `json:"name"`
		Pipeline      string       `json:"pipeline"`
		Message       modelMessage `json:"message"`
		Expect        []string     `json:"expect"`
		Forbid        []string     `json:"forbid"`
		ExpectedFiles []string     `json:"expectedFiles,omitempty"`
	}
	liveFixtures := []fixtureEntry{}
	cloudFixtures := []fixtureEntry{}
	for _, shape := range shapes {
		message := &types.Message{
			ID: 1, TopicID: "p2p_7_9", FromUID: 7, MsgType: "text", Content: shape.content,
			ContentBlocks: shape.blocks,
			Metadata:      map[string]interface{}{gatewayAnnotationsMetadataKey: value},
		}
		read := agentRead(message)
		clean := func(data *MsgServerData) modelMessage {
			blocks := []map[string]interface{}{}
			for _, block := range data.ContentBlocks {
				blocks = append(blocks, map[string]interface{}{"type": block.Type, "text": block.Text, "payload": block.Payload})
			}
			return modelMessage{
				Content: data.Content, ContentBlocks: blocks, Metadata: data.Metadata,
				TopicID: data.Topic, FromUID: message.FromUID, Type: data.Type,
				MsgType: data.MsgType, SeqID: int64(data.SeqID),
			}
		}
		agent := clean(read)
		human := clean(humanRead(message))

		// Live pipeline expectations: the block merge always sees the
		// annotation; the original text appears for shapes whose live
		// semantics carried text (string content or exact text blocks).
		// Attachment names/urls ride the result.files channel (expectedFiles),
		// never the merged text, and only shapes whose content carries a real
		// `payload` object are collected by the live parser at all.
		liveExpect := []string{"改成蓝色", "element_id=submit-btn"}
		liveForbid := []string{"/forged"}
		if shape.blocks != nil || (strings.Contains(shape.content, "original") && !strings.Contains(shape.content, `"`)) {
			liveExpect = append(liveExpect, "original")
		}
		liveFiles := liveFileURLs(shape)
		liveFixtures = append(liveFixtures,
			fixtureEntry{Name: "agent " + shape.name + " (live)", Pipeline: "live", Message: agent, Expect: liveExpect, Forbid: liveForbid, ExpectedFiles: liveFiles},
			fixtureEntry{Name: "human " + shape.name + " (live)", Pipeline: "live", Message: human, Expect: liveHumanExpect(shape), Forbid: []string{"改成蓝色", "Gateway 标注"}, ExpectedFiles: liveFiles},
		)

		// Cloud pipeline expectations: the annotation rides content or the
		// rendered description; originals follow real consumer rendering.
		cloudExpect := []string{"改成蓝色", "element_id=submit-btn"}
		cloudForbid := []string{}
		if cloudRendersOriginal(shape) {
			cloudExpect = append(cloudExpect, cloudOriginalFragment(shape))
		}
		if cloudName := cloudRenderedName(shape); cloudName != "" {
			cloudExpect = append(cloudExpect, cloudName)
		}
		cloudFixtures = append(cloudFixtures,
			fixtureEntry{Name: "agent " + shape.name + " (cloud)", Pipeline: "cloud", Message: agent, Expect: cloudExpect, Forbid: cloudForbid},
			fixtureEntry{Name: "human " + shape.name + " (cloud)", Pipeline: "cloud", Message: human, Expect: cloudHumanExpect(shape), Forbid: []string{"改成蓝色", "Gateway 标注"}},
		)
	}

	writeFixture := func(path string, entries []fixtureEntry) {
		encoded, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", path, err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeFixture("/tmp/xiaoba-r2-live-fixture.json", liveFixtures)
	writeFixture("/tmp/xiaoba-r2-cloud-fixture.json", cloudFixtures)
	t.Log("fixtures written to /tmp/xiaoba-r2-live-fixture.json and /tmp/xiaoba-r2-cloud-fixture.json")
}

func liveFileURLs(shape struct {
	name    string
	content string
	blocks  []types.ContentBlock
}) []string {
	urls := []string{}
	// parseMessage only collects attachments from a real `payload` object in
	// the rich content (or from file/image blocks); flat rich shapes without
	// a payload field have no live attachment semantics to preserve.
	if strings.Contains(shape.content, `"payload"`) && strings.Contains(shape.content, "/uploads/f.pdf") {
		urls = append(urls, "/uploads/f.pdf")
	}
	for _, block := range shape.blocks {
		if block.Type == "image" || block.Type == "file" {
			if payload, ok := block.Payload["url"].(string); ok {
				urls = append(urls, payload)
			}
		}
	}
	return urls
}

func liveHumanExpect(shape struct {
	name    string
	content string
	blocks  []types.ContentBlock
}) []string {
	if shape.blocks != nil {
		return []string{"original"}
	}
	if strings.Contains(shape.content, "original") && !strings.Contains(shape.content, `"`) {
		return []string{"original"}
	}
	return nil
}

// cloudWinnerValue returns the description field the real cloudMessageText
// resolves for the shape (payload.text wins over payload.description with raw
// JS truthiness; payload falls back to the rich object itself) and whether a
// field existed at all.
// cloudRenderedName returns the attachment name the real cloudMessageText
// renders inside "[历史X：name]" for this shape.
func cloudRenderedName(shape struct {
	name    string
	content string
	blocks  []types.ContentBlock
}) string {
	if shape.blocks != nil || !strings.Contains(shape.content, `"`) {
		return ""
	}
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(shape.content), &fields); err != nil {
		return ""
	}
	payload, ok := fields["payload"].(map[string]interface{})
	if !ok {
		payload = fields
	}
	name, _ := payload["name"].(string)
	if name == "" {
		return ""
	}
	switch strings.TrimSpace(fmt.Sprint(fields["type"])) {
	case "file", "image":
		return name
	default:
		return ""
	}
}

func cloudWinnerValue(shape struct {
	name    string
	content string
	blocks  []types.ContentBlock
}) (string, bool) {
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(shape.content), &fields); err != nil {
		return "", false
	}
	payload, ok := fields["payload"].(map[string]interface{})
	if !ok {
		payload = fields
	}
	if text, present := payload["text"].(string); present && text != "" {
		return text, true
	}
	if description, present := payload["description"].(string); present && description != "" {
		return description, true
	}
	return "", false
}

func cloudRendersOriginal(shape struct {
	name    string
	content string
	blocks  []types.ContentBlock
}) bool {
	// The real consumer renders the original when a non-empty string content
	// exists, or when the winning description field (payload.text ||
	// payload.description, JS truthiness) carries substantive text. A " " text
	// wins but is blank, and the JS-native shadowing is preserved, so no
	// original text is rendered there.
	if shape.blocks != nil {
		return true
	}
	if !strings.Contains(shape.content, `"`) && shape.content != "" {
		return true
	}
	value, _ := cloudWinnerValue(shape)
	return strings.TrimSpace(value) != ""
}

func cloudOriginalFragment(shape struct {
	name    string
	content string
	blocks  []types.ContentBlock
}) string {
	if shape.blocks != nil {
		return "original-blocks-only"
	}
	value, found := cloudWinnerValue(shape)
	if found && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if !strings.Contains(shape.content, `"`) && shape.content != "" {
		return shape.content
	}
	return "original"
}

func cloudHumanExpect(shape struct {
	name    string
	content string
	blocks  []types.ContentBlock
}) []string {
	if shape.blocks != nil {
		return []string{"original-blocks-only"}
	}
	if !strings.Contains(shape.content, `"`) && shape.content != "" {
		return []string{"original"}
	}
	value, found := cloudWinnerValue(shape)
	if found && strings.TrimSpace(value) != "" {
		return []string{strings.TrimSpace(value)}
	}
	return nil
}

// TestR2RealXiaoBaASTConsumers re-runs the vendored actual-XiaoBa AST runner
// against both exported fixture pipelines whenever a XiaoBa checkout is
// available (GATEWAY_ANNOTATIONS_EXPORT_FIXTURE + XIAOBA_ROOT). CI without
// the XiaoBa source skips this and still runs the Go consumer-semantics
// matrix above.
func TestR2RealXiaoBaASTConsumers(t *testing.T) {
	if os.Getenv("GATEWAY_ANNOTATIONS_EXPORT_FIXTURE") == "" || os.Getenv("XIAOBA_ROOT") == "" {
		t.Skip("set GATEWAY_ANNOTATIONS_EXPORT_FIXTURE=1 and XIAOBA_ROOT to run the real consumer AST regression")
	}
	run := func(fixture string) {
		output, err := exec.Command("node", "testdata/xiaoba-consumer-ast-runner.cjs", fixture).CombinedOutput()
		if err != nil {
			t.Fatalf("real AST runner failed for %s: %v\n%s", fixture, err, output)
		}
		var summary struct {
			Passed int `json:"passed"`
			Failed int `json:"failed"`
		}
		if err := json.Unmarshal(output, &summary); err != nil {
			t.Fatalf("runner output: %v\n%s", err, output)
		}
		if summary.Failed != 0 {
			t.Fatalf("real AST consumer regression failed for %s: %s", fixture, output)
		}
		t.Logf("%s: %d passed", fixture, summary.Passed)
	}
	run("/tmp/xiaoba-r2-live-fixture.json")
	run("/tmp/xiaoba-r2-cloud-fixture.json")
}
