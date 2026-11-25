package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Cats 算力任务回执投递的契约：
//   - 至少一次：≥1 个在线连接接收才标 delivered；离线留存并累计 attempts，重连后补投；
//   - 幂等：同一 receipt_id 投同样的内容，接收方据 id 去重；已送达不再重投；
//   - worker finish 成功后 Kick 一轮（不等周期扫描）；Kick 对 nil deliverer 安全。

type fakeComputeTaskReceiptStore struct {
	receipts  []store.ComputeTaskReceipt
	delivered map[string]bool
	attempts  map[string]int
	touched   []string
}

func newFakeComputeTaskReceiptStore(receipts ...store.ComputeTaskReceipt) *fakeComputeTaskReceiptStore {
	return &fakeComputeTaskReceiptStore{receipts: receipts, delivered: map[string]bool{}, attempts: map[string]int{}}
}

func (f *fakeComputeTaskReceiptStore) ListUndeliveredComputeTaskReceipts(_ context.Context, limit int) ([]store.ComputeTaskReceipt, error) {
	var out []store.ComputeTaskReceipt
	for _, receipt := range f.receipts {
		if f.delivered[receipt.ReceiptID] {
			continue
		}
		receipt.DeliveryAttempts = f.attempts[receipt.ReceiptID]
		out = append(out, receipt)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeComputeTaskReceiptStore) MarkComputeTaskReceiptDelivered(_ context.Context, receiptID string) error {
	f.delivered[receiptID] = true
	return nil
}

func (f *fakeComputeTaskReceiptStore) TouchComputeTaskReceiptDelivery(_ context.Context, receiptID string) error {
	f.attempts[receiptID]++
	f.touched = append(f.touched, receiptID)
	return nil
}

type fakeComputeTaskReceiptPusher struct {
	online int
	pushed []MsgComputeTaskReceipt
	uids   []int64
}

func (p *fakeComputeTaskReceiptPusher) PushComputeTaskReceipt(uid int64, receipt *MsgComputeTaskReceipt) int {
	p.pushed = append(p.pushed, *receipt)
	p.uids = append(p.uids, uid)
	return p.online
}

func testComputeTaskReceipt(quoteID string, uid int64, status string, chargedUnits int64) store.ComputeTaskReceipt {
	receiptID := store.ComputeTaskReceiptIDForQuote(quoteID)
	createdAt := time.Date(2026, 3, 1, 2, 3, 4, 0, time.UTC)
	return store.ComputeTaskReceipt{
		ReceiptID: receiptID,
		QuoteID:   quoteID,
		CallerUID: uid,
		Status:    status,
		Payload: store.BuildComputeTaskReceiptPayload(receiptID, quoteID, status, status, chargedUnits,
			store.ComputeTaskProgress{Phase: "released"}, createdAt),
		CreatedAt: createdAt,
	}
}

// 在线：推一次即标 delivered，且不再重投（receipt_id 幂等）。
func TestComputeTaskReceiptDeliveredWhenCallerOnline(t *testing.T) {
	quoteID := "hpc_0123456789abcdef0123456789abcdef"
	receipt := testComputeTaskReceipt(quoteID, 441, store.ComputeTaskCompleted, 120)
	st := newFakeComputeTaskReceiptStore(receipt)
	pusher := &fakeComputeTaskReceiptPusher{online: 2}
	deliverer := NewComputeTaskReceiptDeliverer(st, pusher)
	ctx := context.Background()

	delivered, err := deliverer.SweepOnce(ctx)
	if err != nil || delivered != 1 {
		t.Fatalf("sweep delivered=%d err=%v, want 1", delivered, err)
	}
	if len(pusher.pushed) != 1 || pusher.uids[0] != 441 {
		t.Fatalf("push = %+v uids=%v", pusher.pushed, pusher.uids)
	}
	msg := pusher.pushed[0]
	if msg.ReceiptID != store.ComputeTaskReceiptIDForQuote(quoteID) || msg.QuoteID != quoteID ||
		msg.ContractVersion != store.ComputeTaskReceiptContract || msg.Status != store.ComputeTaskCompleted ||
		msg.State != store.ComputeTaskCompleted || msg.ChargedUnits != 120 {
		t.Fatalf("pushed receipt = %+v", msg)
	}
	var progress store.ComputeTaskProgress
	if err := json.Unmarshal(msg.Progress, &progress); err != nil || progress.Phase != "released" {
		t.Fatalf("pushed progress = %s err=%v", msg.Progress, err)
	}
	if !st.delivered[receipt.ReceiptID] || len(st.touched) != 0 {
		t.Fatalf("delivered=%v touched=%v", st.delivered, st.touched)
	}
	// 已送达：第二轮既不推送也不累计 attempts。
	if delivered, err := deliverer.SweepOnce(ctx); err != nil || delivered != 0 || len(pusher.pushed) != 1 {
		t.Fatalf("second sweep delivered=%d err=%v pushed=%d", delivered, err, len(pusher.pushed))
	}
}

// 离线：留存 + attempts 累计，调用方上线后补投。
func TestComputeTaskReceiptRetainedWhileCallerOffline(t *testing.T) {
	quoteID := "hpc_ffffffffffffffffffffffffffffffff"
	receipt := testComputeTaskReceipt(quoteID, 442, store.ComputeTaskFailed, 0)
	st := newFakeComputeTaskReceiptStore(receipt)
	pusher := &fakeComputeTaskReceiptPusher{online: 0}
	deliverer := NewComputeTaskReceiptDeliverer(st, pusher)
	ctx := context.Background()

	for round := 1; round <= 2; round++ {
		if delivered, err := deliverer.SweepOnce(ctx); err != nil || delivered != 0 {
			t.Fatalf("offline sweep #%d delivered=%d err=%v, want 0", round, delivered, err)
		}
	}
	if st.delivered[receipt.ReceiptID] || st.attempts[receipt.ReceiptID] != 2 {
		t.Fatalf("offline receipt marked delivered=%v attempts=%d", st.delivered[receipt.ReceiptID], st.attempts[receipt.ReceiptID])
	}
	pending, err := st.ListUndeliveredComputeTaskReceipts(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].QuoteID != quoteID {
		t.Fatalf("pending = %+v err=%v", pending, err)
	}

	pusher.online = 1
	if delivered, err := deliverer.SweepOnce(ctx); err != nil || delivered != 1 {
		t.Fatalf("reconnect sweep delivered=%d err=%v, want 1", delivered, err)
	}
	if !st.delivered[receipt.ReceiptID] {
		t.Fatal("receipt not delivered after the caller reconnected")
	}
}

// 载荷异常（理论上不可达）不能永久阻塞扫描：直接标送达，拉取通道仍是真相。
func TestComputeTaskReceiptInvalidPayloadDoesNotBlockSweep(t *testing.T) {
	broken := store.ComputeTaskReceipt{ReceiptID: "ctr_broken", QuoteID: "hpc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", CallerUID: 443,
		Status: store.ComputeTaskFailed, Payload: json.RawMessage(`"not-an-object"`)}
	good := testComputeTaskReceipt("hpc_cccccccccccccccccccccccccccccccc", 444, store.ComputeTaskCompleted, 10)
	st := newFakeComputeTaskReceiptStore(broken, good)
	pusher := &fakeComputeTaskReceiptPusher{online: 1}
	deliverer := NewComputeTaskReceiptDeliverer(st, pusher)
	delivered, err := deliverer.SweepOnce(context.Background())
	if err != nil || delivered != 1 {
		t.Fatalf("sweep delivered=%d err=%v, want 1", delivered, err)
	}
	if !st.delivered["ctr_broken"] {
		t.Fatal("invalid payload receipt must be marked delivered instead of retried forever")
	}
	if len(pusher.pushed) != 1 || pusher.pushed[0].QuoteID != good.QuoteID {
		t.Fatalf("pushes = %+v", pusher.pushed)
	}
}

// Kick 对 nil deliverer / nil store 安全，避免装配处到处判空。
func TestComputeTaskReceiptDelivererNilSafety(t *testing.T) {
	var nilDeliverer *ComputeTaskReceiptDeliverer
	nilDeliverer.Kick()
	nilDeliverer.Stop()
	if delivered, err := nilDeliverer.SweepOnce(context.Background()); err != nil || delivered != 0 {
		t.Fatalf("nil deliverer sweep delivered=%d err=%v", delivered, err)
	}
	withoutStore := NewComputeTaskReceiptDeliverer(nil, nil)
	if delivered, err := withoutStore.SweepOnce(context.Background()); err != nil || delivered != 0 {
		t.Fatalf("storeless sweep delivered=%d err=%v", delivered, err)
	}
	var hub *Hub
	if pushed := hub.PushComputeTaskReceipt(441, &MsgComputeTaskReceipt{ReceiptID: "ctr_x"}); pushed != 0 {
		t.Fatalf("nil hub pushed %d", pushed)
	}
}

type countingComputeTaskReceiptKicker struct{ kicks int }

func (k *countingComputeTaskReceiptKicker) Kick() { k.kicks++ }

// computeSaveKickStore 让 save 也能走到：内嵌的结算替身没实现 SaveComputeTaskProgress。
type computeSaveKickStore struct{ *computeSettlementStore }

func (s computeSaveKickStore) SaveComputeTaskProgress(context.Context, string, string, store.ComputeTaskProgress) error {
	return nil
}

// worker finish 成功后立即 Kick（周期扫描只是兜底）；save 不 Kick。
func TestComputeTaskWorkerFinishKicksReceiptDelivery(t *testing.T) {
	id := "hpc_" + strings.Repeat("1", 32)
	lease := "hpc_" + strings.Repeat("2", 32)
	confirmed := time.Now().UTC().Add(-time.Hour)
	started := confirmed.Add(time.Minute)
	released := started.Add(20 * time.Minute)
	st := &computeSettlementStore{run: &store.ComputeTaskRun{
		ConfirmedAt: &confirmed,
		ComputeTaskQuote: store.ComputeTaskQuote{
			Spec:     store.ComputeTaskSpec{Nodes: 2, LogicalCPUs: 2, MemoryMiB: 1024, WallSeconds: 60},
			Rate:     store.ComputeTaskRate{UnitsPerNodePeriod: 30_000_000, PeriodSeconds: 3600, IncrementSeconds: 60},
			MaxUnits: 5_000_000, MaxNodeSeconds: 600,
		},
	}}
	progress := store.ComputeTaskProgress{
		Phase: "released", QueueID: "pt-owned", QueueCreateOperation: "job-create", QueueDeleteOperation: "job-delete",
		ExpandOperation: "job-expand", ShrinkOperation: "job-shrink", ProvisionStartedAt: &started, ReleasedAt: &released,
		NodeIDs: []string{"nd-a", "nd-b"}, NodeNames: []string{"wn1", "wn2"}, InstanceIDs: []string{"ecs-a", "ecs-b"}, VolumeIDs: []string{"evs-a", "evs-b"},
		SlurmJobID: "12", SlurmState: "COMPLETED", SlurmExitCode: "0:0", SlurmNodeList: []string{"wn2", "wn1"}, SlurmElapsed: 15, SlurmAllocatedCPUs: 4,
	}
	st.run.Progress = progress
	kicker := &countingComputeTaskReceiptKicker{}
	handler := &ComputeTaskHandler{store: computeSaveKickStore{st}, workerToken: strings.Repeat("t", 32), receiptKicker: kicker}

	// save 不动回执投递。
	saveBody, err := json.Marshal(map[string]any{"action": "save", "quote_id": id, "lease": lease, "progress": progress})
	if err != nil {
		t.Fatal(err)
	}
	saveReq := httptest.NewRequest(http.MethodPost, computeTaskWorkerRoute, strings.NewReader(string(saveBody)))
	saveReq.Header.Set("Authorization", "Bearer "+handler.workerToken)
	saveResponse := httptest.NewRecorder()
	handler.serveWorker(saveResponse, saveReq)
	if saveResponse.Code != http.StatusNoContent || kicker.kicks != 0 {
		t.Fatalf("save HTTP %d kicks=%d, want 204 and no kick", saveResponse.Code, kicker.kicks)
	}

	// finish 成功 → Kick 一次。
	finishBody, err := json.Marshal(map[string]any{"action": "finish", "quote_id": id, "lease": lease, "progress": progress})
	if err != nil {
		t.Fatal(err)
	}
	finishReq := httptest.NewRequest(http.MethodPost, computeTaskWorkerRoute, strings.NewReader(string(finishBody)))
	finishReq.Header.Set("Authorization", "Bearer "+handler.workerToken)
	finishResponse := httptest.NewRecorder()
	handler.serveWorker(finishResponse, finishReq)
	if finishResponse.Code != http.StatusNoContent {
		t.Fatalf("finish HTTP %d, want 204: %s", finishResponse.Code, finishResponse.Body)
	}
	if !st.finished || kicker.kicks != 1 {
		t.Fatalf("finish settled=%v kicks=%d, want settlement and one kick", st.finished, kicker.kicks)
	}
}

// 没有任何 kicker（老装配 / 测试替身）时 finish 依然成功，靠周期扫描兜底。
func TestComputeTaskWorkerFinishWithoutKicker(t *testing.T) {
	id := "hpc_" + strings.Repeat("3", 32)
	lease := "hpc_" + strings.Repeat("4", 32)
	confirmed := time.Now().UTC().Add(-time.Hour)
	started := confirmed.Add(time.Minute)
	released := started.Add(20 * time.Minute)
	progress := store.ComputeTaskProgress{
		Phase: "released", QueueID: "pt-owned", QueueCreateOperation: "job-create", QueueDeleteOperation: "job-delete",
		ExpandOperation: "job-expand", ShrinkOperation: "job-shrink", ProvisionStartedAt: &started, ReleasedAt: &released,
		NodeIDs: []string{"nd-a", "nd-b"}, NodeNames: []string{"wn1", "wn2"}, InstanceIDs: []string{"ecs-a", "ecs-b"}, VolumeIDs: []string{"evs-a", "evs-b"},
		SlurmJobID: "12", SlurmState: "COMPLETED", SlurmExitCode: "0:0", SlurmNodeList: []string{"wn1", "wn2"}, SlurmElapsed: 15, SlurmAllocatedCPUs: 4,
	}
	st := &computeSettlementStore{run: &store.ComputeTaskRun{
		ConfirmedAt: &confirmed, Progress: progress,
		ComputeTaskQuote: store.ComputeTaskQuote{
			Spec:     store.ComputeTaskSpec{Nodes: 2, LogicalCPUs: 2, MemoryMiB: 1024, WallSeconds: 60},
			Rate:     store.ComputeTaskRate{UnitsPerNodePeriod: 30_000_000, PeriodSeconds: 3600, IncrementSeconds: 60},
			MaxUnits: 5_000_000, MaxNodeSeconds: 600,
		},
	}}
	handler := &ComputeTaskHandler{store: st, workerToken: strings.Repeat("t", 32)}
	body, err := json.Marshal(map[string]any{"action": "finish", "quote_id": id, "lease": lease, "progress": progress})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, computeTaskWorkerRoute, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+handler.workerToken)
	response := httptest.NewRecorder()
	handler.serveWorker(response, req)
	if response.Code != http.StatusNoContent || !st.finished {
		t.Fatalf("finish without kicker HTTP %d settled=%v", response.Code, st.finished)
	}
}
