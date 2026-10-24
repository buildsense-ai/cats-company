package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const publicationRepairSourceRequestPrefix = "pubrepairsource_"

type publicationRepairSourcePending struct {
	runtime  *publicationCapturePending // immutable socket/route facts, not capture authority
	expected store.SkillHubPublicationCaptureInput
	replies  chan publicationCaptureReply // bounded scalar status only, never raw JSON
	queued   bool
}

type publicationRepairSourceEntryReader interface {
	GetSkillHubPublicationEntry(context.Context, int64, string) (*store.SkillHubPublicationEntry, error)
}

type publicationRepairSourceTransport struct {
	h        *Hub
	apps     publicationDescriptionCopyStore // current authority Get only
	entries  publicationRepairSourceEntryReader
	captures store.SkillHubPublicationCaptureReader
	slots    chan struct{}
	mu       sync.Mutex
	pending  map[string]*publicationRepairSourcePending
	active   map[string]bool
}

func newPublicationRepairSourceTransportFromEnv(h *Hub) *publicationRepairSourceTransport {
	enabled := func(name string) bool {
		v := strings.TrimSpace(os.Getenv(name))
		return v == "1" || strings.EqualFold(v, "true")
	}
	if !publicationWorkspaceEntriesEnabled() || !enabled("CATSCO_SKILLHUB_PUBLICATION_CAPTURE_ENABLED") ||
		!enabled("CATSCO_SKILLHUB_PUBLICATION_REPAIR_SOURCE_ENABLED") {
		return nil
	}
	return newPublicationRepairSourceTransport(h)
}

func newPublicationRepairSourceTransport(h *Hub) *publicationRepairSourceTransport {
	if h == nil || h.db == nil {
		return nil
	}
	a, ok := h.db.(publicationDescriptionCopyStore)
	e, entries := h.db.(publicationRepairSourceEntryReader)
	c, captures := h.db.(store.SkillHubPublicationCaptureReader)
	if !ok || !entries || !captures {
		return nil
	}
	return &publicationRepairSourceTransport{h: h, apps: a, entries: e, captures: c,
		slots: make(chan struct{}, 8), pending: make(map[string]*publicationRepairSourcePending), active: make(map[string]bool)}
}

// verifyPublicationDescriptionSource is an internal VerifySource port. Neither
// an HTTP/model tool nor an executor is registered here. The supplied application
// is compared with current authoritative persistence before any Runtime send.
func (h *Hub) verifyPublicationDescriptionSource(ctx context.Context, actor int64, app store.SkillHubPublicationDescriptionApplication) error {
	if h == nil || h.publicationRepairSource == nil {
		return store.ErrSkillHubPublicationDenied
	}
	return h.publicationRepairSource.verify(ctx, actor, app)
}

// Bind only the authenticated actor from the trusted service invocation. This
// function matches the existing application/copy VerifySource port directly.
func (h *Hub) publicationDescriptionSourceVerifier(actor int64) func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
	return func(ctx context.Context, app store.SkillHubPublicationDescriptionApplication) error {
		return h.verifyPublicationDescriptionSource(ctx, actor, app)
	}
}

func validPublicationRepairSourceApplication(app store.SkillHubPublicationDescriptionApplication) bool {
	identity, err := app.Input.IdentityHash()
	if err != nil || app.IdentityHash != identity || app.ID != "pubdesc_"+identity || app.CreatedAt.IsZero() ||
		(app.Result == nil) != (app.AppliedAt == nil) {
		return false
	}
	return app.Result == nil || validatePublicationDescriptionCopyApplication(app) == nil
}

