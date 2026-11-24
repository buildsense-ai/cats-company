package server

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const publicationWorkspaceCreate = "skillhub.publication.createEntry.v1"

// This short-lived probe is NOT a durable entry or upload credential. Its only
// consumer may create an inert #620 intent after exact-runtime confirmation.
type publicationWorkspaceProbe struct {
	input store.SkillHubPublicationEntryInput
	role  string
}

func publicationWorkspaceEntriesEnabled() bool {
	enabled := func(key string) bool {
		v := strings.TrimSpace(os.Getenv(key))
		return v == "1" || strings.EqualFold(v, "true")
	}
	return enabled("CATSCO_SKILLHUB_PUBLICATION_WORKSPACE_ENTRIES_ENABLED") &&
		enabled("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED")
}

func (h *Hub) publicationWorkspaceError(client *Client, id, code string) {
	// No raw runtime/DB errors, caller payloads or credentials in results/logs.
	h.SendToClient(client, &ServerMessage{ThinToolRPC: &MsgThinToolRPC{
		Type: thinToolRPCTypeResult, RequestID: id, ToolName: publicationWorkspaceCreate,
		Error: &MsgDeviceRPCError{Code: code, Message: "Publication entry could not be prepared; no upload was started."},
	}})
}

func (h *Hub) publicationWorkspaceReply(pending thinToolRPCPending, result any, code string) {
	if h == nil || pending.publication == nil || !h.isClientRegistered(pending.requester) ||
		!h.clientRoute(pending.requester).matches(pending.requesterRoute) {
		return // Never deliver private work to a different login/reconnection.
	}
	id := pending.publication.input.RequestID
	if code != "" {
		h.publicationWorkspaceError(pending.requester, id, code)
		return
	}
	h.SendToClient(pending.requester, &ServerMessage{ThinToolRPC: &MsgThinToolRPC{
		Type: thinToolRPCTypeResult, RequestID: id, ToolName: publicationWorkspaceCreate, Result: result,
	}})
}

// publicationWorkspaceBinding derives identity from the active human, current
// Bot configuration, registered device, authenticated Bot socket and durable
// connection generation. Browser source references are only selectors until
// the runtime has confirmed them. There is no body/device fallback or rebinding.
func (h *Hub) publicationWorkspaceBinding(client *Client, botUID int64, deviceID string) (store.SkillHubPublicationSource, runtimeRoute, string, string) {
	if h == nil || client == nil || client.uid <= 0 || client.accountType != types.AccountHuman ||
		client.deviceConnector != nil || client.wikiAgentUID != 0 || !h.isClientRegistered(client) {
		return store.SkillHubPublicationSource{}, runtimeRoute{}, "", "publication.owner_required"
	}
	return h.publicationRuntimeBinding(client.uid, botUID, deviceID)
}

