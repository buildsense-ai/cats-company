package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/redis/go-redis/v9"
)

var errDeviceSelectionConflict = errors.New("device selection changed; refresh and confirm again")

func (p deviceSelectionPreference) active(now int64) bool {
	return p.DeviceID != "" && (p.Durable || p.ExpiresAt > now)
}

func (r *userDeviceRegistry) withSelectionStore(db store.Store) *userDeviceRegistry {
	r.selectionStore, _ = db.(store.ConversationDeviceSelectionStore)
	return r
}

func durablePreference(record *store.ConversationDeviceSelection) deviceSelectionPreference {
	return deviceSelectionPreference{Durable: true, DeviceID: record.DeviceID, Revision: record.Revision,
		Source: record.Source, UpdatedAt: record.UpdatedAt}
}

// Records retain a revision after the active preference expires. Expiry must
// invalidate grants, not turn an old confirmation into a first-time selection.
func (r *userDeviceRegistry) selectionRecord(actor int64, session string) (deviceSelectionPreference, error) {
	if r.selectionStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		record, err := r.selectionStore.GetConversationDeviceSelection(ctx, actor, session)
		if err != nil {
			return deviceSelectionPreference{}, err
		} // Never fall back on DB failure.
		if record != nil {
			return durablePreference(record), nil
		}
		legacy, err := r.runtimeSelectionRecord(actor, session)
		if err != nil {
			return deviceSelectionPreference{}, err
		}
		if legacy.DeviceID == "" && legacy.Revision == "" {
			return deviceSelectionPreference{}, nil
		}
		// Preserve the old target even if its runtime lease expired. Rotate the
		// revision so pre-upgrade grants cannot become executable again.
		revision, err := randomHex(24)
		if err != nil {
			return deviceSelectionPreference{}, err
		}
		next := store.ConversationDeviceSelection{DeviceID: legacy.DeviceID, Revision: revision,
			Source: "legacy_selection", UpdatedAt: unixMillis(r.now())}
		won, err := r.selectionStore.CompareConversationDeviceSelection(ctx, actor, session, "", next)
		if err != nil {
			return deviceSelectionPreference{}, err
		}
		if won {
			return durablePreference(&next), nil
		}
		record, err = r.selectionStore.GetConversationDeviceSelection(ctx, actor, session)
		if err != nil {
			return deviceSelectionPreference{}, err
		}
		if record == nil {
			return deviceSelectionPreference{}, errDeviceSelectionConflict
		}
		return durablePreference(record), nil
	}
	return r.runtimeSelectionRecord(actor, session)
}