func (w *publicationRepairSourceTransport) verify(parent context.Context, actor int64, input store.SkillHubPublicationDescriptionApplication) error {
	app := snapshotPublicationDescriptionApplication(input)
	if actor <= 0 || !validPublicationRepairSourceApplication(app) {
		return store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(parent, publicationCaptureTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case w.slots <- struct{}{}:
	default:
		return store.ErrSkillHubPublicationBusy
	}
	defer func() { <-w.slots }()
	w.mu.Lock()
	if w.active[app.ID] {
		w.mu.Unlock()
		return store.ErrSkillHubPublicationBusy
	}
	w.active[app.ID] = true
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.active, app.ID); w.mu.Unlock() }()

	id := app.Input.Receipt.Query.EntryID
	// Entry identifies the original human/Bot/source. It is not new repair
	// authority: application Get below also checks the confirmed private task.
	// No actor, path, selector or expected hash comes from a browser/model body.
	entry, err := w.entries.GetSkillHubPublicationEntry(ctx, actor, id)
	if err != nil {
		return err
	}
	if entry == nil || entry.ID != id || entry.Input.ActorUID != actor || entry.Input.Validate() != nil {
		return store.ErrSkillHubPublicationDenied
	}
	in := entry.Input
	checkpoint := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := w.apps.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
		if err != nil {
			return err
		}
		if current == nil || !reflect.DeepEqual(snapshotPublicationDescriptionApplication(*current), app) {
			return store.ErrSkillHubPublicationConflict
		}
		currentEntry, err := w.entries.GetSkillHubPublicationEntry(ctx, actor, id)
		if err != nil {
			return err
		}
		if currentEntry == nil || currentEntry.ID != id || currentEntry.Input != in {
			return store.ErrSkillHubPublicationConflict
		}
		return ctx.Err()
	}
	if err := checkpoint(); err != nil {
		return err
	}
	source, route, role, code := w.h.publicationRuntimeBinding(actor, in.BotUID, in.Source.DeviceID)
	source.LocalSkillID, source.WorkspaceGeneration = in.Source.LocalSkillID, in.Source.WorkspaceGeneration
	expectedSource := in.Source
	if code != "" || source.RouteGeneration < expectedSource.RouteGeneration {
		return store.ErrSkillHubPublicationStale
	}
	expectedSource.RouteGeneration = source.RouteGeneration
	if source != expectedSource {
		return store.ErrSkillHubPublicationStale
	}
	deadline, _ := ctx.Deadline()
	runtime := &publicationCapturePending{id: publicationRepairSourceRequestPrefix + rand.Text(), entryID: id,
		binding: store.SkillHubPublicationDeliveryBinding{ActorUID: actor, BotUID: in.BotUID, Source: source, NodeID: route.NodeID, ConnectionID: route.ConnectionID},
		route:   route, role: role, deadline: deadline}
	if _, err := w.h.recheckPublicationRuntime(runtime, DeviceCapabilitySkillHubPublicationRepairSource); err != nil {
		return err
	}
	captured, err := w.captures.ReadSkillHubPublicationCapture(ctx, id, runtime.binding)
	if err != nil {
		return err
	}
	if captured == nil {
		return store.ErrSkillHubPublicationDenied
	}
	capture := *captured
	q := app.Input.Receipt.Query
	// Verify the unchanged ORIGINAL source against its original capture in every
	// repair round. Draft.Evidence may identify a later independent repair copy;
	// it is bound by the current application Get, not a replacement capture.
	// This proof does not verify that copy's bytes or authorize continuation.
	old := capture.Input.Binding
	if capture.Input.Validate() != nil || capture.Input.EntryID != id || capture.CapturedAt.IsZero() ||
		capture.Input.RequestBytes > publicationCaptureRuntimeMaxBytes || capture.Input.Revision != q.Revision ||
		capture.Input.RequestHash != q.RequestHash || capture.Input.SourceSnapshotHash != q.SourceSnapshotHash || capture.Input.RequestBytes != q.RequestBytes ||
		old.Source.RouteGeneration > source.RouteGeneration ||
		(old.Source.RouteGeneration == source.RouteGeneration && (old.NodeID != route.NodeID || old.ConnectionID != route.ConnectionID)) {
		return store.ErrSkillHubPublicationConflict
	}
	old.Source.RouteGeneration, old.NodeID, old.ConnectionID = source.RouteGeneration, route.NodeID, route.ConnectionID
	if old != runtime.binding {
		return store.ErrSkillHubPublicationConflict
	}
	p := &publicationRepairSourcePending{runtime: runtime, expected: capture.Input, replies: make(chan publicationCaptureReply, 1)}
	msg := &ServerMessage{ThinToolRPC: &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: runtime.id,
		TargetOwnerUserID: formatUID(actor), TargetDeviceID: source.DeviceID, DeviceID: source.DeviceID,
		ToolName: string(DeviceCapabilitySkillHubPublicationRepairSource), CreatedAt: unixMillis(time.Now()), ExpiresAt: unixMillis(deadline),
		Payload: map[string]interface{}{"entry_id": id, "bot_uid": strconv.FormatInt(in.BotUID, 10), "local_skill_id": source.LocalSkillID, "workspace_generation": source.WorkspaceGeneration,
			"expected_capture": map[string]interface{}{"schemaVersion": 1, "entryId": id, "candidateRevision": capture.Input.Revision,
				"requestHash": capture.Input.RequestHash, "sourceSnapshotHash": capture.Input.SourceSnapshotHash, "requestBytes": capture.Input.RequestBytes}}}}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := checkpoint(); err != nil {
		return err
	}
	w.mu.Lock()
	w.pending[runtime.id] = p
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.pending, runtime.id); w.mu.Unlock() }()
	target, err := w.h.recheckPublicationRuntime(runtime, DeviceCapabilitySkillHubPublicationRepairSource)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Nonblocking: never disconnect the Bot or block model switching under load.
	if !target.trySend(data) {
		return store.ErrSkillHubPublicationBusy
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case reply := <-p.replies:
		finish := func(err error, code int) error { publicationDeliveryAck(reply.client, reply.id, code); return err }
		if err := ctx.Err(); err != nil {
			return finish(err, http.StatusConflict)
		}
		client, err := w.h.recheckPublicationRuntime(runtime, DeviceCapabilitySkillHubPublicationRepairSource)
		if err != nil || client != reply.client {
			return finish(store.ErrSkillHubPublicationStale, http.StatusConflict)
		}
		if err := checkpoint(); err != nil {
			return finish(err, http.StatusConflict)
		}
		latest, err := w.captures.ReadSkillHubPublicationCapture(ctx, id, runtime.binding)
		if err != nil {
			return finish(err, http.StatusConflict)
		}
		if latest == nil || *latest != capture {
			return finish(store.ErrSkillHubPublicationConflict, http.StatusConflict)
		}
		if err := checkpoint(); err != nil {
			return finish(err, http.StatusConflict)
		}
		client, err = w.h.recheckPublicationRuntime(runtime, DeviceCapabilitySkillHubPublicationRepairSource)
		if err != nil || client != reply.client {
			return finish(store.ErrSkillHubPublicationStale, http.StatusConflict)
		}
		if err := ctx.Err(); err != nil {
			return finish(err, http.StatusConflict)
		}
		if reply.err != nil {
			return finish(reply.err, http.StatusUnprocessableEntity)
		}
		return finish(nil, http.StatusOK)
	}
}

