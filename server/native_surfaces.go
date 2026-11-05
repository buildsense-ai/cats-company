package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	activityPushPrefix = "activitykit://"
	surfacePushPrefix  = "surface://"
	activityTTL        = 8 * time.Hour
	surfaceTTL         = 30 * 24 * time.Hour
	maxSurfaceResponse = 64 * 1024
)

func isNativePushEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, nativePushPrefix) || strings.HasPrefix(endpoint, activityPushPrefix) || strings.HasPrefix(endpoint, surfacePushPrefix)
}

// Metadata fits the existing p256dh VARCHAR(256); tokens never enter metadata.
type activityRegistrationMetadata struct {
	Topic   string `json:"t"`
	Run     string `json:"r"`
	Source  int64  `json:"s"`
	Expires int64  `json:"e"`
	Details bool   `json:"d"`
}

type surfaceRegistrationMetadata struct {
	Scope   string `json:"s"`
	Expires int64  `json:"e"`
}

type activityRegistrationRequest struct {
	Token          string `json:"token"`
	Environment    string `json:"environment"`
	RegistrationID string `json:"registration_id"`
	TopicID        string `json:"topic_id"`
	RunID          string `json:"run_id"`
	Details        bool   `json:"details"`
}

type surfaceRegistrationRequest struct {
	AccessToken    string `json:"access_token"`
	RegistrationID string `json:"registration_id"`
	Scope          string `json:"scope"`
}

// Background surfaces reuse the same live account/topic checks as Web APIs.
type NativeSurfaceHandler struct {
	push          *NativePushService
	db            store.Store
	hub           *Hub
	agents        *AgentHandler
	conversations *ConversationHandler
}

func NewNativeSurfaceHandler(push *NativePushService, db store.Store, hub *Hub, agents *AgentHandler, conversations *ConversationHandler) *NativeSurfaceHandler {
	h := &NativeSurfaceHandler{push: push, db: db, hub: hub, agents: agents, conversations: conversations}
	push.surfaces = h
	return h
}

func (h *NativeSurfaceHandler) activeHuman(uid int64) bool {
	if h == nil || h.db == nil || uid <= 0 {
		return false
	}
	user, err := h.db.GetUser(uid)
	return err == nil && user != nil && user.State == 0 && user.AccountType == types.AccountHuman
}

func (h *NativeSurfaceHandler) reader() (store.NativeSurfaceReader, error) {
	provider, ok := h.db.(store.NativeSurfaceStore)
	if !ok {
		return nil, errors.New("surface context reader unavailable")
	}
	return provider.NativeSurfaceReader(), nil
}

func (h *NativeSurfaceHandler) canRead(ctx context.Context, uid int64, topic string) (bool, error) {
	if !validCanonicalTopicID(topic) || uid <= 0 {
		return false, nil
	}
	reader, err := h.reader()
	if err != nil {
		return false, err
	}
	user, err := reader.User(ctx, uid)
	if err != nil {
		return false, err
	}
	if user == nil || user.State != 0 || user.AccountType != types.AccountHuman {
		return false, nil
	}
	if isGroupTopic(topic) {
		managed, member, err := reader.GroupAccess(ctx, extractGroupID(topic), uid)
		return !managed && member, err
	}
	peerID := extractPeerUID(topic, uid)
	if peerID <= 0 {
		return false, nil
	}
	peer, err := reader.User(ctx, peerID)
	if err != nil || peer == nil {
		return false, err
	}
	return reader.PeerAccess(ctx, uid, peerID)
}

