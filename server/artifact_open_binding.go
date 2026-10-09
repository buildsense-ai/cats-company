package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	ArtifactOpenBindingContractV1 = "catsco.artifact-open-binding.v1"
	artifactOpenBindingTTL        = 30 * time.Minute
	artifactOpenBindingMaxEntries = 4096
	artifactBoundSubmitMaxBody    = 64 << 10
)

// ArtifactOpenBinding is a parent-only open-instance reference, not a login
// credential. Neither this value nor its auth-session fingerprint is sent to
// the gateway, app HTML, URLs, cookies or durable message metadata.
type ArtifactOpenBinding struct {
	ContractVersion string    `json:"contract_version"`
	OpenRef         string    `json:"open_ref"`
	TopicID         string    `json:"topic_id"`
	AgentUID        int64     `json:"agent_uid"`
	AppID           string    `json:"app_id"`
	AppOrigin       string    `json:"app_origin"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type artifactOpenBindingRecord struct {
	ArtifactOpenBinding
	actorUID               int64
	authSessionFingerprint [32]byte
	revoked                bool
}

// Bounded, process-local TTL storage intentionally fails closed after a restart
// or when another replica handles a reference. It never reconstructs routing
// from a viewer cookie, client topic or UID. Revoked records remain until TTL so
// only their owning auth session can perform an idempotent revoke.
type artifactOpenBindingStore struct {
	mu         sync.Mutex
	records    map[string]artifactOpenBindingRecord
	maxEntries int
}

func newArtifactOpenBindingStore() *artifactOpenBindingStore {
	return &artifactOpenBindingStore{records: make(map[string]artifactOpenBindingRecord), maxEntries: artifactOpenBindingMaxEntries}
}

func (s *artifactOpenBindingStore) issue(record artifactOpenBindingRecord, now time.Time) (ArtifactOpenBinding, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ArtifactOpenBinding{}, err
	}
	record.OpenRef = "aob_" + base64.RawURLEncoding.EncodeToString(random[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	for ref, existing := range s.records {
		if !now.Before(existing.ExpiresAt) {
			delete(s.records, ref)
		}
	}
	if len(s.records) >= s.maxEntries {
		return ArtifactOpenBinding{}, errors.New("open binding store full")
	}
	if _, exists := s.records[record.OpenRef]; exists {
		return ArtifactOpenBinding{}, errors.New("open binding reference collision")
	}
	s.records[record.OpenRef] = record
	return record.ArtifactOpenBinding, nil
}

// persistenceGate linearizes revoke against save: a revoke completed before
// this gate prevents persistence; a save already inside the gate finishes first.
// The global store lock serializes bound DB saves. Code inside the locked save
// must never call lookup/revoke/issue (including via a Store callback). Permission
// and external registry lookups belong before this lock. Read time after lock
// acquisition so a queued save cannot pass TTL with an outdated timestamp.
func (s *artifactOpenBindingStore) persistenceGate(ref string, uid int64, fingerprint [32]byte, now func() time.Time) (func(), bool) {
	s.mu.Lock()
	record, ok := s.records[ref]
	if !ok || record.actorUID != uid || record.authSessionFingerprint != fingerprint || record.revoked || !now().Before(record.ExpiresAt) {
		s.mu.Unlock()
		return nil, false
	}
	return s.mu.Unlock, true
}

func (s *artifactOpenBindingStore) lookup(ref string, uid int64, fingerprint [32]byte, now time.Time, revoke bool) (artifactOpenBindingRecord, bool) {
	if s == nil {
		return artifactOpenBindingRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[ref]
	if !ok || record.actorUID != uid || record.authSessionFingerprint != fingerprint {
		return artifactOpenBindingRecord{}, false
	}
	if !now.Before(record.ExpiresAt) {
		delete(s.records, ref)
		return artifactOpenBindingRecord{}, false
	}
	if revoke {
		record.revoked = true
		s.records[ref] = record
		return record, true
	}
	return record, !record.revoked
}

// ArtifactOpenBindingHandler wraps normal message ingestion with a fixed,
// authenticated launch destination. No provider or internal Agent session API
// is involved. The ordinary v1 message endpoint remains compatible.
type ArtifactOpenBindingHandler struct {
	hub      *Hub
	messages *MessageHandler
	apps     *ArtifactAppsHandler
	store    *artifactOpenBindingStore
	origins  []string
	now      func() time.Time
	uploads  *UploadHandler
}

func NewArtifactOpenBindingHandler(hub *Hub, messages *MessageHandler, apps *ArtifactAppsHandler) *ArtifactOpenBindingHandler {
	return &ArtifactOpenBindingHandler{hub: hub, messages: messages, apps: apps, store: newArtifactOpenBindingStore(), now: time.Now}
}

func (h *ArtifactLaunchHandler) SetOpenBindingHandler(bindings *ArtifactOpenBindingHandler) {
	h.openBindings = bindings
	if bindings != nil {
		bindings.origins = append([]string(nil), h.launchOrigins...)
	}
}

func (h *ArtifactOpenBindingHandler) session(r *http.Request) (*JWTClaims, [32]byte, int, string) {
	var empty [32]byte
	if h == nil || h.hub == nil || h.hub.db == nil {
		return nil, empty, http.StatusServiceUnavailable, "artifact_open_binding_unavailable"
	}
	claims, status, msg := ownerClaimsFromRequest(r, h.hub.db.GetUser)
	if status != 0 {
		return nil, empty, status, msg
	}
	if claims.UID <= 0 || claims.UID != UIDFromContext(r.Context()) {
		return nil, empty, http.StatusUnauthorized, "unauthorized"
	}
	// Bind the actual verified bearer token, not mutable username/UID claims.
	return claims, sha256.Sum256([]byte(extractToken(r))), 0, ""
}

func canonicalArtifactOpenTopic(topic string, actorUID int64) (string, bool) {
	if isGroupTopic(topic) {
		id, err := strconv.ParseInt(strings.TrimPrefix(topic, "grp_"), 10, 64)
		canonical := "grp_" + strconv.FormatInt(id, 10)
		return canonical, err == nil && id > 0 && topic == canonical
	}
	parts := strings.Split(topic, "_")
	if len(parts) != 3 || parts[0] != "p2p" {
		return "", false
	}
	a, errA := strconv.ParseInt(parts[1], 10, 64)
	b, errB := strconv.ParseInt(parts[2], 10, 64)
	if errA != nil || errB != nil || a <= 0 || b <= 0 || a == b || (a != actorUID && b != actorUID) {
		return "", false
	}
	canonical := p2pTopicID(a, b)
	return canonical, topic == canonical
}

func (h *ArtifactOpenBindingHandler) topicAgent(actorUID int64, topic string) (int64, int, string) {
	if _, ok := canonicalArtifactOpenTopic(topic, actorUID); !ok {
		return 0, http.StatusBadRequest, "artifact_topic_invalid"
	}
	if status, msg := h.hub.validateMessagePublish(actorUID, types.AccountHuman, topic, false); status != 0 {
		return 0, status, msg
	}
	agentUID, ok := h.hub.artifactAgentForTopic(actorUID, topic)
	if !ok {
		return 0, http.StatusForbidden, "artifact_topic_agent_unavailable"
	}
	agent, err := h.hub.db.GetUser(agentUID)
	if err != nil || agent == nil || agent.State != 0 || agent.AccountType != types.AccountBot {
		return 0, http.StatusForbidden, "artifact_topic_agent_unavailable"
	}
	// Group configuration alone is not proof of current Agent membership.
	if isGroupTopic(topic) {
		groupID := extractGroupID(topic)
		member, err := h.hub.db.IsGroupMember(groupID, agentUID)
		if err != nil || !member {
			return 0, http.StatusForbidden, "artifact_topic_agent_unavailable"
		}
		// The legacy resolver has a Group.AgentIDs shortcut. Do not let a
		// stale single-Agent config conceal another current bot member.
		members, err := h.hub.db.GetGroupMembers(groupID)
		if err != nil {
			return 0, http.StatusForbidden, "artifact_topic_agent_unavailable"
		}
		foundAgent := false
		for _, candidate := range members {
			if candidate == nil {
				continue
			}
			user, err := h.hub.db.GetUser(candidate.UserID)
			if err != nil || user == nil {
				return 0, http.StatusForbidden, "artifact_topic_agent_unavailable"
			}
			if user.AccountType == types.AccountBot {
				if user.ID != agentUID {
					return 0, http.StatusForbidden, "artifact_topic_agent_unavailable"
				}
				foundAgent = true
			}
		}
		if !foundAgent {
			return 0, http.StatusForbidden, "artifact_topic_agent_unavailable"
		}
	}
	return agentUID, 0, ""
}

func (h *ArtifactOpenBindingHandler) authorizedApp(r *http.Request, appID string, agentUID int64) (artifactApp, int, string) {
	if h.apps == nil || !h.apps.Enabled() {
		return artifactApp{}, http.StatusServiceUnavailable, "artifact_gateway_unavailable"
	}
	app, found, err := h.apps.gatewayApp(r.Context(), appID)
	if err != nil {
		return artifactApp{}, http.StatusBadGateway, "artifact_gateway_unavailable"
	}
	if !found || app.Agent != strconv.FormatInt(agentUID, 10) {
		return artifactApp{}, http.StatusForbidden, "artifact_app_not_authorized"
	}
	// All advertised URLs must be exact configured gateway app roots. A prefix
	// match, userinfo, query or foreign alternate URL cannot establish identity.
	urls := append([]string{app.URL}, app.URLs...)
	for _, raw := range urls {
		if _, ok := gatewayAppOrigin(raw, appID, h.origins); !ok {
			return artifactApp{}, http.StatusBadGateway, "artifact_gateway_app_invalid"
		}
	}
	return app, 0, ""
}

func gatewayAppOrigin(raw, appID string, origins []string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.Path != "/"+appID+"/" {
		return "", false
	}
	origin := u.Scheme + "://" + u.Host
	for _, allowed := range origins {
		if origin == allowed {
			return origin, true
		}
	}
	return "", false
}

func (h *ArtifactOpenBindingHandler) prepareLaunch(r *http.Request, appID, topic string) (artifactOpenBindingRecord, artifactApp, int, string) {
	claims, fingerprint, status, msg := h.session(r)
	if status != 0 {
		return artifactOpenBindingRecord{}, artifactApp{}, status, msg
	}
	agentUID, status, msg := h.topicAgent(claims.UID, topic)
	if status != 0 {
		return artifactOpenBindingRecord{}, artifactApp{}, status, msg
	}
	app, status, msg := h.authorizedApp(r, appID, agentUID)
	if status != 0 {
		return artifactOpenBindingRecord{}, artifactApp{}, status, msg
	}
	expires := h.now().UTC().Add(artifactOpenBindingTTL)
	if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(expires) {
		expires = claims.ExpiresAt.Time.UTC()
	}
	return artifactOpenBindingRecord{ArtifactOpenBinding: ArtifactOpenBinding{
		ContractVersion: ArtifactOpenBindingContractV1, TopicID: topic, AgentUID: agentUID, AppID: appID, ExpiresAt: expires,
	}, actorUID: claims.UID, authSessionFingerprint: fingerprint}, app, 0, ""
}

func (h *ArtifactOpenBindingHandler) finishLaunch(record artifactOpenBindingRecord, app artifactApp, result ArtifactLaunchResult) (ArtifactOpenBinding, error) {
	launch, err := url.Parse(result.LaunchURL)
	if err != nil || launch.User != nil || launch.Fragment != "" || launch.RawPath != "" || launch.Path != "/_launch/"+result.Code || result.AppID != record.AppID {
		return ArtifactOpenBinding{}, errors.New("invalid gateway launch")
	}
	query, err := url.ParseQuery(launch.RawQuery)
	if err != nil || len(query) != 1 || len(query["next"]) != 1 || query.Get("next") != "/"+record.AppID+"/" {
		return ArtifactOpenBinding{}, errors.New("invalid gateway destination")
	}
	origin := launch.Scheme + "://" + launch.Host
	for _, raw := range append([]string{app.URL}, app.URLs...) {
		if registeredOrigin, ok := gatewayAppOrigin(raw, record.AppID, h.origins); ok && registeredOrigin == origin {
			record.AppOrigin = origin
			if !h.now().Before(record.ExpiresAt) {
				return ArtifactOpenBinding{}, errors.New("expired auth session")
			}
			return h.store.issue(record, h.now())
		}
	}
	return ArtifactOpenBinding{}, errors.New("unregistered gateway origin")
}

type artifactBoundSubmitRequest struct {
	OpenRef            string               `json:"open_ref"`
	ClientMsgID        string               `json:"client_msg_id"`
	Content            string               `json:"content"`
	ReplyTo            int                  `json:"reply_to,omitempty"`
	ContentBlocks      []types.ContentBlock `json:"content_blocks,omitempty"`
	GatewayAnnotations json.RawMessage      `json:"gateway_annotations"`
}

func openBindingFailure(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": msg})
}

// HandleAnnotations handles POST /api/artifacts/annotations. The topic and
// recipient are taken exclusively from the authenticated reference.
func (h *ArtifactOpenBindingHandler) HandleAnnotations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		openBindingFailure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	claims, fingerprint, status, msg := h.session(r)
	if status != 0 {
		openBindingFailure(w, status, msg)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, artifactBoundSubmitMaxBody+1))
	if err != nil {
		openBindingFailure(w, http.StatusBadRequest, "artifact_request_invalid")
		return
	}
	if len(body) > artifactBoundSubmitMaxBody {
		openBindingFailure(w, http.StatusRequestEntityTooLarge, "artifact_request_too_large")
		return
	}
	var req artifactBoundSubmitRequest
	if err := json.Unmarshal(body, &req); err != nil {
		openBindingFailure(w, http.StatusBadRequest, "artifact_request_invalid")
		return
	}
	record, ok := h.store.lookup(req.OpenRef, claims.UID, fingerprint, h.now(), false)
	if !ok {
		openBindingFailure(w, http.StatusForbidden, "artifact_open_binding_invalid")
		return
	}
	if strings.TrimSpace(req.ClientMsgID) == "" || len(req.ClientMsgID) > 128 || containsControlCharacter(req.ClientMsgID) || strings.TrimSpace(req.Content) == "" || req.ReplyTo < 0 {
		openBindingFailure(w, http.StatusBadRequest, "artifact_request_invalid")
		return
	}
	agentUID, status, msg := h.topicAgent(claims.UID, record.TopicID)
	if status != 0 {
		openBindingFailure(w, status, msg)
		return
	}
	if agentUID != record.AgentUID {
		openBindingFailure(w, http.StatusForbidden, "artifact_open_binding_invalid")
		return
	}
	app, status, msg := h.authorizedApp(r, record.AppID, record.AgentUID)
	if status != 0 {
		openBindingFailure(w, status, msg)
		return
	}
	originStillRegistered := false
	for _, raw := range append([]string{app.URL}, app.URLs...) {
		origin, ok := gatewayAppOrigin(raw, record.AppID, h.origins)
		originStillRegistered = originStillRegistered || (ok && origin == record.AppOrigin)
	}
	if !originStillRegistered {
		openBindingFailure(w, http.StatusForbidden, "artifact_open_binding_invalid")
		return
	}
	document, err := normalizeGatewayAnnotations(req.GatewayAnnotations)
	if err != nil || document == nil {
		openBindingFailure(w, http.StatusBadRequest, "artifact_annotations_invalid")
		return
	}
	if document.AgentUID != record.AgentUID || document.AppID != record.AppID {
		openBindingFailure(w, http.StatusForbidden, "artifact_open_binding_mismatch")
		return
	}
	value, err := document.restamped()
	if err != nil {
		openBindingFailure(w, http.StatusBadRequest, "artifact_annotations_invalid")
		return
	}
	_, metadataStore := h.hub.db.(store.MessageMetadataStore)
	if h.messages == nil || h.messages.hub != h.hub || h.messages.db == nil || !metadataStore {
		openBindingFailure(w, http.StatusServiceUnavailable, "artifact_open_binding_unavailable")
		return
	}
	// Recheck lifecycle after the external registry call. A completed revoke
	// must prevent a request still waiting for permission from persisting.
	if _, ok := h.store.lookup(req.OpenRef, claims.UID, fingerprint, h.now(), false); !ok {
		openBindingFailure(w, http.StatusForbidden, "artifact_open_binding_invalid")
		return
	}
	blocks, err := h.validatedScreenshotBlocks(claims.UID, req.ContentBlocks)
	if err != nil {
		openBindingFailure(w, http.StatusBadRequest, "artifact_screenshots_invalid")
		return
	}
	content, _ := json.Marshal(req.Content)
	ordinary := SendMessageRequest{TopicID: record.TopicID, ClientMsgID: req.ClientMsgID, Content: content, ReplyTo: req.ReplyTo, ContentBlocks: blocks, Type: "text", MsgType: "text", Metadata: map[string]interface{}{gatewayAnnotationsMetadataKey: value}}
	// Groups use the normal structured-mention route, targeting only the
	// unique, bound Agent rather than invoking every group bot.
	if isGroupTopic(record.TopicID) {
		ordinary.Mentions = []string{formatUID(record.AgentUID)}
		// No frame/client content block may address another bot or "all".
		// The ordinary normalizer materializes our single trusted mention.
		for index := range ordinary.ContentBlocks {
			block := &ordinary.ContentBlocks[index]
			payload := make(map[string]interface{}, len(block.Payload))
			for key, value := range block.Payload {
				if key != "mentions" {
					payload[key] = value
				}
			}
			block.Payload = payload
		}
	}
	h.messages.sendMessage(w, r, ordinary, func() (func(), bool) {
		// Ordinary ingestion performs another external ownership lookup. Check
		// current auth/account and original topic again after that wait, before
		// taking the store lock; never make these lookups inside locked save.
		currentClaims, currentFingerprint, status, _ := h.session(r)
		if status != 0 || r.Context().Err() != nil {
			return nil, false
		}
		currentAgent, status, _ := h.topicAgent(currentClaims.UID, record.TopicID)
		if status != 0 || currentAgent != record.AgentUID {
			return nil, false
		}
		// A request can queue behind another bound DB save. Recheck auth expiry
		// after acquiring the lock too; no external/auth DB lookup under it.
		return h.store.persistenceGate(req.OpenRef, currentClaims.UID, currentFingerprint, func() time.Time {
			now := h.now()
			if currentClaims.ExpiresAt != nil && !time.Now().Before(currentClaims.ExpiresAt.Time) {
				return record.ExpiresAt
			}
			return now
		})
	})
}

// HandleRevoke handles DELETE /api/artifacts/open-bindings/{open_ref}.
// Repeated revoke is idempotent while the TTL tombstone exists. Unknown,
// expired, foreign actor or foreign token references all fail closed alike.
func (h *ArtifactOpenBindingHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		openBindingFailure(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	claims, fingerprint, status, msg := h.session(r)
	if status != 0 {
		openBindingFailure(w, status, msg)
		return
	}
	ref := strings.TrimPrefix(r.URL.Path, "/api/artifacts/open-bindings/")
	if _, ok := h.store.lookup(ref, claims.UID, fingerprint, h.now(), true); !ok {
		openBindingFailure(w, http.StatusForbidden, "artifact_open_binding_invalid")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
