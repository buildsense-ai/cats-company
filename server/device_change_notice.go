package server

import (
	"crypto/sha256"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// Receipts are conversational acknowledgements of committed human actions.
// They are delivered to human messaging connections, never routed to Agents.
type deviceChangeNotice struct {
	MessageID int64 `json:"message_id,omitempty"`
	Pending   bool  `json:"pending,omitempty"`
}

func (h *Hub) publishDeviceChangeNotice(actor int64, topic, eventKey, text string) deviceChangeNotice {
	if h == nil || h.db == nil {
		return deviceChangeNotice{Pending: true}
	}
	db, ok := h.db.(store.MessageMetadataStore)
	if !ok {
		return deviceChangeNotice{Pending: true}
	}
	// Like an ordinary first message, a receipt can be the first persisted
	// content in a private conversation. Its topic must exist before the insert.
	if !isGroupTopic(topic) {
		if err := h.db.CreateTopic(topic, "p2p", actor); err != nil {
			return deviceChangeNotice{Pending: true}
		}
	}
	recipients := []int64{actor}
	if isGroupTopic(topic) {
		members, err := h.db.GetGroupMembers(extractGroupID(topic))
		if err != nil {
			return deviceChangeNotice{Pending: true}
		}
		recipients = nil
		for _, member := range members {
			if !member.IsBot {
				recipients = append(recipients, member.UserID)
			}
		}
	}
	digest := sha256.Sum256([]byte(topic + ":" + eventKey))
	metadata := map[string]interface{}{"catsco_device_notice": true}
	id, duplicate, err := db.SaveMessageWithMetadata(topic, actor, text, nil, "normal", "user", "text", 0, fmt.Sprintf("device-notice-%x", digest), metadata)
	if err != nil {
		return deviceChangeNotice{Pending: true}
	}
	if !duplicate {
		payload := &normalizedMessagePayload{StoredContent: text, DisplayContent: text, StoredType: "text", DisplayType: "text", Mode: "normal", Role: "user", Metadata: metadata}
		for _, uid := range recipients {
			// Membership rows can outlive a disabled Bot; verify account type rather
			// than allowing an informational receipt to become a Bot turn.
			user, err := h.db.GetUser(uid)
			if err == nil && user != nil && user.AccountType == "human" {
				h.SendToUser(uid, h.messageForRecipient(actor, uid, topic, 0, payload, id))
			}
		}
	}
	return deviceChangeNotice{MessageID: id}
}

func (h *Hub) deviceNoticeName(uid int64) string {
	if user, err := h.db.GetUser(uid); err == nil && user != nil {
		return displayNameOrUsername(user.DisplayName, user.Username)
	}
	return formatUID(uid)
}

func (h *Hub) publishGroupShareNotice(s *store.GroupDeviceShare) deviceChangeNotice {
	if s == nil {
		return deviceChangeNotice{Pending: true}
	}
	name := s.DeviceID
	if d, ok := h.userDevices.registeredDevice(s.OwnerUID, s.DeviceID); ok && d.DisplayName != "" {
		name = d.DisplayName
	}
	text := fmt.Sprintf("已授权 %s 在本群通过 Bot 使用我的电脑 %s。", h.deviceNoticeName(s.GranteeUID), name)
	if s.Status == "revoked" {
		text = fmt.Sprintf("已撤销 %s 在本群对我的电脑 %s 的使用授权。", h.deviceNoticeName(s.GranteeUID), name)
	}
	return h.publishDeviceChangeNotice(s.OwnerUID, s.TopicID, fmt.Sprintf("share:%s:%d", s.ID, s.Revision), text)
}
