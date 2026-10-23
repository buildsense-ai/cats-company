package server

import (
	"fmt"
	"log"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const taskStatusOutboxLease = 30 * time.Second

// A committed receipt has one durable notification. The legacy field always
// carries a freshly read topic aggregate; the source field preserves the actual
// receipt even when another agent is still running. Existing clients ignore the
// additive fields and keep their topic-level behavior.
func (h *Hub) dispatchConversationTaskStatusEvents() bool {
	outbox, ok := h.db.(store.ConversationTaskStatusOutboxStore)
	if !ok {
		return false
	}
	statusStore, ok := h.db.(store.ConversationTaskStatusStore)
	if !ok {
		return false
	}
	// Keep one local dispatcher from racing the request path. Other nodes are
	// fenced by outbox claim tokens and lease expiry in the database.
	h.taskStatusDeliveryMu.Lock()
	defer h.taskStatusDeliveryMu.Unlock()
	events, err := outbox.ClaimConversationTaskStatusEvents(32, taskStatusOutboxLease)
	if err != nil {
		log.Printf("task status outbox: claim failed: %v", err)
		return false
	}
	for _, event := range events {
		if event.Status == nil {
			continue
		}
		statuses, err := statusStore.GetConversationTaskStatuses([]string{event.Status.TopicID})
		if err != nil {
			log.Printf("task status outbox: reload id=%d: %v", event.ID, err)
			continue
		}
		aggregate := statuses[event.Status.TopicID]
		if aggregate == nil {
			// An expired receipt must not resurrect a running indicator.
			aggregate = &types.ConversationTaskStatus{TopicID: event.Status.TopicID, State: "idle", UpdatedAt: time.Now().UTC()}
		}
		msg := &ServerMessage{TaskStatus: aggregate, TaskStatusSource: event.Status, TaskStatusEventID: event.ID}
		if err := h.fanoutTaskStatusEvent(event.Status.SourceUID, event.Status.TopicID, msg); err != nil {
			log.Printf("task status outbox: fanout id=%d topic=%s run=%s: %v", event.ID, event.Status.TopicID, event.Status.RunID, err)
			continue
		}
		if err := outbox.CompleteConversationTaskStatusEvent(event.ID, event.Claim); err != nil {
			log.Printf("task status outbox: completion id=%d: %v", event.ID, err)
		}
	}
	return len(events) == 32
}

func (h *Hub) runConversationTaskStatusDelivery() {
	if _, ok := h.db.(store.ConversationTaskStatusOutboxStore); !ok {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if h.dispatchConversationTaskStatusEvents() {
			continue
		}
		select {
		case <-ticker.C:
		case <-h.taskStatusDelivery:
		}
	}
}

func (h *Hub) fanoutTaskStatusEvent(source int64, topic string, msg *ServerMessage) error {
	recipients := []int64{source}
	if isGroupTopic(topic) {
		members, err := h.db.GetGroupMembers(extractGroupID(topic))
		if err != nil {
			return err
		}
		// Resolve visibility completely before sending. A failed bot lookup is
		// retried, rather than treating an unknown member as authorized.
		managedStore, ok := h.db.(store.ChannelManagedGroupStore)
		if !ok {
			return fmt.Errorf("group visibility store unavailable")
		}
		channelManaged, err := managedStore.IsChannelManagedGroup(extractGroupID(topic))
		if err != nil {
			return err
		}
		recipients = nil
		for _, member := range members {
			if channelManaged {
				bot, err := h.db.IsUserBot(member.UserID)
				if err != nil {
					return err
				}
				if !bot {
					continue
				}
			}
			recipients = append(recipients, member.UserID)
		}
	} else {
		peer := extractPeerUID(topic, source)
		if peer == 0 {
			return fmt.Errorf("invalid task status topic %q", topic)
		}
		recipients = append(recipients, peer)
	}
	for _, uid := range recipients {
		if h.sharedRuntime != nil {
			accepted := h.sharedRuntime.broadcastUserMessage(uid, msg)
			// Redis reports publication failure independently of whether a user
			// is online. The memory runtime returns false for an offline user.
			if !accepted && h.sharedRuntime.runtimeMode() == "redis" {
				return fmt.Errorf("shared task status publication failed for uid=%d", uid)
			}
		} else {
			h.SendToUser(uid, msg)
		}
	}
	return nil
}

func (h *Hub) publishCommittedTaskStatus(source int64, aggregate *types.ConversationTaskStatus, exclude *Client) {
	if _, ok := h.db.(store.ConversationTaskStatusOutboxStore); ok {
		if h.taskStatusDelivery == nil {
			h.dispatchConversationTaskStatusEvents()
		} else {
			select {
			case h.taskStatusDelivery <- struct{}{}:
			default:
			}
		}
		return
	}
	// Test stores and third-party adapters can retain the smaller legacy store
	// interface. Production SQL adapters always use the durable outbox.
	h.fanoutConversationTaskStatus(source, aggregate, exclude)
}

func (h *Hub) handleTaskStatusHistory(client *Client, msg *MsgClientGet) {
	if code, text := h.validateTopicReadAccess(client.uid, client.accountType, msg.Topic); code != 0 {
		h.SendToClient(client, &ServerMessage{Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: msg.Topic, Code: code, Text: text}})
		return
	}
	runsStore, ok := h.db.(store.ConversationTaskRunStore)
	if !ok {
		h.SendToClient(client, &ServerMessage{Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: msg.Topic, Code: 503, Text: "task run history unavailable"}})
		return
	}
	if msg.BeforeID < 0 {
		h.SendToClient(client, &ServerMessage{Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: msg.Topic, Code: 400, Text: "before_id must be nonnegative"}})
		return
	}
	runs, err := runsStore.ListConversationTaskRuns(msg.Topic, msg.BeforeID, 100)
	if err != nil {
		log.Printf("task status history: topic=%s: %v", msg.Topic, err)
		h.SendToClient(client, &ServerMessage{Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: msg.Topic, Code: 500, Text: "task run history unavailable"}})
		return
	}
	next := int64(0)
	if len(runs) == 100 {
		next = runs[len(runs)-1].ID
	}
	h.SendToClient(client, &ServerMessage{Ctrl: &MsgServerCtrl{ID: msg.ID, Topic: msg.Topic, Code: 200, Text: "ok", Params: map[string]interface{}{"task_statuses": runs, "next_before_id": next}}})
}