// Background work derives actorUID from an immutable accepted entry, never a
// synthetic Client or runtime-supplied owner. It does not require an owner socket.
func (h *Hub) publicationRuntimeBinding(actorUID, botUID int64, deviceID string) (store.SkillHubPublicationSource, runtimeRoute, string, string) {
	deny := func(code string) (store.SkillHubPublicationSource, runtimeRoute, string, string) {
		return store.SkillHubPublicationSource{}, runtimeRoute{}, "", code
	}
	if h == nil || h.db == nil || h.userDevices == nil || h.bodyLeases == nil || actorUID <= 0 {
		return deny("publication.owner_required")
	}
	actor, err := h.db.GetUser(actorUID)
	if err != nil {
		return deny("publication.authority_unavailable")
	}
	if actor == nil || actor.State != 0 || actor.AccountType != types.AccountHuman {
		return deny("publication.owner_required")
	}
	bot, err := h.db.GetUser(botUID)
	if err != nil {
		return deny("publication.authority_unavailable")
	}
	if bot == nil || bot.State != 0 || bot.AccountType != types.AccountBot {
		return deny("publication.owner_required")
	}
	config, err := h.db.GetBotConfig(botUID)
	if err != nil {
		return deny("publication.authority_unavailable")
	}
	if config == nil || config.UserID != botUID || config.OwnerID != actorUID || !config.Enabled {
		return deny("publication.owner_required")
	}
	device, ok := h.userDevices.activeDevice(actorUID, deviceID)
	if !ok {
		return deny("publication.runtime_unavailable")
	}
	if device.OwnerUID != actorUID || device.BotUID != botUID || device.DeviceID != deviceID ||
		config.BodyID == "" || isLegacyBotBodyID(config.BodyID) || device.BodyID != config.BodyID ||
		device.InstallationID == "" || (device.RuntimeRole != "server" && device.RuntimeRole != "desktop") {
		return deny("publication.runtime_changed")
	}
	supported := false
	for _, cap := range device.Capabilities {
		if cap == DeviceCapabilitySkillHubPublicationProbe {
			supported = true
		}
	}
	if !supported {
		return deny("publication.runtime_unsupported")
	}
	route, target := h.findDeviceRPCTarget(actorUID, device)
	if !route.validAt(nowForRoute(h)) {
		return deny("publication.runtime_unavailable")
	}
	// Thin-tool pending state is node-local. Cross-node publication probes need
	// durable routing/receipts first, not best-effort forwarding that loses replies.
	if route.NodeID != h.nodeID {
		return deny("publication.route_unsupported")
	}
	if !route.validAt(nowForRoute(h)) || !h.routeConnected(route) || target == nil {
		return deny("publication.runtime_unavailable")
	}
	h.mu.RLock()
	exact := target.uid == botUID && target.accountType == types.AccountBot && target.deviceConnector == nil && target.wikiAgentUID == 0 &&
		target.bodyID == config.BodyID && target.installationID == device.InstallationID &&
		target.deviceOwnerUID == actorUID && target.deviceID == deviceID &&
		target.deviceBodyID == config.BodyID && target.deviceInstallationID == device.InstallationID
	localGeneration := h.botConnectionEpochs[botUID]
	h.mu.RUnlock()
	if !exact {
		return deny("publication.runtime_changed")
	}
	lease, ok := h.bodyLeases.status(botUID)
	if !ok || lease.bodyID != config.BodyID || lease.connectionID != route.ConnectionID ||
		lease.nodeID != route.NodeID || !nowForRoute(h).Before(lease.expiresAt) {
		return deny("publication.runtime_changed")
	}
	generations, ok := h.db.(store.ConversationTaskGenerationStore)
	if !ok {
		return deny("publication.storage_unsupported")
	}
	generation, err := generations.BotConnectionGeneration(botUID)
	if err != nil {
		return deny("publication.authority_unavailable")
	}
	if generation == 0 || generation > math.MaxInt64 || generation != localGeneration {
		return deny("publication.runtime_changed")
	}
	return store.SkillHubPublicationSource{
		DeviceID: device.DeviceID, BodyID: device.BodyID, InstallationID: device.InstallationID,
		RouteGeneration: int64(generation),
	}, route, device.RuntimeRole, ""
}

