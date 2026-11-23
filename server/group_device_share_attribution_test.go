package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type attributedShareRequestStore struct {
	*groupSharingTestStore
	request store.GroupDeviceShareRequest
	sender  int64
	role    string
	blocks  []types.ContentBlock
	writes  int
}

func (s *attributedShareRequestStore) CreateGroupDeviceShareRequest(_ context.Context, request store.GroupDeviceShareRequest) (*store.GroupDeviceShareRequest, error) {
	if s.request.ID == "" {
		s.request = request
	}
	copy := s.request
	return &copy, nil
}
func (s *attributedShareRequestStore) SetGroupDeviceShareRequestMessage(_ context.Context, id string, message int64) error {
	s.request.MessageID = message
	return nil
}
func (s *attributedShareRequestStore) SaveMessageIdempotent(topic string, from int64, content string, blocks []types.ContentBlock, mode, role, kind string, reply int64, clientID string) (int64, bool, error) {
	s.sender, s.blocks = from, blocks
	s.role = role
	s.writes++
	return 100, false, nil
}

func TestGroupDeviceShareBotCardPreservesActualAuthor(t *testing.T) {
	h, _, base := groupShareCompatibilityFixture(t)
	base.history = []*types.Message{{ID: 42, TopicID: "grp_1", FromUID: 7, MsgType: "text", ContentBlocks: []types.ContentBlock{{Type: "text", Text: "@bot ask owner", Payload: map[string]interface{}{"mentions": []string{"usr42"}}}}}}
	db := &attributedShareRequestStore{groupSharingTestStore: base}
	h.db = db
	handler := NewGroupDeviceShareHandler(h)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/api/groups/device-share-requests", strings.NewReader(`{"action":"create","topic_id":"grp_1","owner_uid":8,"message_id":42,"description":"Bot generated reason"}`))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
		rec := httptest.NewRecorder()
		handler.HandleRequests(rec, req)
		if rec.Code != 200 {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
	}
	if db.request.RequesterUID != 7 || db.sender != 42 || db.role != "assistant" || db.writes != 1 {
		t.Fatalf("attribution/idempotency: requester=%d author=%d writes=%d", db.request.RequesterUID, db.sender, db.writes)
	}
	ref := db.blocks[0].Payload["group_device_share"].(map[string]any)
	if ref["requested_by_bot"] != "usr42" || ref["requester_name"] != "actor" || ref["origin_message_id"] != int64(42) {
		t.Fatalf("missing provenance: %+v", ref)
	}
}
