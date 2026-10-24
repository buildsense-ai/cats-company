package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const publicationCaptureRequestPrefix = "pubcapture_"
const publicationCaptureTimeout = 25 * time.Second
const publicationCaptureRuntimeMaxBytes = 32 * 1024 * 1024

var publicationCaptureHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

type publicationCaptureReply struct {
	client *Client
	id     string
	input  store.SkillHubPublicationCaptureInput
	err    error
}

type publicationCapturePending struct {
	id       string
	entryID  string
	binding  store.SkillHubPublicationDeliveryBinding
	route    runtimeRoute
	role     string
	deadline time.Time
	replies  chan publicationCaptureReply
	queued   bool // mu; a reply cannot be replaced while its DB write is pending
}

type publicationCaptureTransport struct {
	h        *Hub
	entries  store.SkillHubPublicationEntryStore
	statuses store.SkillHubPublicationSourceStatusReader
	captures store.SkillHubPublicationCaptureStore
	slots    chan struct{}
	mu       sync.Mutex
	pending  map[string]*publicationCapturePending
	active   map[string]bool
}

func newPublicationCaptureTransportFromEnv(h *Hub) *publicationCaptureTransport {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CAPTURE_ENABLED"))
	if !publicationWorkspaceEntriesEnabled() || (v != "1" && !strings.EqualFold(v, "true")) {
		return nil
	}
	return newPublicationCaptureTransport(h)
}

func newPublicationCaptureTransport(h *Hub) *publicationCaptureTransport {
	if h == nil || h.db == nil {
		return nil
	}
	e, ok := h.db.(store.SkillHubPublicationEntryStore)
	if !ok {
		return nil
	}
	s, ok := h.db.(store.SkillHubPublicationSourceStatusReader)
	if !ok {
		return nil
	}
	c, ok := h.db.(store.SkillHubPublicationCaptureStore)
	if !ok {
		return nil
	}
	return &publicationCaptureTransport{h: h, entries: e, statuses: s, captures: c,
		slots: make(chan struct{}, 8), pending: make(map[string]*publicationCapturePending), active: make(map[string]bool)}
}

// CaptureSkillHubPublication is an internal service operation, not an HTTP,
// browser/model tool. The internal durable scheduler calls it with an entry and actor;
// identity/source facts come from durable admission and the authenticated local
// runtime. A caller's cancellation/lifecycle bounds the complete operation.
// It never issues grants, uploads bytes, confirms intent or marks publication done.
func (h *Hub) CaptureSkillHubPublication(ctx context.Context, actor int64, entryID string) (*store.SkillHubPublicationCaptureRecord, error) {
	if h == nil || h.publicationCapture == nil {
		return nil, store.ErrSkillHubPublicationDenied
	}
	return h.publicationCapture.capture(ctx, actor, entryID)
}

