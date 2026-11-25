package server

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Cats 算力任务完成回执的投递器。
//
// 与 service_run_receipts 是两条独立链路：算力任务是 /api/compute/tasks/... 这个
// 独立工具，不走 service_runs，所以回执自己入箱（FinishComputeTask 同一事务）：
//   - 至少一次投递：≥1 个在线连接接收才标 delivered；否则留存（attempts 计数），
//     下轮 / 调用方重连后补投；
//   - 幂等去重：receipt_id 由 quote_id 确定性派生（ctr_<quote 去 hpc_ 前缀>），
//     重投同 id 同内容，接收方据 id 去重；
//   - 离线待取：周期扫描兜底，拉取（catsco_compute_task_status）永远是真相通道。
//
// worker finish 成功后立即 Kick 一轮（ComputeTaskHandler 注入同一个 kicker），
// 周期扫描只是兜底：Kick 丢了、进程重启、离线补投都靠它。
const (
	computeTaskReceiptSweepInterval = 5 * time.Second
	computeTaskReceiptSweepBatch    = 50
	computeTaskReceiptSweepTimeout  = 10 * time.Second
)

// computeTaskReceiptPusher 把回执推给 uid 的在线连接，返回接收连接数（*Hub 实现）。
type computeTaskReceiptPusher interface {
	PushComputeTaskReceipt(uid int64, receipt *MsgComputeTaskReceipt) int
}

type ComputeTaskReceiptDeliverer struct {
	store    store.ComputeTaskReceiptStore
	pusher   computeTaskReceiptPusher
	interval time.Duration
	kickCh   chan struct{}
	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewComputeTaskReceiptDeliverer(st store.ComputeTaskReceiptStore, pusher computeTaskReceiptPusher) *ComputeTaskReceiptDeliverer {
	return &ComputeTaskReceiptDeliverer{
		store:  st,
		pusher: pusher,
		kickCh: make(chan struct{}, 1),
		stopCh: make(chan struct{}),
	}
}

// Kick 唤醒一轮即时投递（非阻塞；worker finish 成功后调用，未注入时靠周期扫描兜底）。
// nil 接收者安全：装配处可以用可选 kicker 直接注入，不必判空。
func (d *ComputeTaskReceiptDeliverer) Kick() {
	if d == nil {
		return
	}
	select {
	case d.kickCh <- struct{}{}:
	default:
	}
}

// Start 周期投递循环（阻塞；挂 goroutine，与 service receipt 投递器同模式）。
func (d *ComputeTaskReceiptDeliverer) Start(interval time.Duration) {
	if d == nil {
		return
	}
	if interval <= 0 {
		interval = computeTaskReceiptSweepInterval
	}
	if d.store == nil {
		return
	}
	d.interval = interval
	d.sweep()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopCh:
			return
		case <-d.kickCh:
			d.sweep()
		case <-ticker.C:
			d.sweep()
		}
	}
}

func (d *ComputeTaskReceiptDeliverer) Stop() {
	if d == nil {
		return
	}
	d.stopOnce.Do(func() { close(d.stopCh) })
}

func (d *ComputeTaskReceiptDeliverer) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), computeTaskReceiptSweepTimeout)
	defer cancel()
	if _, err := d.SweepOnce(ctx); err != nil {
		log.Printf("compute task receipt delivery: %v", err)
	}
}

// SweepOnce 投递一轮待投递回执（测试直接调用）。返回本轮送达数。
func (d *ComputeTaskReceiptDeliverer) SweepOnce(ctx context.Context) (int, error) {
	if d == nil || d.store == nil {
		return 0, nil
	}
	receipts, err := d.store.ListUndeliveredComputeTaskReceipts(ctx, computeTaskReceiptSweepBatch)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, receipt := range receipts {
		var msg MsgComputeTaskReceipt
		if err := json.Unmarshal(receipt.Payload, &msg); err != nil || msg.ReceiptID == "" {
			// 载荷异常（理论上不可达）：标记送达防止永久堵塞扫描（拉取通道仍是真相）。
			log.Printf("compute task receipt %s payload invalid: %v", receipt.ReceiptID, err)
			if err := d.store.MarkComputeTaskReceiptDelivered(ctx, receipt.ReceiptID); err != nil {
				return delivered, err
			}
			continue
		}
		if d.pusher != nil && d.pusher.PushComputeTaskReceipt(receipt.CallerUID, &msg) > 0 {
			if err := d.store.MarkComputeTaskReceiptDelivered(ctx, receipt.ReceiptID); err != nil {
				return delivered, err
			}
			delivered++
			continue
		}
		// 无在线连接：留存待补投（attempts 累计，可观测）。
		if err := d.store.TouchComputeTaskReceiptDelivery(ctx, receipt.ReceiptID); err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}

// PushComputeTaskReceipt 把回执推给调用方账号的在线连接（走本地 hub 路由）。
func (h *Hub) PushComputeTaskReceipt(uid int64, receipt *MsgComputeTaskReceipt) int {
	if h == nil || receipt == nil || uid <= 0 {
		return 0
	}
	return h.sendToUserExceptConfirmed(uid, &ServerMessage{ComputeTaskReceipt: receipt}, nil)
}