func (h *NativeSurfaceHandler) HandleActivities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		w.WriteHeader(401)
		return
	}
	reader, err := h.reader()
	if err != nil {
		w.WriteHeader(503)
		return
	}
	user, err := reader.User(r.Context(), uid)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if user == nil || user.State != 0 || user.AccountType != types.AccountHuman {
		w.WriteHeader(401)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.WriteHeader(405)
		return
	}
	var input activityRegistrationRequest
	if decodeStrictPushJSON(w, r, &input) != nil {
		w.WriteHeader(400)
		return
	}
	endpoint, err := h.push.endpoint(nativePushRegistration{Token: input.Token, Environment: input.Environment, RegistrationID: input.RegistrationID})
	if err != nil || !validCanonicalTopicID(input.TopicID) || input.RunID == "" || len(input.RunID) > maxTaskRunIDLength {
		w.WriteHeader(400)
		return
	}
	endpoint = activityPushPrefix + strings.TrimPrefix(endpoint, nativePushPrefix)
	if h.push.store == nil {
		w.WriteHeader(503)
		return
	}
	rows, err := h.push.store.ListPushSubscriptions(r.Context(), uid)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	var existing *types.PushSubscription
	var previous activityRegistrationMetadata
	for _, row := range rows {
		if row != nil && row.UID == uid && row.Endpoint == endpoint {
			existing = row
			_ = json.Unmarshal([]byte(row.P256DH), &previous)
		}
	}
	if r.Method == http.MethodDelete {
		// Match topic/run too: a late teardown must not delete a newer activity.
		if existing != nil && existing.RegistrationID == input.RegistrationID && previous.Topic == input.TopicID && previous.Run == input.RunID {
			if err := h.deleteVersion(r.Context(), existing); err != nil {
				w.WriteHeader(503)
				return
			}
		}
		writeJSON(w, 200, map[string]bool{"registered": false})
		return
	}
	if !h.push.Enabled() {
		w.WriteHeader(503)
		return
	}
	if _, ok := h.push.store.(store.PushSubscriptionVersionStore); !ok {
		w.WriteHeader(503)
		return
	}
	readable, err := h.canRead(r.Context(), uid, input.TopicID)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if !readable {
		w.WriteHeader(403)
		return
	}
	status, err := reader.CurrentTask(r.Context(), input.TopicID)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if status == nil || status.TopicID != input.TopicID || status.RunID != input.RunID || status.SourceUID <= 0 || !allowedTaskStatusStates[status.State] || isTerminalTaskStatus(status.State) || status.State == "idle" || (status.ExpiresAt != nil && !status.ExpiresAt.After(time.Now())) {
		w.WriteHeader(409)
		return
	}
	// The topic aggregate can be synthetic in multi-agent groups. Require an
	// exact current source run, rather than enrolling an aggregate-only run ID.
	source, err := reader.TaskRun(r.Context(), input.TopicID, status.SourceUID, input.RunID)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if source == nil || source.TopicID != input.TopicID || source.SourceUID != status.SourceUID || source.RunID != input.RunID || source.State != status.State {
		w.WriteHeader(409)
		return
	}
	if existing != nil && existing.RegistrationID == input.RegistrationID && (previous.Topic != input.TopicID || previous.Run != input.RunID || previous.Source != status.SourceUID) {
		w.WriteHeader(409)
		return
	}
	meta := activityRegistrationMetadata{Topic: input.TopicID, Run: input.RunID, Source: status.SourceUID, Expires: time.Now().Add(activityTTL).Unix(), Details: input.Details}
	auth := "activity-v1"
	if existing != nil && existing.RegistrationID == input.RegistrationID && previous.Topic == meta.Topic && previous.Run == meta.Run && previous.Source == meta.Source {
		// Refresh cannot extend the lifetime or reset a delivered event.
		meta.Expires = previous.Expires
		auth = existing.Auth
		if meta.Expires <= time.Now().Unix() || meta.Expires > time.Now().Add(activityTTL).Unix() {
			w.WriteHeader(409)
			return
		}
	}
	data, _ := json.Marshal(meta)
	if len(data) > 256 {
		w.WriteHeader(400)
		return
	}
	row := &types.PushSubscription{UID: uid, Endpoint: endpoint, P256DH: string(data), Auth: auth, RegistrationID: input.RegistrationID}
	stored, err := h.storeRegistration(r.Context(), row, existing)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if stored == nil {
		w.WriteHeader(409)
		return
	}
	writeJSON(w, 201, map[string]bool{"registered": true})
}

func validSurfaceHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func surfaceToken(uid int64, supplied string) (string, bool) {
	prefix := strconv.FormatInt(uid, 10) + "."
	secret := strings.TrimPrefix(supplied, prefix)
	if !validSurfaceHex(secret) {
		return "", false
	}
	return prefix + secret, true
}

func surfaceHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Conflicting creations may refresh the same activity without resetting its
// watermark. A losing widget creation may only observe the same secret; it
// cannot reinterpret its stale request as a new rotation.
func (h *NativeSurfaceHandler) storeRegistration(ctx context.Context, row, previous *types.PushSubscription) (*types.PushSubscription, error) {
	versioned, ok := h.push.store.(store.PushSubscriptionVersionStore)
	if !ok {
		return nil, errors.New("surface durable fencing unavailable")
	}
	if previous == nil {
		if err := h.purgeExpired(ctx, row.UID); err != nil {
			return nil, err
		}
	}
	// Once any version has been observed, absence is evidence that its
	// revocation won. Only a request that has never observed a row may create.
	observedExisting := previous != nil
	for attempt := 0; attempt < 3; attempt++ {
		if previous != nil && previous.RegistrationID == row.RegistrationID && strings.HasPrefix(row.Endpoint, activityPushPrefix) {
			var old, requested activityRegistrationMetadata
			if json.Unmarshal([]byte(previous.P256DH), &old) != nil || json.Unmarshal([]byte(row.P256DH), &requested) != nil || old.Topic != requested.Topic || old.Run != requested.Run || old.Source != requested.Source || old.Expires <= time.Now().Unix() {
				return nil, nil
			}
			requested.Expires = old.Expires
			data, _ := json.Marshal(requested)
			row.P256DH, row.Auth = string(data), previous.Auth
			if row.P256DH == previous.P256DH {
				return previous, nil
			}
		}
		var stored bool
		var err error
		if previous != nil && previous.RegistrationID == row.RegistrationID && strings.HasPrefix(row.Endpoint, activityPushPrefix) {
			stored, err = versioned.CompareAndSwapPushSubscriptionMetadata(ctx, row.UID, row.Endpoint, row.RegistrationID, previous.P256DH, row.P256DH, previous.Auth)
		} else {
			stored, err = versioned.ReplacePushSubscription(ctx, row, previous, maxPushSubscriptionsPerUser)
		}
		if err != nil {
			return nil, err
		}
		if stored {
			return row, nil
		}
		rows, err := h.push.store.ListPushSubscriptions(ctx, row.UID)
		if err != nil {
			return nil, err
		}
		previous = nil
		for _, candidate := range rows {
			if candidate != nil && candidate.UID == row.UID && candidate.Endpoint == row.Endpoint {
				previous = candidate
				break
			}
		}
		if previous == nil {
			if observedExisting {
				return nil, nil
			}
			continue
		}
		observedExisting = true
		if previous.RegistrationID != row.RegistrationID {
			return nil, nil
		}
		if strings.HasPrefix(row.Endpoint, surfacePushPrefix) {
			var old, requested surfaceRegistrationMetadata
			if previous.Auth != row.Auth || json.Unmarshal([]byte(previous.P256DH), &old) != nil || json.Unmarshal([]byte(row.P256DH), &requested) != nil || old.Scope != requested.Scope || old.Expires <= time.Now().Unix() {
				return nil, nil
			}
			return previous, nil
		}
	}
	return nil, nil
}

func (h *NativeSurfaceHandler) HandleSurfaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	uid := UIDFromContext(r.Context())
	if !h.activeHuman(uid) {
		w.WriteHeader(401)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.WriteHeader(405)
		return
	}
	var input surfaceRegistrationRequest
	if decodeStrictPushJSON(w, r, &input) != nil || !validNativeRegistrationID(input.RegistrationID) || !validSurfaceHex(input.Scope) {
		w.WriteHeader(400)
		return
	}
	token, valid := surfaceToken(uid, input.AccessToken)
	if !valid {
		w.WriteHeader(400)
		return
	}
	if h.push.store == nil {
		w.WriteHeader(503)
		return
	}
	endpoint := fmt.Sprintf("%s%d/%s", surfacePushPrefix, uid, input.RegistrationID)
	auth := "surface-v1:" + surfaceHash(token)
	rows, err := h.push.store.ListPushSubscriptions(r.Context(), uid)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	meta := surfaceRegistrationMetadata{Scope: input.Scope, Expires: time.Now().Add(surfaceTTL).Unix()}
	var matched *types.PushSubscription
	for _, row := range rows {
		if row == nil || row.UID != uid || row.Endpoint != endpoint {
			continue
		}
		matched = row
		var old surfaceRegistrationMetadata
		if json.Unmarshal([]byte(row.P256DH), &old) != nil || old.Scope != input.Scope {
			w.WriteHeader(403)
			return
		}
		if r.Method == http.MethodDelete && subtle.ConstantTimeCompare([]byte(auth), []byte(row.Auth)) != 1 {
			w.WriteHeader(403)
			return
		}
		if row.Auth == auth && old.Expires > time.Now().Unix() {
			meta.Expires = old.Expires
		}
	}
	if r.Method == http.MethodDelete {
		if err := h.deleteVersion(r.Context(), matched); err != nil {
			w.WriteHeader(503)
			return
		}
		writeJSON(w, 200, map[string]bool{"registered": false})
		return
	}
	if matched != nil {
		var previous surfaceRegistrationMetadata
		_ = json.Unmarshal([]byte(matched.P256DH), &previous)
		if previous.Expires > time.Now().Unix() {
			meta.Expires = previous.Expires
			if matched.Auth != auth {
				versioned, ok := h.push.store.(store.PushSubscriptionVersionStore)
				if !ok {
					w.WriteHeader(503)
					return
				}
				updated, err := versioned.CompareAndSwapPushSubscriptionAuth(r.Context(), uid, endpoint, input.RegistrationID, matched.P256DH, matched.Auth, auth)
				if err != nil {
					w.WriteHeader(503)
					return
				}
				if !updated {
					w.WriteHeader(409)
					return
				}
			}
			writeJSON(w, 201, map[string]interface{}{"registered": true, "access_token": token, "expires_at": time.Unix(meta.Expires, 0).UTC().Format(time.RFC3339)})
			return
		}
	}
	data, _ := json.Marshal(meta)
	stored, err := h.storeRegistration(r.Context(), &types.PushSubscription{UID: uid, Endpoint: endpoint, P256DH: string(data), Auth: auth, RegistrationID: input.RegistrationID}, matched)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if stored == nil {
		w.WriteHeader(409)
		return
	}
	_ = json.Unmarshal([]byte(stored.P256DH), &meta)
	writeJSON(w, 201, map[string]interface{}{"registered": true, "access_token": token, "expires_at": time.Unix(meta.Expires, 0).UTC().Format(time.RFC3339)})
}

