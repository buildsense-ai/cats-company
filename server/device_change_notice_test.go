package server

import (
	"errors"
	"github.com/openchat/openchat/server/store/types"
	"testing"
)

type deviceNoticeStore struct {
	*groupSharingTestStore
	keys          map[string]int64
	metadata      map[string]interface{}
	fail          bool
	createdTopics []string
	failTopic     bool
}

func (s *deviceNoticeStore) CreateTopic(topic, _ string, _ int64) error {
	if s.failTopic {
		return errors.New("topic storage unavailable")
	}
	s.createdTopics = append(s.createdTopics, topic)
	return nil
}

func (s *deviceNoticeStore) SaveMessageWithMetadata(_ string, _ int64, _ string, _ []types.ContentBlock, _, _, _ string, _ int64, key string, metadata map[string]interface{}) (int64, bool, error) {
	if s.fail {
		return 0, false, errors.New("notice storage unavailable")
	}
	if id, ok := s.keys[key]; ok {
		return id, true, nil
	}
	id := int64(len(s.keys) + 1)
	s.keys[key] = id
	s.metadata = metadata
	return id, false, nil
}

func TestDeviceChangeNoticeCreatesFirstPrivateTopic(t *testing.T) {
	h, bot, base := groupShareCompatibilityFixture(t)
	db := &deviceNoticeStore{groupSharingTestStore: base, keys: map[string]int64{}}
	h.db = db
	for len(bot.send) > 0 {
		<-bot.send
	}
	notice := h.publishDeviceChangeNotice(7, "p2p_7_42", "first-selection", "默认设备已切换。")
	if notice.Pending || len(db.createdTopics) != 1 || db.createdTopics[0] != "p2p_7_42" || len(bot.send) != 0 {
		t.Fatal("first private receipt failed or reached Bot", notice, db.createdTopics)
	}
	db.failTopic = true
	failed := h.publishDeviceChangeNotice(7, "p2p_7_42", "second-selection", "默认设备已切换。")
	if !failed.Pending || len(db.keys) != 1 {
		t.Fatal("failed topic reported success", failed)
	}
}
func TestDeviceChangeNoticeOnlyReachesHumansAndIsIdempotent(t *testing.T) {
	h, bot, base := groupShareCompatibilityFixture(t)
	db := &deviceNoticeStore{groupSharingTestStore: base, keys: map[string]int64{}}
	h.db = db
	actor := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 16)}
	owner := &Client{uid: 8, accountType: types.AccountHuman, send: make(chan []byte, 16)}
	h.addClient(actor)
	h.addClient(owner)
	for _, c := range []*Client{actor, owner, bot} {
		for len(c.send) > 0 {
			<-c.send
		}
	}
	receipt := h.publishDeviceChangeNotice(7, "grp_1", "selected:r1", "默认设备已切换。")
	if receipt.Pending || receipt.MessageID <= 0 || db.metadata["catsco_device_notice"] != true {
		t.Fatal("notice was not persisted", receipt)
	}
	if len(actor.send) != 1 || len(owner.send) != 1 || len(bot.send) != 0 {
		t.Fatal("notice was not human-only", len(actor.send), len(owner.send), len(bot.send))
	}
	repeated := h.publishDeviceChangeNotice(7, "grp_1", "selected:r1", "默认设备已切换。")
	if repeated.MessageID != receipt.MessageID || len(db.keys) != 1 || len(actor.send) != 1 {
		t.Fatal("notice retry duplicated the message")
	}
	db.fail = true
	failed := h.publishDeviceChangeNotice(7, "grp_1", "selected:r2", "尚未确认。")
	if !failed.Pending || failed.MessageID != 0 || len(actor.send) != 1 {
		t.Fatal("failed notice was reported as delivered")
	}
}