func (h *Hub) handlePublicationWorkspaceRequest(client *Client, msg *MsgThinToolRPC) {
	if !store.ValidSkillHubPublicationRequestID(msg.RequestID) {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusBadRequest, "invalid publication request ID", nil)
		return
	}
	fail := func(code string) { h.publicationWorkspaceError(client, msg.RequestID, code) }
	if !h.publicationWorkspaceEntries || msg.ToolName != publicationWorkspaceCreate {
		fail("publication.disabled")
		return
	}
	if _, ok := h.db.(store.SkillHubPublicationEntryStore); !ok {
		fail("publication.storage_unsupported")
		return
	}
	if _, ok := h.db.(store.SkillHubPublicationEntryRequestReader); !ok {
		fail("publication.storage_unsupported")
		return
	}
	if h.thinToolRPC == nil || h.publicationEntryLimits == nil {
		fail("publication.unavailable")
		return
	}
	if client == nil || client.uid <= 0 || client.accountType != types.AccountHuman || client.deviceConnector != nil || client.wikiAgentUID != 0 || !h.isClientRegistered(client) {
		fail("publication.owner_required")
		return
	}
	if !h.publicationEntryLimits.allow(HTTPRateLimitConfig{Name: "publication_workspace", Limit: 20, Window: time.Minute, Burst: 3}, strconv.FormatInt(client.uid, 10)) {
		fail("publication.rate_limited")
		return
	}
	// Exact body: no actor/owner/body/installation/route fields, absolute paths,
	// URLs, credentials or candidate bytes may be supplied by the caller.
	botText, botOK := msg.Payload["bot_uid"].(string)
	localID, localOK := msg.Payload["local_skill_id"].(string)
	workspace, workspaceOK := msg.Payload["workspace_generation"].(string)
	botUID, err := strconv.ParseInt(botText, 10, 64)
	if len(msg.Payload) != 3 || !botOK || !localOK || !workspaceOK || len(localID) == 0 || len(localID) > 128 || len(workspace) == 0 || len(workspace) > 128 || err != nil || botUID <= 0 || strconv.FormatInt(botUID, 10) != botText ||
		msg.TargetOwnerUserID != formatUID(client.uid) || msg.Result != nil || msg.Error != nil {
		fail("publication.invalid_request")
		return
	}
	source, route, role, code := h.publicationWorkspaceBinding(client, botUID, msg.TargetDeviceID)
	if code != "" {
		fail(code)
		return
	}
	source.LocalSkillID, source.WorkspaceGeneration = localID, workspace
	input := store.SkillHubPublicationEntryInput{RequestID: msg.RequestID, ActorUID: client.uid, BotUID: botUID, Entrypoint: "workspace", Source: source}
	if input.Validate() != nil {
		fail("publication.invalid_request")
		return
	}
	now := h.thinToolRPC.now()
	expires := now.Add(30 * time.Second)
	if msg.ExpiresAt > 0 && time.UnixMilli(msg.ExpiresAt).Before(expires) {
		expires = time.UnixMilli(msg.ExpiresAt)
	}
	if !now.Before(expires) {
		fail("publication.invalid_request")
		return
	}
	pending := thinToolRPCPending{
		requestID: "pubprobe_" + rand.Text(), requester: client, requesterRoute: h.clientRoute(client), targetRoute: route,
		targetOwnerUID: client.uid, targetDeviceID: source.DeviceID, toolName: string(DeviceCapabilitySkillHubPublicationProbe),
		createdAt: now, expiresAt: expires, publication: &publicationWorkspaceProbe{input: input, role: role},
	}
	if h.thinToolRPC.add(pending) != nil {
		fail("publication.busy")
		return
	}
	if code := h.recheckPublicationWorkspace(pending); code != "" {
		if _, ok := h.thinToolRPC.finishMatching(pending); ok {
			fail(code)
		}
		return
	}
	forward := &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: pending.requestID,
		TargetOwnerUserID: formatUID(input.ActorUID), TargetDeviceID: source.DeviceID, DeviceID: source.DeviceID,
		ToolName: pending.toolName, CreatedAt: unixMillis(now), ExpiresAt: unixMillis(expires),
		Payload: map[string]interface{}{"bot_uid": botText, "local_skill_id": localID, "workspace_generation": workspace},
	}
	if !h.sendThinToolRPCToRoute(route, forward) {
		if _, ok := h.thinToolRPC.finishMatching(pending); ok {
			fail("publication.runtime_unavailable")
		}
		return
	}
	h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "source verification pending", map[string]interface{}{"request_id": msg.RequestID})
}