func (w *publicationCaptureTransport) capture(parent context.Context, actor int64, entryID string) (*store.SkillHubPublicationCaptureRecord, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(entryID) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(parent, publicationCaptureTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case w.slots <- struct{}{}:
	default:
		return nil, store.ErrSkillHubPublicationBusy
	}
	defer func() { <-w.slots }()
	w.mu.Lock()
	if w.active[entryID] {
		w.mu.Unlock()
		return nil, store.ErrSkillHubPublicationBusy
	}
	w.active[entryID] = true
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.active, entryID); w.mu.Unlock() }()
	entry, err := w.entries.GetSkillHubPublicationEntry(ctx, actor, entryID)
	if err != nil {
		return nil, err
	}
	if entry == nil || entry.ID != entryID || entry.Input.ActorUID != actor || entry.State != "waiting_runtime" || entry.Input.Validate() != nil {
		return nil, store.ErrSkillHubPublicationDenied
	}
	// Copy before any waits; never retain a mutable store observation.
	// Source selectors are opaque admission references, not capture digests.
	// Validate above enforces their shared store/schema contract.
	in := entry.Input
	status, err := w.statuses.GetSkillHubPublicationSourceStatus(ctx, actor, entryID)
	if err != nil {
		return nil, err
	}
	if status == nil || status.ActorUID != actor || status.EntryID != entryID || status.State != "source_verified" || status.SourceVerifiedAt == nil {
		return nil, store.ErrSkillHubPublicationDenied
	}
	source, route, role, code := w.h.publicationRuntimeBinding(actor, in.BotUID, in.Source.DeviceID)
	if code != "" {
		return nil, store.ErrSkillHubPublicationStale
	}
	source.LocalSkillID, source.WorkspaceGeneration = in.Source.LocalSkillID, in.Source.WorkspaceGeneration
	expected := in.Source
	if source.RouteGeneration < expected.RouteGeneration {
		return nil, store.ErrSkillHubPublicationStale
	}
	expected.RouteGeneration = source.RouteGeneration
	if source != expected {
		return nil, store.ErrSkillHubPublicationStale
	}
	deadline, _ := ctx.Deadline()
	p := &publicationCapturePending{id: publicationCaptureRequestPrefix + rand.Text(), entryID: entryID,
		binding: store.SkillHubPublicationDeliveryBinding{ActorUID: actor, BotUID: in.BotUID, Source: source, NodeID: route.NodeID, ConnectionID: route.ConnectionID},
		route:   route, role: role, deadline: deadline, replies: make(chan publicationCaptureReply, 1)}
	target, err := w.recheck(p)
	if err != nil {
		return nil, err
	}
	// Check cancellation again after the possibly slow runtime/authority lookup.
	latest, err := w.entries.GetSkillHubPublicationEntry(ctx, actor, entryID)
	if err != nil {
		return nil, err
	}
	if latest == nil || latest.ID != entryID || latest.Input != in || latest.State != "waiting_runtime" {
		return nil, store.ErrSkillHubPublicationDenied
	}
	msg := &ServerMessage{ThinToolRPC: &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: p.id,
		TargetOwnerUserID: formatUID(actor), TargetDeviceID: source.DeviceID, DeviceID: source.DeviceID,
		ToolName: string(DeviceCapabilitySkillHubPublicationCapture), CreatedAt: unixMillis(time.Now()), ExpiresAt: unixMillis(deadline),
		Payload: map[string]interface{}{"entry_id": entryID, "bot_uid": strconv.FormatInt(in.BotUID, 10), "local_skill_id": source.LocalSkillID, "workspace_generation": source.WorkspaceGeneration}}}
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.pending[p.id] = p
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.pending, p.id); w.mu.Unlock() }()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	target, err = w.recheck(p)
	if err != nil {
		return nil, err
	}
	// Nonblocking; background backpressure must not disconnect the Bot or block
	// model switching. Store/authority work never runs on the WS read loop.
	if !target.trySend(data) {
		return nil, store.ErrSkillHubPublicationBusy
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-p.replies:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, err := w.recheck(p)
		if err != nil || current != r.client {
			publicationDeliveryAck(r.client, r.id, http.StatusConflict)
			return nil, store.ErrSkillHubPublicationStale
		}
		if r.err != nil {
			publicationDeliveryAck(r.client, r.id, http.StatusUnprocessableEntity)
			return nil, r.err
		}
		record, err := w.captures.RecordSkillHubPublicationCapture(ctx, r.input)
		if err != nil {
			code := http.StatusServiceUnavailable
			if errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationConflict) || errors.Is(err, store.ErrSkillHubPublicationStale) || errors.Is(err, store.ErrSkillHubPublicationSettled) {
				code = http.StatusConflict
			}
			publicationDeliveryAck(r.client, r.id, code)
			return nil, err
		}
		publicationDeliveryAck(r.client, r.id, http.StatusOK)
		return record, nil
	}
}

func (w *publicationCaptureTransport) recheck(p *publicationCapturePending) (*Client, error) {
	return w.h.recheckPublicationRuntime(p, DeviceCapabilitySkillHubPublicationCapture)
}