func (r *userDeviceRegistry) runtimeSelectionRecord(actor int64, session string) (deviceSelectionPreference, error) {
	if r.shared != nil {
		return r.shared.deviceSelectionRecord(actor, session)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.preferences[actor][session], nil
}

func (r *userDeviceRegistry) compareSelection(actor int64, session, expected string, next deviceSelectionPreference) error {
	if r.selectionStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		won, err := r.selectionStore.CompareConversationDeviceSelection(ctx, actor, session, expected,
			store.ConversationDeviceSelection{DeviceID: next.DeviceID, Revision: next.Revision, Source: next.Source, UpdatedAt: next.UpdatedAt})
		if err != nil {
			return err
		}
		if !won {
			return errDeviceSelectionConflict
		}
		return nil
	}
	if r.shared != nil {
		return r.shared.compareDeviceSelection(actor, session, expected, next)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.preferences[actor][session].Revision != expected {
		return errDeviceSelectionConflict
	}
	if r.preferences[actor] == nil {
		r.preferences[actor] = make(map[string]deviceSelectionPreference)
	}
	r.preferences[actor][session] = next
	return nil
}

// Establish a version without choosing a default or revoking grants. Both the
// first explicit-target message and picker GET use this before issuing/reading
// capabilities, so a late picker cannot invalidate a message already in flight.
func (r *userDeviceRegistry) ensureDeviceSelectionRevision(actor int64, session string) (deviceSelectionPreference, error) {
	current, err := r.selectionRecord(actor, session)
	if err != nil || current.Revision != "" {
		return current, err
	}
	revision, err := randomHex(24)
	if err != nil {
		return deviceSelectionPreference{}, err
	}
	next := current
	next.Revision = revision
	next.Durable = r.selectionStore != nil
	next.Source = "selection_snapshot"
	next.UpdatedAt = unixMillis(r.now())
	next.ExpiresAt = unixMillis(r.now().Add(r.preferenceTTL))
	if err := r.compareSelection(actor, session, "", next); err != nil {
		if !errors.Is(err, errDeviceSelectionConflict) {
			return deviceSelectionPreference{}, err
		}
		// Another first message, GET or explicit confirmation won the CAS. Use
		// its version, including its chosen device; never overwrite it.
		current, err = r.selectionRecord(actor, session)
		if err != nil {
			return deviceSelectionPreference{}, err
		}
		if current.Revision == "" {
			return deviceSelectionPreference{}, errDeviceSelectionConflict
		}
		return current, nil
	}
	return next, nil
}

func (r *userDeviceRegistry) confirmDeviceSelection(actor int64, session, device, source, expected string) (deviceSelectionPreference, error) {
	revision, err := randomHex(24)
	if err != nil {
		return deviceSelectionPreference{}, err
	}
	now := r.now()
	next := deviceSelectionPreference{Durable: r.selectionStore != nil, DeviceID: device, Revision: revision, Source: source,
		UpdatedAt: unixMillis(now), ExpiresAt: unixMillis(now.Add(r.preferenceTTL))}
	if err := r.compareSelection(actor, session, expected, next); err != nil {
		return deviceSelectionPreference{}, err
	}
	// A newly confirmed target creates a new authorization revision. Remove
	// stored grants for older revisions; lookups also fail closed on revision.
	r.revokeDeviceGrantsForSelection(actor, session)
	return next, nil
}

func (r *userDeviceRegistry) refreshDeviceSelection(actor int64, session string, current deviceSelectionPreference) {
	if current.Durable {
		return
	} // A routing choice is not a renewable execution grant.
	current.ExpiresAt = unixMillis(r.now().Add(r.preferenceTTL))
	_ = r.compareSelection(actor, session, current.Revision, current)
}

func (r *userDeviceRegistry) grantSelectionCurrent(grant ScopedDeviceGrant) bool {
	actor := parseFormattedUID(grant.ActorUserID)
	current, err := r.selectionRecord(actor, grant.SessionKey)
	if err != nil {
		return false
	}
	if current.Revision != grant.SelectionRevision {
		// A failed best-effort cleanup must not leave stale records forever.
		// Remove only this grant so a newer revision in the same session survives.
		r.revokeDeviceGrant(grant)
		return false
	}
	if grant.TargetScope == "explicit_device" {
		// A default's lease controls aliases, not explicit task targets. The
		// grant's own short expiry is checked by lookupGrant; a default switch
		// still invalidates every grant through the revision comparison above.
		return xiaobaRuntimeV2EnabledForBot(parseFormattedUID(grant.AgentID)) && grant.OwnerUserID == grant.ActorUserID
	}
	if current.DeviceID == "" {
		return grant.SelectionRevision == ""
	}
	return current.DeviceID == grant.DeviceID && current.active(unixMillis(r.now()))
}

func (s *sharedMemoryRuntimeState) deviceSelectionRecord(actor int64, session string) (deviceSelectionPreference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preferences[actor][session], nil
}

func (s *sharedMemoryRuntimeState) compareDeviceSelection(actor int64, session, expected string, next deviceSelectionPreference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.preferences[actor][session].Revision != expected {
		return errDeviceSelectionConflict
	}
	if s.preferences[actor] == nil {
		s.preferences[actor] = make(map[string]deviceSelectionPreference)
	}
	s.preferences[actor][session] = next
	return nil
}

func (s *RedisRuntimeState) deviceSelectionRecord(actor int64, session string) (deviceSelectionPreference, error) {
	raw, err := s.client.Get(s.ctx, s.deviceSelectionPreferenceKey(actor, session)).Result()
	if errors.Is(err, redis.Nil) {
		return deviceSelectionPreference{}, nil
	}
	if err != nil {
		return deviceSelectionPreference{}, err
	}
	var record deviceSelectionPreference
	err = json.Unmarshal([]byte(raw), &record)
	return record, err
}

func (s *RedisRuntimeState) compareDeviceSelection(actor int64, session, expected string, next deviceSelectionPreference) error {
	key := s.deviceSelectionPreferenceKey(actor, session)
	payload, err := json.Marshal(next)
	if err != nil {
		return err
	}
	// Retain the expired revision beyond the maximum grant lifetime. Redis is
	// shared by all server nodes; a local mutex is not sufficient for this CAS.
	ttl := time.Until(time.UnixMilli(next.ExpiresAt)) + defaultDeviceGrantTTL
	if ttl <= 0 {
		return errDeviceSelectionConflict
	}
	for attempt := 0; attempt < 4; attempt++ {
		err = s.client.Watch(s.ctx, func(tx *redis.Tx) error {
			raw, err := tx.Get(s.ctx, key).Result()
			if err != nil && !errors.Is(err, redis.Nil) {
				return err
			}
			var current deviceSelectionPreference
			if err == nil {
				if err := json.Unmarshal([]byte(raw), &current); err != nil {
					return err
				}
			}
			if current.Revision != expected {
				return errDeviceSelectionConflict
			}
			_, err = tx.TxPipelined(s.ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(s.ctx, key, payload, ttl)
				return nil
			})
			return err
		}, key)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
	}
	return errDeviceSelectionConflict
}
