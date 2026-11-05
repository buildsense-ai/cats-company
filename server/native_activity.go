package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type nativeDeliveryJob struct {
	pushDeliveryJob
	status *types.ConversationTaskStatus
}

type activityContentState struct {
	Title     string `json:"title"`
	Agent     string `json:"agent"`
	State     string `json:"state"`
	Summary   string `json:"summary"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	Updated   int64  `json:"updated"`
	Details   bool   `json:"details"`
}

func (s *NativePushService) enqueueActivityStatus(status *types.ConversationTaskStatus) bool {
	if !s.Enabled() || s.surfaces == nil || status == nil || status.RunID == "" || status.SourceUID <= 0 {
		return false
	}
	s.startNativeWorkers()
	copy := *status
	select {
	case s.activityQueue <- nativeDeliveryJob{pushDeliveryJob: pushDeliveryJob{queuedAt: time.Now()}, status: &copy}:
		return true
	default:
		return false
	}
}

func (s *NativePushService) sendActivityStatus(ctx context.Context, event *types.ConversationTaskStatus) error {
	if !s.Enabled() || s.surfaces == nil || event == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	h := s.surfaces
	reader, err := h.reader()
	if err != nil {
		return err
	}
	recipients := []int64{}
	if isGroupTopic(event.TopicID) {
		managed, member, err := reader.GroupAccess(ctx, extractGroupID(event.TopicID), event.SourceUID)
		if err != nil {
			return err
		}
		if managed || !member {
			return nil
		}
		members, err := reader.Recipients(ctx, extractGroupID(event.TopicID))
		if err != nil {
			return err
		}
		for _, uid := range members {
			if uid != event.SourceUID {
				recipients = append(recipients, uid)
			}
		}
	} else if uid := extractPeerUID(event.TopicID, event.SourceUID); uid > 0 {
		recipients = append(recipients, uid)
	}
	var failures []error
	for _, uid := range recipients {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.sendActivitiesForUser(ctx, uid, event); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func activityVersion(auth string) (eventNano, timestamp int64, digest string) {
	parts := strings.Split(auth, ":")
	if len(parts) == 4 && parts[0] == "activity-v1" {
		eventNano, _ = strconv.ParseInt(parts[1], 10, 64)
		timestamp, _ = strconv.ParseInt(parts[2], 10, 64)
		digest = parts[3]
	}
	return
}

func (s *NativePushService) sendActivitiesForUser(ctx context.Context, uid int64, event *types.ConversationTaskStatus) error {
	cas, ok := s.store.(store.PushSubscriptionVersionStore)
	if !ok {
		return errors.New("activity durable fencing unavailable")
	}
	rows, err := s.store.ListPushSubscriptions(ctx, uid)
	if err != nil {
		return errors.New("activity registrations unavailable")
	}
	var failures []error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if row == nil || row.UID != uid || !strings.HasPrefix(row.Endpoint, activityPushPrefix) || !(row.Auth == "activity-v1" || strings.HasPrefix(row.Auth, "activity-v1:")) {
			continue
		}
		var meta activityRegistrationMetadata
		if json.Unmarshal([]byte(row.P256DH), &meta) != nil {
			continue
		}
		current, err := s.surfaces.activityCurrent(ctx, uid, meta)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if current == nil {
			if err := s.surfaces.deleteVersion(ctx, row); err != nil {
				failures = append(failures, errors.New("could not retire activity"))
			}
			continue
		}
		if meta.Topic != event.TopicID || meta.Run != event.RunID || meta.Source != event.SourceUID {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(row.Endpoint, activityPushPrefix), "/")
		if len(parts) != 3 || parts[1] != s.config.BundleID {
			continue
		}
		input := nativePushRegistration{Token: parts[2], Environment: parts[0], RegistrationID: row.RegistrationID}
		if _, err := s.endpoint(input); err != nil {
			continue
		}
		content := activityContentState{Title: "CatsCo", Agent: "CatsCo", State: current.State, Details: meta.Details}
		if meta.Details {
			reader, err := s.surfaces.reader()
			if err != nil {
				return err
			}
			title, err := reader.Title(ctx, uid, meta.Topic, extractGroupID(meta.Topic), extractPeerUID(meta.Topic, uid))
			if err != nil {
				failures = append(failures, err)
				continue
			}
			content.Title = truncateUTF8(title, 160)
			agent, err := reader.User(ctx, meta.Source)
			if err != nil || agent == nil {
				continue
			}
			content.Agent = truncateUTF8(displayNameOrUsername(agent.DisplayName, agent.Username), 160)
			content.Summary = truncateUTF8(current.Summary, 500)
		}
		// Exclude heartbeat timestamps from the fingerprint. No authoritative
		// step counter exists in the task store, so progress stays at zero.
		signature, _ := json.Marshal(content)
		digest := surfaceHash(string(signature))
		eventNano := current.EventUpdatedAt.UnixNano()
		if current.EventUpdatedAt.IsZero() {
			eventNano = current.UpdatedAt.UnixNano()
		}
		lastEvent, lastTimestamp, lastDigest := activityVersion(row.Auth)
		if eventNano < lastEvent || lastDigest == digest && (current.UpdatedAt.Unix() <= lastTimestamp || lastTimestamp > time.Now().Add(-2*time.Minute).Unix()) {
			continue
		}
		timestamp := current.UpdatedAt.Unix()
		if timestamp <= lastTimestamp {
			timestamp = lastTimestamp + 1
		}
		// ActivityKit orders by integer seconds. Coalescing a burst is safer
		// than producing unbounded timestamps in the future.
		if timestamp > time.Now().Add(30*time.Second).Unix() && !isTerminalTaskStatus(current.State) {
			continue
		}
		content.Updated = current.UpdatedAt.Unix()
		eventName, priority := "update", "5"
		aps := map[string]interface{}{"timestamp": timestamp, "event": eventName, "content-state": content}
		if isTerminalTaskStatus(current.State) {
			aps["event"] = "end"
			aps["dismissal-date"] = time.Now().Unix()
			priority = "10"
		} else {
			staleAt := time.Now().Add(5 * time.Minute).Unix()
			if current.ExpiresAt != nil && current.ExpiresAt.Unix() < staleAt {
				staleAt = current.ExpiresAt.Unix()
			}
			if meta.Expires < staleAt {
				staleAt = meta.Expires
			}
			aps["stale-date"] = staleAt
		}
		data, err := json.Marshal(map[string]interface{}{"aps": aps})
		if err != nil || len(data) > maxPushPayloadLen {
			return errors.New("activity payload too large")
		}
		newAuth := fmt.Sprintf("activity-v1:%d:%d:%s", eventNano, timestamp, digest)
		claimed, err := cas.CompareAndSwapPushSubscriptionAuth(ctx, uid, row.Endpoint, row.RegistrationID, row.P256DH, row.Auth, newAuth)
		if err != nil {
			failures = append(failures, errors.New("could not claim activity update"))
			continue
		}
		if !claimed {
			continue
		}
		allowed := func() bool {
			latest, err := s.surfaces.activityCurrent(ctx, uid, meta)
			if err != nil || latest == nil || latest.State != current.State || latest.Summary != current.Summary || !latest.EventUpdatedAt.Equal(current.EventUpdatedAt) {
				return false
			}
			// Read the durable generation before every provider attempt so a
			// queued retry cannot resurrect a revoked/rotated capability.
			active, err := s.store.ListPushSubscriptions(ctx, uid)
			if err != nil {
				return false
			}
			for _, candidate := range active {
				if candidate != nil && candidate.UID == uid && candidate.Endpoint == row.Endpoint && candidate.RegistrationID == row.RegistrationID && candidate.P256DH == row.P256DH && candidate.Auth == newAuth {
					return true
				}
			}
			return false
		}
		claimedRow := *row
		claimedRow.Auth = newAuth
		result, err := s.sendAPNs(ctx, uid, &claimedRow, input, data, s.config.BundleID+".push-type.liveactivity", "liveactivity", priority, meta.Expires, nativeCollapseID(row.Endpoint), allowed)
		if err != nil {
			failures = append(failures, err)
		}
		if result.Accepted == 0 && result.Expired == 0 {
			if _, err := cas.CompareAndSwapPushSubscriptionAuth(ctx, uid, row.Endpoint, row.RegistrationID, row.P256DH, newAuth, row.Auth); err != nil {
				failures = append(failures, errors.New("could not release activity claim"))
			}
		}
		if result.Accepted > 0 && isTerminalTaskStatus(current.State) {
			if err := s.surfaces.deleteVersion(ctx, &claimedRow); err != nil {
				failures = append(failures, errors.New("could not retire ended activity"))
			}
		}
	}
	return errors.Join(failures...)
}
