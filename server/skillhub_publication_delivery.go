package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const publicationDeliveryRequestPrefix = "pubdelivery_"
const publicationDeliveryPendingLimit = 32
const publicationDeliveryBotBatch = 16
const publicationDeliveryDispatchBatch = 8

type publicationDeliveryPending struct {
	id         string
	lease      store.SkillHubPublicationDeliveryLeaseRecord
	route      runtimeRoute
	role       string
	deadline   time.Time
	processing bool // protected by worker.mu; includes queued/in-flight DB work
}

type publicationDeliveryResult struct {
	pending   *publicationDeliveryPending
	client    *Client
	messageID string
	code      string // normalized only; never store/queue raw runtime objects
}

type publicationDeliveryWorker struct {
	h          *Hub
	entries    store.SkillHubPublicationEntryStore
	deliveries store.SkillHubPublicationDeliveryStore
	lister     store.SkillHubPublicationRuntimeDeliveryLister
	ctx        context.Context
	cancel     context.CancelFunc
	started    atomic.Bool
	running    atomic.Bool
	sweepMu    sync.Mutex
	mu         sync.Mutex
	pending    map[string]*publicationDeliveryPending
	results    chan publicationDeliveryResult
	cursor     int64 // sweepMu
}

func newPublicationDeliveryWorkerFromEnv(h *Hub) *publicationDeliveryWorker {
	if !publicationDeliveryEnabled() {
		return nil
	}
	return newPublicationDeliveryWorker(h)
}

func publicationDeliveryEnabled() bool {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DELIVERY_ENABLED"))
	return publicationWorkspaceEntriesEnabled() && (v == "1" || strings.EqualFold(v, "true"))
}

func newPublicationDeliveryWorker(h *Hub) *publicationDeliveryWorker {
	if h == nil || h.db == nil {
		return nil
	}
	entries, ok := h.db.(store.SkillHubPublicationEntryStore)
	if !ok {
		return nil
	}
	deliveries, ok := h.db.(store.SkillHubPublicationDeliveryStore)
	if !ok {
		return nil
	}
	lister, ok := h.db.(store.SkillHubPublicationRuntimeDeliveryLister)
	if !ok {
		return nil
	}
	if _, ok := h.db.(store.ConversationTaskGenerationStore); !ok {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &publicationDeliveryWorker{h: h, entries: entries, deliveries: deliveries, lister: lister, ctx: ctx, cancel: cancel,
		pending: make(map[string]*publicationDeliveryPending), results: make(chan publicationDeliveryResult, publicationDeliveryPendingLimit)}
}

// Called only after selecting the final Hub (local or shared-runtime). No worker
// starts from NewHub, and every goroutine below is tied to the server lifecycle.
func (h *Hub) RunPublicationDeliveries(ctx context.Context) {
	if h == nil || h.publicationDelivery == nil {
		return
	}
	w := h.publicationDelivery
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	if ctx.Err() != nil {
		w.cancel()
		return
	}
	stop := context.AfterFunc(ctx, w.cancel)
	defer stop()
	w.running.Store(true)
	defer w.running.Store(false)
	defer w.cancel()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-w.ctx.Done():
					return
				case r := <-w.results:
					w.processResult(r)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		w.sweep()
		for {
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
				w.sweep()
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-w.ctx.Done():
			wg.Wait()
			return
		case now := <-ticker.C:
			w.queueTimeouts(now)
		}
	}
}

type publicationDeliveryBot struct {
	uid, owner int64
	device     string
}

// Only this node's authenticated Bot sockets are candidates. Never forward
// private source selectors through Redis or synthesize an Owner Client.
func (w *publicationDeliveryWorker) localBots() []publicationDeliveryBot {
	return w.h.publicationLocalBots(w.cursor)
}

func (h *Hub) publicationLocalBots(cursor int64) []publicationDeliveryBot {
	h.mu.RLock()
	byUID := make(map[int64]publicationDeliveryBot)
	for uid, clients := range h.clients {
		for c := range clients {
			if c.accountType == types.AccountBot && c.deviceConnector == nil && c.wikiAgentUID == 0 && c.deviceOwnerUID > 0 && c.deviceID != "" {
				byUID[uid] = publicationDeliveryBot{uid, c.deviceOwnerUID, c.deviceID}
			}
		}
	}
	h.mu.RUnlock()
	ids := make([]int64, 0, len(byUID))
	for id := range byUID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) == 0 {
		return nil
	}
	start := sort.Search(len(ids), func(i int) bool { return ids[i] > cursor })
	out := make([]publicationDeliveryBot, 0, publicationDeliveryBotBatch)
	for i := 0; i < len(ids) && i < publicationDeliveryBotBatch; i++ {
		out = append(out, byUID[ids[(start+i)%len(ids)]])
	}
	return out
}

func (w *publicationDeliveryWorker) hasCapacity() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending) < publicationDeliveryPendingLimit
}

