package server

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 生命周期回注投递器（设计 §3.3-2 回流通道 / §4.8-2 回注送达）。
// 语义：run 以 completed / failed / awaiting_input 收尾时回执已入收件箱（store 层同事务写入）；
// 本投递器把待投递回执推给调用方账号的在线连接（WS 消息），满足：
//   - 至少一次投递：≥1 个在线连接接收才标记送达；否则留存（attempts 计数），下轮/重连后补投；
//   - 幂等去重：receipt_id 稳定（rcp_<run_id>），重复推送同 id 同内容，接收方据 id 去重；
//   - 离线待取：投递器周期扫描，调用方上线即补投；拉取（catsco_run_status）永远是真相通道。
// 多副本说明：当前生产单副本；多副本形态下推送应走"用户连接所在副本"（设计 §4.8-7），
// 本实现按本地 hub 投递，跨副本场景后续接 sharedRuntime 广播。

const (
	serviceReceiptSweepInterval = 5 * time.Second
	serviceReceiptSweepBatch    = 50
	serviceReceiptSweepTimeout  = 10 * time.Second
)

// serviceRunReceiptPusher 把回执推给 uid 的在线连接，返回接收连接数（*Hub 实现）。
type serviceRunReceiptPusher interface {
	PushServiceRunReceipt(uid int64, receipt *MsgServiceRunReceipt) int
}

type ServiceRunReceiptDeliverer struct {
	store    store.ServiceAgentStore
	pusher   serviceRunReceiptPusher
	interval time.Duration
	kickCh   chan struct{}
	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewServiceRunReceiptDeliverer(st store.ServiceAgentStore, pusher serviceRunReceiptPusher) *ServiceRunReceiptDeliverer {
	return &ServiceRunReceiptDeliverer{
		store:  st,
		pusher: pusher,
		kickCh: make(chan struct{}, 1),
		stopCh: make(chan struct{}),
	}
}

// Kick 唤醒一轮即时投递（非阻塞；编排终态路径调用，未注入时靠周期扫描兜底）。
func (d *ServiceRunReceiptDeliverer) Kick() {
	select {
	case d.kickCh <- struct{}{}:
	default:
	}
}

// Start 周期投递循环（阻塞；与 reaper 同模式，挂 goroutine）。
func (d *ServiceRunReceiptDeliverer) Start(interval time.Duration) {
	if interval <= 0 {
		interval = serviceReceiptSweepInterval
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

func (d *ServiceRunReceiptDeliverer) Stop() {
	d.stopOnce.Do(func() { close(d.stopCh) })
}

func (d *ServiceRunReceiptDeliverer) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), serviceReceiptSweepTimeout)
	defer cancel()
	if _, err := d.SweepOnce(ctx); err != nil {
		log.Printf("service receipt delivery: %v", err)
	}
}

// SweepOnce 投递一轮待投递回执（测试直接调用）。返回本轮送达数。
func (d *ServiceRunReceiptDeliverer) SweepOnce(ctx context.Context) (int, error) {
	receipts, err := d.store.ListUndeliveredServiceRunReceipts(ctx, serviceReceiptSweepBatch)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, receipt := range receipts {
		var msg MsgServiceRunReceipt
		if err := json.Unmarshal(receipt.Payload, &msg); err != nil || msg.ReceiptID == "" {
			// 载荷异常（理论上不可达）：标记送达防止永久堵塞扫描（拉取通道仍是真相）。
			log.Printf("service receipt %s payload invalid: %v", receipt.ReceiptID, err)
			if err := d.store.MarkServiceRunReceiptDelivered(ctx, receipt.ReceiptID); err != nil {
				return delivered, err
			}
			continue
		}
		if d.pusher != nil && d.pusher.PushServiceRunReceipt(receipt.CallerUID, &msg) > 0 {
			if err := d.store.MarkServiceRunReceiptDelivered(ctx, receipt.ReceiptID); err != nil {
				return delivered, err
			}
			delivered++
			continue
		}
		// 无在线连接：留存待补投（attempts 累计，可观测）。
		if err := d.store.TouchServiceRunReceiptDelivery(ctx, receipt.ReceiptID); err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}

// PushServiceRunReceipt 把回执推给调用方账号的在线连接（设计 §4.8-7：走本地 hub 路由）。
func (h *Hub) PushServiceRunReceipt(uid int64, receipt *MsgServiceRunReceipt) int {
	if h == nil || receipt == nil || uid <= 0 {
		return 0
	}
	return h.sendToUserExceptConfirmed(uid, &ServerMessage{ServiceRunReceipt: receipt}, nil)
}