func (h *Hub) recheckPublicationWorkspace(pending thinToolRPCPending) string {
	if !h.publicationWorkspaceEntries || pending.publication == nil {
		return "publication.disabled"
	}
	in := pending.publication.input
	if !h.thinToolRPC.now().Before(pending.expiresAt) {
		return "publication.runtime_timeout"
	}
	if pending.requester == nil || pending.requester.uid != in.ActorUID || !h.clientRoute(pending.requester).matches(pending.requesterRoute) {
		return "publication.owner_required"
	}
	source, route, role, code := h.publicationWorkspaceBinding(pending.requester, in.BotUID, in.Source.DeviceID)
	if code != "" {
		return code
	}
	source.LocalSkillID, source.WorkspaceGeneration = in.Source.LocalSkillID, in.Source.WorkspaceGeneration
	if source != in.Source || !route.matches(pending.targetRoute) || role != pending.publication.role {
		return "publication.runtime_changed"
	}
	return ""
}

func (h *Hub) handlePublicationWorkspaceResult(client *Client, msg *MsgThinToolRPC, pending thinToolRPCPending) {
	h.mu.RLock()
	matched := h.thinToolRPCResultMatchesTarget(pending, client)
	h.mu.RUnlock()
	if !matched {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusForbidden, "publication result source mismatch", nil)
		return
	}
	// Claim once before store work; concurrent duplicate results cannot create
	// multiple entries. Lost responses use the original account-scoped request ID.
	if _, ok := h.thinToolRPC.finishMatching(pending); !ok {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusNotFound, "request not pending", nil)
		return
	}
	defer h.sendThinToolRPCAck(client, msg.ID, http.StatusOK, "publication source receipt processed", nil)
	if code := h.recheckPublicationWorkspace(pending); code != "" {
		h.publicationWorkspaceReply(pending, nil, code)
		return
	}
	if msg.Error != nil {
		code := "publication.source_unavailable"
		runtimeCode := normalizePublicationProbeRequestError(msg.Error.Code)
		switch runtimeCode {
		case "publication.source_missing", "publication.source_changed", "publication.source_unsafe", "publication.runtime_unsupported", "publication.runtime_timeout":
			code = runtimeCode
		}
		h.publicationWorkspaceReply(pending, nil, code)
		return
	}
	result, ok := msg.Result.(map[string]interface{})
	in := pending.publication.input
	if !ok || len(result) != 3 || result["schema_version"] != float64(1) || result["local_skill_id"] != in.Source.LocalSkillID || result["workspace_generation"] != in.Source.WorkspaceGeneration {
		h.publicationWorkspaceReply(pending, nil, "publication.source_changed")
		return
	}
	entries, ok := h.db.(store.SkillHubPublicationEntryStore)
	if !ok {
		h.publicationWorkspaceReply(pending, nil, "publication.storage_unsupported")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entry, replay, err := entries.CreateSkillHubPublicationEntry(ctx, in, store.DefaultSkillHubPublicationEntryPolicy())
	if err != nil {
		code := "publication.admission_uncertain"
		switch {
		case errors.Is(err, store.ErrSkillHubPublicationDenied):
			code = "publication.owner_required"
		case errors.Is(err, store.ErrSkillHubPublicationConflict):
			code = "publication.request_conflict"
		case errors.Is(err, store.ErrSkillHubPublicationInvalid):
			code = "publication.invalid_request"
		case errors.Is(err, store.ErrSkillHubPublicationCapacity):
			code = "publication.capacity"
		case errors.Is(err, store.ErrSkillHubPublicationRateLimit):
			code = "publication.rate_limited"
		}
		h.publicationWorkspaceReply(pending, nil, code)
		return
	}
	if entry == nil || !skillHubPublicationEntryID.MatchString(entry.ID) || entry.Input != in || (entry.State != "waiting_runtime" && entry.State != "cancelled") {
		h.publicationWorkspaceReply(pending, nil, "publication.admission_uncertain")
		return
	}
	// Recheck authority before disclosing an accepted record. The transaction
	// may have committed before a disconnect/transfer; do not claim it rolled back.
	if code := h.recheckPublicationWorkspace(pending); code != "" {
		h.publicationWorkspaceReply(pending, nil, "publication.admission_uncertain")
		return
	}
	h.publicationWorkspaceReply(pending, map[string]interface{}{
		"schema_version": 1, "entry_request_id": entry.ID, "state": entry.State, "replay": replay,
	}, "")
}