func (w *publicationDeliveryWorker) sweep() {
	if !w.running.Load() || w.ctx.Err() != nil || !w.sweepMu.TryLock() {
		return
	}
	defer w.sweepMu.Unlock()
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	defer cancel()
	dispatched := 0
	for _, bot := range w.localBots() {
		if ctx.Err() != nil || !w.hasCapacity() || dispatched >= publicationDeliveryDispatchBatch {
			return
		}
		w.cursor = bot.uid
		// Check the exact local runtime before any list/claim. Offline or legacy
		// runtimes consume no attempts and never use another device as fallback.
		runtimeSource, _, _, code := w.h.publicationRuntimeBinding(bot.owner, bot.uid, bot.device)
		if code != "" {
			continue
		}
		selector := store.SkillHubPublicationRuntimeSelector{ActorUID: bot.owner, BotUID: bot.uid, DeviceID: runtimeSource.DeviceID, BodyID: runtimeSource.BodyID, InstallationID: runtimeSource.InstallationID}
		ids, err := w.lister.ListSkillHubPublicationRuntimeDeliveries(ctx, selector, 4)
		if err != nil {
			return
		} // bounded outage backoff: next sweep, not an inner retry
		for _, id := range ids {
			if ctx.Err() != nil || !w.hasCapacity() || dispatched >= publicationDeliveryDispatchBatch {
				return
			}
			entry, err := w.entries.GetSkillHubPublicationEntry(ctx, bot.owner, id)
			if err != nil {
				return
			}
			if entry == nil || entry.ID != id || entry.State != "waiting_runtime" || entry.Input.Validate() != nil || entry.Input.ActorUID != bot.owner || entry.Input.BotUID != bot.uid || entry.Input.Source.DeviceID != bot.device {
				continue
			}
			source, route, role, code := w.h.publicationRuntimeBinding(entry.Input.ActorUID, entry.Input.BotUID, entry.Input.Source.DeviceID)
			if code != "" {
				continue
			}
			source.LocalSkillID, source.WorkspaceGeneration = entry.Input.Source.LocalSkillID, entry.Input.Source.WorkspaceGeneration
			expected := entry.Input.Source
			if source.RouteGeneration < expected.RouteGeneration {
				continue
			}
			expected.RouteGeneration = source.RouteGeneration
			if source != expected {
				continue
			}
			binding := store.SkillHubPublicationDeliveryBinding{ActorUID: entry.Input.ActorUID, BotUID: entry.Input.BotUID, Source: source, NodeID: route.NodeID, ConnectionID: route.ConnectionID}
			lease, err := w.deliveries.ClaimSkillHubPublicationDelivery(ctx, id, binding)
			if err != nil {
				continue
			}
			dispatched++
			if lease == nil || lease.EntryID != id || lease.Binding != binding || !store.ValidSkillHubPublicationDeliveryToken(lease.Token) {
				continue
			}
			p := &publicationDeliveryPending{id: publicationDeliveryRequestPrefix + rand.Text(), lease: *lease, route: route, role: role, deadline: lease.ExpiresAt.Add(-5 * time.Second)}
			if !time.Now().Before(p.deadline) || w.ctx.Err() != nil {
				continue
			}
			// Register before sending so an immediate runtime reply cannot be lost.
			w.mu.Lock()
			w.pending[p.id] = p
			w.mu.Unlock()
			target, code := w.recheck(p)
			if code != "" || w.ctx.Err() != nil {
				w.remove(p)
				continue
			}
			msg := &ServerMessage{ThinToolRPC: &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: p.id,
				TargetOwnerUserID: formatUID(binding.ActorUID), TargetDeviceID: source.DeviceID, DeviceID: source.DeviceID,
				ToolName: string(DeviceCapabilitySkillHubPublicationProbe), CreatedAt: unixMillis(time.Now()), ExpiresAt: unixMillis(p.deadline),
				Payload: map[string]interface{}{"bot_uid": strconv.FormatInt(binding.BotUID, 10), "local_skill_id": source.LocalSkillID, "workspace_generation": source.WorkspaceGeneration}}}
			data, err := json.Marshal(msg)
			// A full Bot send queue must not disconnect it or block model switching.
			// A slow binding lookup may overlap timeout settlement. Do not send a
			// probe already expired/claimed by the independent timeout worker.
			w.mu.Lock()
			active := w.pending[p.id] == p && !p.processing && time.Now().Before(p.deadline) && w.ctx.Err() == nil
			sent := false
			if active && err == nil {
				sent = target.trySend(data)
			}
			w.mu.Unlock()
			if active && !sent {
				w.queueFailure(p, "publication.runtime_unavailable")
			}
		}
	}
}

func (w *publicationDeliveryWorker) recheck(p *publicationDeliveryPending) (*Client, string) {
	b := p.lease.Binding
	source, route, role, code := w.h.publicationRuntimeBinding(b.ActorUID, b.BotUID, b.Source.DeviceID)
	if code != "" {
		return nil, code
	}
	source.LocalSkillID, source.WorkspaceGeneration = b.Source.LocalSkillID, b.Source.WorkspaceGeneration
	if source != b.Source || !route.matches(p.route) || role != p.role {
		return nil, "publication.runtime_changed"
	}
	client := w.h.getClientByConnectionID(route.ConnectionID)
	if client == nil {
		return nil, "publication.runtime_unavailable"
	}
	return client, ""
}

