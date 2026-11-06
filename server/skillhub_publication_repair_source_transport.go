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

// Description GET is a CURRENT authority gate, unlike repair-task history.
// Recovery after a proposal finishes its lease must use this separate gate.
type publicationRepairDraftSourceReader interface {
	GetSkillHubPublicationDescription(context.Context, int64, string) (*store.SkillHubPublicationDescriptionDraft, error)
	GetSkillHubPublicationRepairTask(context.Context, int64, string) (*store.SkillHubPublicationRepairTask, error)
}

type publicationRepairSourceTransport struct {
	h        *Hub
	apps     publicationDescriptionCopyStore // current authority Get only
	tasks    store.SkillHubPublicationRepairExecutionReader
	drafts   publicationRepairDraftSourceReader
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
	t, tasks := h.db.(store.SkillHubPublicationRepairExecutionReader)
	e, entries := h.db.(publicationRepairSourceEntryReader)
	c, captures := h.db.(store.SkillHubPublicationCaptureReader)
	if !ok || !entries || !captures {
		return nil
	}
	var taskReader store.SkillHubPublicationRepairExecutionReader
	if tasks {
		taskReader = t
	}
	drafts, _ := h.db.(publicationRepairDraftSourceReader)
	return &publicationRepairSourceTransport{h: h, apps: a, tasks: taskReader, drafts: drafts, entries: e, captures: c,
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

func (h *Hub) verifyPublicationRepairTaskSource(ctx context.Context, actor int64, task store.SkillHubPublicationRepairTask, lease store.SkillHubPublicationRepairLease) error {
	if h == nil || h.publicationRepairSource == nil {
		return store.ErrSkillHubPublicationDenied
	}
	return h.publicationRepairSource.verifyTask(ctx, actor, task, lease)
}

func (h *Hub) publicationRepairTaskSourceVerifier(actor int64) func(context.Context, store.SkillHubPublicationRepairTask, store.SkillHubPublicationRepairLease) error {
	return func(ctx context.Context, task store.SkillHubPublicationRepairTask, lease store.SkillHubPublicationRepairLease) error {
		return h.verifyPublicationRepairTaskSource(ctx, actor, task, lease)
	}
}

// Supports the retained-description consumer both before proposal and during
// read-only recovery. A finished lease is NEVER promoted back to execution.
func (h *Hub) publicationRepairDescriptionSourceVerifier(actor int64) func(context.Context, store.SkillHubPublicationRepairTask) error {
	return func(ctx context.Context, task store.SkillHubPublicationRepairTask) error {
		if h == nil || h.publicationRepairSource == nil || len(task.Rounds) != 1 {
			return store.ErrSkillHubPublicationDenied
		}
		if task.Rounds[0].Result == nil {
			return h.publicationRepairSource.verifyTask(ctx, actor, task, task.Rounds[0].Lease)
		}
		return h.publicationRepairSource.verifyDraftTask(ctx, actor, task)
	}
}

func validPublicationRepairDraftSourceTask(task store.SkillHubPublicationRepairTask) bool {
	if len(task.Rounds) != 1 || task.State() != "waiting_confirmation" {
		return false
	}
	r := task.Rounds[0]
	if r.Result == nil || r.Result.Validate() != nil || r.Result.Evidence != task.Input.Evidence {
		return false
	}
	// Structural identity only. Expired finished leases are valid for read-only
	// draft recovery; only independent current-authority draft GET authorizes it.
	pending := snapshotPublicationRepairTask(task)
	pending.Rounds[0].Result = nil
	return r.Lease.Validate() == nil && validPublicationRepairModelTask(pending, r.Lease)
}

func (w *publicationRepairSourceTransport) verifyDraftTask(ctx context.Context, actor int64, input store.SkillHubPublicationRepairTask) error {
	task := snapshotPublicationRepairTask(input)
	if actor <= 0 || w.drafts == nil || !validPublicationRepairDraftSourceTask(task) {
		return store.ErrSkillHubPublicationInvalid
	}
	id := task.Input.Receipt.Query.EntryID
	var baseline *store.SkillHubPublicationDescriptionDraft
	authority := func(ctx context.Context) error {
		// Read CURRENT authority before history. History alone must never authorize
		// a source RPC after cancellation, transfer, task or membership changes.
		d, err := w.drafts.GetSkillHubPublicationDescription(ctx, actor, id)
		if err != nil {
			return err
		}
		if d == nil {
			return store.ErrSkillHubPublicationDenied
		}
		frozen := snapshotPublicationModelDescription(*d)
		current, err := w.drafts.GetSkillHubPublicationRepairTask(ctx, actor, id)
		if err != nil {
			return err
		}
		if current == nil || !reflect.DeepEqual(snapshotPublicationRepairTask(*current), task) || frozen.ValidateFor(task) != nil {
			return store.ErrSkillHubPublicationConflict
		}
		if baseline == nil {
			baseline = &frozen
		} else if !reflect.DeepEqual(*baseline, frozen) {
			return store.ErrSkillHubPublicationConflict
		}
		// Bracket history as well. Human confirmation arriving in-flight causes a
		// conservative conflict; a later read can recover that new exact state.
		latest, err := w.drafts.GetSkillHubPublicationDescription(ctx, actor, id)
		if err != nil {
			return err
		}
		if latest == nil || !reflect.DeepEqual(snapshotPublicationModelDescription(*latest), *baseline) {
			return store.ErrSkillHubPublicationConflict
		}
		return ctx.Err()
	}
	return w.verifyLiveSource(ctx, actor, id, task.Input.Receipt.Query, authority)
}

func validPublicationRepairSourceApplication(app store.SkillHubPublicationDescriptionApplication) bool {
	identity, err := app.Input.IdentityHash()
	if err != nil || app.IdentityHash != identity || app.ID != "pubdesc_"+identity || app.CreatedAt.IsZero() ||
		(app.Result == nil) != (app.AppliedAt == nil) {
		return false
	}
	return app.Result == nil || validatePublicationDescriptionCopyApplication(app) == nil
}

func validPublicationRepairSourceTask(task store.SkillHubPublicationRepairTask, lease store.SkillHubPublicationRepairLease) bool {
	hash, err := task.Input.IdentityHash()
	if err != nil || task.IdentityHash != hash || task.ID != lease.TaskID || !publicationRepairTaskID.MatchString(task.ID) ||
		task.CreatedAt.IsZero() || task.Input.Receipt.Query.EntryID != lease.EntryID || lease.Validate() != nil ||
		!lease.Until.After(time.Now()) || len(task.Rounds) != lease.Round || task.State() != "running" {
		return false
	}
	for i, round := range task.Rounds {
		if round.Lease.Validate() != nil || round.Lease.EntryID != lease.EntryID || round.Lease.TaskID != task.ID || round.Lease.Round != i+1 ||
			(i == 0 && round.Lease.Input.Evidence != task.Input.Evidence) {
			return false
		}
		if i == len(task.Rounds)-1 {
			if round.Result != nil || !samePublicationRepairModelLease(round.Lease, lease) {
				return false
			}
			continue
		}
		if round.Result == nil || round.Result.Validate() != nil || round.Result.Outcome != "rejected" ||
			round.Result.Evidence != task.Rounds[i+1].Lease.Input.Evidence {
			return false
		}
	}
	return true
}

func (w *publicationRepairSourceTransport) verify(parent context.Context, actor int64, input store.SkillHubPublicationDescriptionApplication) error {
	app := snapshotPublicationDescriptionApplication(input)
	if actor <= 0 || !validPublicationRepairSourceApplication(app) {
		return store.ErrSkillHubPublicationInvalid
	}
	id := app.Input.Receipt.Query.EntryID
	authority := func(ctx context.Context) error {
		current, err := w.apps.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
		if err != nil {
			return err
		}
		if current == nil || !reflect.DeepEqual(snapshotPublicationDescriptionApplication(*current), app) {
			return store.ErrSkillHubPublicationConflict
		}
		return nil
	}
	return w.verifyLiveSource(parent, actor, id, app.Input.Receipt.Query, authority)
}

func (w *publicationRepairSourceTransport) verifyTask(parent context.Context, actor int64, input store.SkillHubPublicationRepairTask, lease store.SkillHubPublicationRepairLease) error {
	task := snapshotPublicationRepairTask(input)
	if actor <= 0 || w.tasks == nil || !validPublicationRepairSourceTask(task, lease) {
		return store.ErrSkillHubPublicationInvalid
	}
	id := task.Input.Receipt.Query.EntryID
	authority := func(ctx context.Context) error {
		current, err := w.tasks.GetSkillHubPublicationRepairExecutionTask(ctx, actor, lease)
		if err != nil {
			return err
		}
		if current == nil || !validPublicationRepairSourceTask(*current, lease) ||
			!reflect.DeepEqual(snapshotPublicationRepairTask(*current), task) {
			return store.ErrSkillHubPublicationConflict
		}
		return nil
	}
	return w.verifyLiveSource(parent, actor, id, task.Input.Receipt.Query, authority)
}

func (w *publicationRepairSourceTransport) verifyLiveSource(parent context.Context, actor int64, id string, q store.SkillHubPublicationUploadQuery, authority func(context.Context) error) error {
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
	if w.active[id] {
		w.mu.Unlock()
		return store.ErrSkillHubPublicationBusy
	}
	w.active[id] = true
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.active, id); w.mu.Unlock() }()

	if err := authority(ctx); err != nil {
		return err
	}
	// Entry identifies the original human/Bot/source. It is not new repair
	// authority: the caller's application or live task/lease checkpoint does.
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
		if err := authority(ctx); err != nil {
			return err
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
	// Verify the unchanged ORIGINAL source against its original capture in every
	// repair round. A later repair candidate is independently bound by the task
	// lease and material reader; this proof does not authorize that copy.
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