// Shared socket/route fence; callers retain their own durable authority checks.
// publicationRuntimeBinding may consult current Bot authority: invoke this from
// a service goroutine, never from the WebSocket result callback.
func (h *Hub) recheckPublicationRuntime(p *publicationCapturePending, capability DeviceGrantOperation) (*Client, error) {
	b := p.binding
	s, route, role, code := h.publicationRuntimeBinding(b.ActorUID, b.BotUID, b.Source.DeviceID)
	s.LocalSkillID, s.WorkspaceGeneration = b.Source.LocalSkillID, b.Source.WorkspaceGeneration
	if code != "" || s != b.Source || !route.matches(p.route) || role != p.role || !time.Now().Before(p.deadline) {
		return nil, store.ErrSkillHubPublicationStale
	}
	device, ok := h.userDevices.activeDevice(b.ActorUID, b.Source.DeviceID)
	if !ok {
		return nil, store.ErrSkillHubPublicationStale
	}
	for _, cap := range device.Capabilities {
		if cap == capability {
			client := h.getClientByConnectionID(route.ConnectionID)
			if client != nil {
				return client, nil
			}
		}
	}
	return nil, store.ErrSkillHubPublicationDenied
}

// Only bounded scalar facts are queued, never raw Runtime JSON or file contents.
func (w *publicationCaptureTransport) acceptResult(client *Client, msg *MsgThinToolRPC) {
	w.mu.Lock()
	p := w.pending[msg.RequestID]
	if p == nil || !time.Now().Before(p.deadline) {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusNotFound)
		return
	}
	b := p.binding
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
	r := publicationCaptureReply{client: client, id: msg.ID, err: store.ErrSkillHubPublicationInvalid}
	result, ok := msg.Result.(map[string]interface{})
	// Only a correctly bound, scalar error envelope may request a retry.
	// Unknown/source/security errors and malformed envelopes remain terminal.
	if msg.Error != nil && msg.Result == nil && msg.Type == thinToolRPCTypeResult &&
		msg.ToolName == string(DeviceCapabilitySkillHubPublicationCapture) && msg.DeviceID == b.Source.DeviceID &&
		msg.TargetDeviceID == b.Source.DeviceID && msg.TargetOwnerUserID == formatUID(b.ActorUID) && len(msg.Payload) == 0 &&
		(msg.Error.Code == "publication.runtime_unavailable" || msg.Error.Code == "publication.runtime_timeout") {
		r.err = store.ErrSkillHubPublicationBusy
	}
	if ok && msg.Error == nil && len(result) == 6 && result["schemaVersion"] == float64(1) && result["candidateRevision"] == float64(1) && result["entryId"] == p.entryID &&
		msg.Type == thinToolRPCTypeResult && msg.ToolName == string(DeviceCapabilitySkillHubPublicationCapture) && msg.DeviceID == b.Source.DeviceID &&
		msg.TargetDeviceID == b.Source.DeviceID && msg.TargetOwnerUserID == formatUID(b.ActorUID) && len(msg.Payload) == 0 {
		requestHash, hashOK := result["requestHash"].(string)
		sourceHash, sourceOK := result["sourceSnapshotHash"].(string)
		size, sizeOK := result["requestBytes"].(float64)
		if hashOK && sourceOK && publicationCaptureHash.MatchString(requestHash) && publicationCaptureHash.MatchString(sourceHash) && sizeOK && size >= 1 && size <= publicationCaptureRuntimeMaxBytes && math.Trunc(size) == size {
			r.input = store.SkillHubPublicationCaptureInput{EntryID: p.entryID, Binding: b, Revision: 1, RequestHash: requestHash, SourceSnapshotHash: sourceHash, RequestBytes: int64(size)}
			r.err = r.input.Validate()
		}
	}
	p.queued = true
	p.replies <- r
	w.mu.Unlock()
}