func (h *NativeSurfaceHandler) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	header := r.Header.Get("Authorization")
	if len(header) > 128 || !strings.HasPrefix(header, "Surface ") {
		w.WriteHeader(401)
		return
	}
	token := strings.TrimPrefix(header, "Surface ")
	parts := strings.Split(token, ".")
	if len(parts) != 2 || !validSurfaceHex(parts[1]) {
		w.WriteHeader(401)
		return
	}
	uid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || parts[0] != strconv.FormatInt(uid, 10) || !h.activeHuman(uid) || h.push.store == nil {
		w.WriteHeader(401)
		return
	}
	rows, err := h.push.store.ListPushSubscriptions(r.Context(), uid)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	var scope string
	for _, row := range rows {
		if row == nil || row.UID != uid || row.Endpoint != fmt.Sprintf("%s%d/%s", surfacePushPrefix, uid, row.RegistrationID) || subtle.ConstantTimeCompare([]byte(row.Auth), []byte("surface-v1:"+surfaceHash(token))) != 1 {
			continue
		}
		var meta surfaceRegistrationMetadata
		if json.Unmarshal([]byte(row.P256DH), &meta) == nil && validSurfaceHex(meta.Scope) && meta.Expires > time.Now().Unix() && meta.Expires <= time.Now().Add(surfaceTTL).Unix() {
			scope = meta.Scope
			break
		}
	}
	if scope == "" {
		w.WriteHeader(401)
		return
	}
	agents, err := h.agents.visibleAgents(uid)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	conversations, err := h.conversations.listConversations(r.Context(), uid, true)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if len(agents) > 20 {
		agents = agents[:20]
	}
	// Existing public roster fields are bounded before encoding; no runtime
	// config, tool inputs, message previews or error/summary text is included.
	for i := range agents {
		agents[i].DisplayName = truncateUTF8(agents[i].DisplayName, 160)
		agents[i].Username = truncateUTF8(agents[i].Username, 160)
		agents[i].AvatarURL = ""
	}
	response := map[string]interface{}{"scope": scope, "generated_at": time.Now().UTC().Format(time.RFC3339), "agents": agents, "conversations": conversations}
	// Recheck the durable capability/account after projection work. A logout
	// or rotation while reading cannot authorize a fresh response.
	active, err := h.push.store.ListPushSubscriptions(r.Context(), uid)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	authorized := false
	for _, row := range active {
		if row == nil || row.UID != uid || !strings.HasPrefix(row.Endpoint, fmt.Sprintf("%s%d/", surfacePushPrefix, uid)) || subtle.ConstantTimeCompare([]byte(row.Auth), []byte("surface-v1:"+surfaceHash(token))) != 1 {
			continue
		}
		var meta surfaceRegistrationMetadata
		if json.Unmarshal([]byte(row.P256DH), &meta) == nil && meta.Scope == scope && meta.Expires > time.Now().Unix() {
			authorized = true
			break
		}
	}
	if !authorized || !h.activeHuman(uid) {
		w.WriteHeader(401)
		return
	}
	data, err := json.Marshal(response)
	if err != nil || len(data) > maxSurfaceResponse {
		w.WriteHeader(503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h *ConversationHandler) publicSurfaceConversations(uid int64, values []*types.ConversationSummary) []*types.ConversationSummary {
	out := make([]*types.ConversationSummary, 0, 20)
	for _, value := range values {
		if value == nil || !validCanonicalTopicID(value.ID) || h.hub == nil {
			continue
		}
		if code, _ := h.hub.validateTopicReadAccess(uid, types.AccountHuman, value.ID); code != 0 {
			continue
		}
		// Construct an allowlist so future Web fields cannot expand capability.
		item := &types.ConversationSummary{ID: value.ID, Name: truncateUTF8(value.Name, 160), IsGroup: value.IsGroup, GroupID: value.GroupID, FriendID: value.FriendID, IsBot: value.IsBot, HasBot: value.HasBot, IsAgentTask: value.IsAgentTask, AgentIDs: append([]int64(nil), value.AgentIDs...)}
		if len(item.AgentIDs) > 20 {
			item.AgentIDs = item.AgentIDs[:20]
		}
		if status := value.TaskStatus; status != nil {
			item.TaskStatus = &types.ConversationTaskStatus{TopicID: item.ID, RunID: truncateUTF8(status.RunID, maxTaskRunIDLength), State: status.State, SourceUID: status.SourceUID, UpdatedAt: status.UpdatedAt, ExpiresAt: status.ExpiresAt}
		}
		out = append(out, item)
		if len(out) == 20 {
			break
		}
	}
	return out
}

func (h *NativeSurfaceHandler) activityCurrent(ctx context.Context, uid int64, meta activityRegistrationMetadata) (*types.ConversationTaskStatus, error) {
	if meta.Expires <= time.Now().Unix() || meta.Expires > time.Now().Add(activityTTL).Unix() {
		return nil, nil
	}
	readable, err := h.canRead(ctx, uid, meta.Topic)
	if err != nil {
		return nil, err
	}
	if !readable || meta.Source <= 0 {
		return nil, nil
	}
	if isGroupTopic(meta.Topic) {
		reader, err := h.reader()
		if err != nil {
			return nil, err
		}
		managed, member, err := reader.GroupAccess(ctx, extractGroupID(meta.Topic), meta.Source)
		if err != nil {
			return nil, errors.New("activity source membership unavailable")
		}
		if managed || !member {
			return nil, nil
		}
	} else if extractPeerUID(meta.Topic, meta.Source) != uid {
		return nil, nil
	}
	reader, err := h.reader()
	if err != nil {
		return nil, err
	}
	status, err := reader.TaskRun(ctx, meta.Topic, meta.Source, meta.Run)
	if err != nil {
		return nil, errors.New("activity task store unavailable")
	}
	if status == nil || status.TopicID != meta.Topic || status.SourceUID != meta.Source || status.RunID != meta.Run || !allowedTaskStatusStates[status.State] || (status.ExpiresAt != nil && !isTerminalTaskStatus(status.State) && !status.ExpiresAt.After(time.Now())) {
		return nil, nil
	}
	return status, nil
}

func (h *NativeSurfaceHandler) deleteVersion(ctx context.Context, row *types.PushSubscription) error {
	if row == nil {
		return nil
	}
	if versioned, ok := h.push.store.(store.PushSubscriptionVersionStore); ok {
		return versioned.DeletePushSubscriptionVersion(ctx, row.UID, row.Endpoint, row.RegistrationID, row.P256DH, row.Auth)
	}
	return h.push.store.DeletePushSubscription(ctx, row.UID, row.Endpoint, row.RegistrationID)
}

// Expired background rows release their share of the unchanged account limit.
func (h *NativeSurfaceHandler) purgeExpired(ctx context.Context, uid int64) error {
	rows, err := h.push.store.ListPushSubscriptions(ctx, uid)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row == nil || row.UID != uid {
			continue
		}
		var expiry int64
		if strings.HasPrefix(row.Endpoint, activityPushPrefix) {
			var meta activityRegistrationMetadata
			if json.Unmarshal([]byte(row.P256DH), &meta) == nil {
				expiry = meta.Expires
			}
		} else if strings.HasPrefix(row.Endpoint, surfacePushPrefix) {
			var meta surfaceRegistrationMetadata
			if json.Unmarshal([]byte(row.P256DH), &meta) == nil {
				expiry = meta.Expires
			}
		} else {
			continue
		}
		if expiry <= time.Now().Unix() {
			if err := h.deleteVersion(ctx, row); err != nil {
				return err
			}
		}
	}
	return nil
}