func (w *publicationDeliveryWorker) remove(p *publicationDeliveryPending) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending[p.id] == p {
		delete(w.pending, p.id)
	}
}

func (w *publicationDeliveryWorker) queueFailure(p *publicationDeliveryPending, code string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending[p.id] != p || p.processing {
		return
	}
	select {
	case w.results <- publicationDeliveryResult{pending: p, code: code}:
		p.processing = true
	default:
	}
}

func (w *publicationDeliveryWorker) queueTimeouts(now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, p := range w.pending {
		if p.processing || now.Before(p.deadline) {
			continue
		}
		select {
		case w.results <- publicationDeliveryResult{pending: p, code: "publication.runtime_timeout"}:
			p.processing = true
		default:
			return
		}
	}
}

// No database access on the WebSocket read loop. Identity checks here are cheap;
// two bounded workers perform fresh authority checks and durable receipt writes.
func (w *publicationDeliveryWorker) acceptResult(client *Client, msg *MsgThinToolRPC) {
	if !w.running.Load() || w.ctx.Err() != nil {
		publicationDeliveryAck(client, msg.ID, http.StatusServiceUnavailable)
		return
	}
	w.mu.Lock()
	p := w.pending[msg.RequestID]
	if p == nil || !time.Now().Before(p.deadline) {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusNotFound)
		return
	}
	w.h.mu.RLock()
	b := p.lease.Binding
	matched := client != nil && client.accountType == types.AccountBot && client.uid == b.BotUID && client.deviceConnector == nil && client.wikiAgentUID == 0 &&
		w.h.clientsByConn[b.ConnectionID] == client && client.connectionID == b.ConnectionID && w.h.nodeID == b.NodeID &&
		client.deviceOwnerUID == b.ActorUID && client.deviceID == b.Source.DeviceID && client.bodyID == b.Source.BodyID && client.installationID == b.Source.InstallationID
	w.h.mu.RUnlock()
	if !matched {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusForbidden)
		return
	}
	if p.processing {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusConflict)
		return
	}
	code := ""
	if msg.Error != nil {
		code = "publication.runtime_unavailable"
		runtimeCode := normalizePublicationProbeRequestError(msg.Error.Code)
		if _, ok := store.SkillHubPublicationDeliveryOutcome(runtimeCode); ok && runtimeCode != "" {
			code = runtimeCode
		}
	} else {
		result, ok := msg.Result.(map[string]interface{})
		if !ok || len(result) != 3 || result["schema_version"] != float64(1) || result["local_skill_id"] != b.Source.LocalSkillID || result["workspace_generation"] != b.Source.WorkspaceGeneration {
			code = "publication.source_changed"
		}
	}
	if (msg.ToolName != "" && msg.ToolName != string(DeviceCapabilitySkillHubPublicationProbe)) ||
		(msg.DeviceID != "" && msg.DeviceID != b.Source.DeviceID) ||
		(msg.TargetDeviceID != "" && msg.TargetDeviceID != b.Source.DeviceID) ||
		(msg.TargetOwnerUserID != "" && msg.TargetOwnerUserID != formatUID(b.ActorUID)) ||
		len(msg.Payload) > 0 || (msg.Error != nil && msg.Result != nil) {
		code = "publication.source_changed"
	}
	select {
	case w.results <- publicationDeliveryResult{pending: p, client: client, messageID: msg.ID, code: code}:
		p.processing = true
		w.mu.Unlock()
	default:
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusTooManyRequests)
	}
}

func (w *publicationDeliveryWorker) processResult(r publicationDeliveryResult) {
	p := r.pending
	defer w.remove(p)
	if w.ctx.Err() != nil {
		return
	} // retained DB lease recovers after shutdown/crash
	if r.client != nil {
		target, code := w.recheck(p)
		if code != "" || target != r.client {
			publicationDeliveryAck(r.client, r.messageID, http.StatusConflict)
			return
		}
	}
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	defer cancel()
	err := w.deliveries.FinishSkillHubPublicationDelivery(ctx, p.lease, r.code)
	status := http.StatusOK
	if err != nil {
		status = http.StatusServiceUnavailable
		if errors.Is(err, store.ErrSkillHubPublicationStale) || errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationSettled) {
			status = http.StatusConflict
		}
	}
	if r.client != nil {
		publicationDeliveryAck(r.client, r.messageID, status)
	}
}

func publicationDeliveryAck(client *Client, id string, status int) {
	if client == nil {
		return
	}
	data, err := json.Marshal(&ServerMessage{Ctrl: &MsgServerCtrl{ID: id, Code: status, Text: "publication source receipt"}})
	if err == nil {
		client.trySend(data)
	} // never disconnect a Bot for background backpressure
}