// WebSocket read loop: bounded parsing and queueing only. All store reads run in
// the waiting service goroutine, never in this callback or under the Hub lock.
func (w *publicationRepairSourceTransport) acceptResult(client *Client, msg *MsgThinToolRPC) {
	w.mu.Lock()
	p := w.pending[msg.RequestID]
	if p == nil || !time.Now().Before(p.runtime.deadline) {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusNotFound)
		return
	}
	b := p.runtime.binding
	w.h.mu.RLock()
	matched := client != nil && client.accountType == types.AccountBot && client.uid == b.BotUID && client.deviceConnector == nil && client.wikiAgentUID == 0 &&
		w.h.clientsByConn[b.ConnectionID] == client && client.connectionID == b.ConnectionID && w.h.nodeID == b.NodeID &&
		client.deviceOwnerUID == b.ActorUID && client.deviceID == b.Source.DeviceID && client.bodyID == b.Source.BodyID && client.installationID == b.Source.InstallationID
	w.h.mu.RUnlock()
	if !matched {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusForbidden)
		return
	}
	if p.queued {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusConflict)
		return
	}
	reply := publicationCaptureReply{client: client, id: msg.ID, err: store.ErrSkillHubPublicationInvalid}
	bound := msg.Type == thinToolRPCTypeResult && msg.ToolName == string(DeviceCapabilitySkillHubPublicationRepairSource) &&
		msg.DeviceID == b.Source.DeviceID && msg.TargetDeviceID == b.Source.DeviceID && msg.TargetOwnerUserID == formatUID(b.ActorUID) && len(msg.Payload) == 0
	if bound && msg.Result == nil && msg.Error != nil &&
		(msg.Error.Code == "publication.runtime_unavailable" || msg.Error.Code == "publication.runtime_timeout") {
		reply.err = store.ErrSkillHubPublicationBusy
	}
	r, ok := msg.Result.(map[string]interface{})
	e := p.expected
	if bound && msg.Error == nil && ok && len(r) == 7 && r["schemaVersion"] == float64(1) && r["candidateRevision"] == float64(e.Revision) &&
		r["entryId"] == e.EntryID && r["requestHash"] == e.RequestHash && r["sourceSnapshotHash"] == e.SourceSnapshotHash &&
		r["requestBytes"] == float64(e.RequestBytes) && r["sourceVerified"] == true {
		reply.err = nil
	}
	p.queued = true
	p.replies <- reply
	w.mu.Unlock()
}
