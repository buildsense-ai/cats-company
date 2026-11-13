package server

import (
	"context"
	"log"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 月额度周期对账（docs/credit-plan-rules-20261109.md P0-2/P0-5）。
//
// 账本本身是幂等的（每个周期一个发放键、跨期作废一个键），所以对账可以在任何
// 时候、以任何并发度重跑：预留、资格查询和后台扫描重复跑都不会多发一分钱。
//
// 三个入口：
//   - 预留（db/postgres ReserveModelCredits）：同一个事务里先对账，再按
//     monthly → gift → paid 扣，避免拿上个周期的额度下单；
//   - 资格/余额读：返回前对账，避免把旧周期额度当成当期可花额度；
//   - 后台扫描：按商业服务 ctx 定时推进，没人访问的用户也会在周期边界换期。

// ModelCreditMonthlyReconciler 是月额度对账的存储面（PostgreSQL 适配器实现）。
type ModelCreditMonthlyReconciler interface {
	ReconcileModelCreditMonthlyPeriod(ctx context.Context, uid int64, now time.Time) (*store.ModelCreditAccount, error)
	ReconcileModelCreditMonthlyPeriods(ctx context.Context, now time.Time, limit int) (int, error)
}

// modelCreditMonthlyPeriodReconciler 是读路径顺手对账的最小接口。
type modelCreditMonthlyPeriodReconciler interface {
	ReconcileModelCreditMonthlyPeriod(ctx context.Context, uid int64, now time.Time) (*store.ModelCreditAccount, error)
}

// modelCreditMonthlyReconcileBatch 是后台单轮扫描的用户数上限。
const modelCreditMonthlyReconcileBatch = 200

// reconcileModelCreditMonthlyBeforeRead 在返回余额/资格前把月额度对齐到当前周期。
// 失败不阻断读请求（账本读仍是权威余额），但必须留日志：否则旧周期额度会被当成
// 当期额度一直花下去。
func reconcileModelCreditMonthlyBeforeRead(ctx context.Context, source any, uid int64) {
	reconciler, ok := source.(modelCreditMonthlyPeriodReconciler)
	if !ok || reconciler == nil || uid <= 0 {
		return
	}
	if _, err := reconciler.ReconcileModelCreditMonthlyPeriod(ctx, uid, time.Now().UTC()); err != nil {
		log.Printf("model credit monthly reconcile failed: uid=%d err=%v", uid, err)
	}
}

// StartModelCreditMonthlyReconciler 启动后台周期任务：用商业服务的 ctx 定时把
// 月额度推进到当前周期。批间幂等由账本事件键保证，多实例并发也不会重复发放。
func StartModelCreditMonthlyReconciler(ctx context.Context, reconciler ModelCreditMonthlyReconciler) {
	if reconciler == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			batchCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			count, err := reconciler.ReconcileModelCreditMonthlyPeriods(batchCtx, time.Now().UTC(), modelCreditMonthlyReconcileBatch)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("model credit monthly reconcile sweep failed: %v", err)
			} else if count > 0 {
				log.Printf("model credit monthly periods reconciled: count=%d", count)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
