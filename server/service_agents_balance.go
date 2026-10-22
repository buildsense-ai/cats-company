package server

import (
	"context"
	"errors"
	"fmt"
	"log"
)

var (
	// ErrQuotaExhausted 调用方额度耗尽（调用面拒绝）。
	ErrQuotaExhausted = errors.New("service quota exhausted")
	// ErrRelayUnavailable relay 不可达或返回错误（调用面暂不可用）。
	ErrRelayUnavailable = errors.New("relay unavailable")
)

// ServiceBalanceChecker 是服务调用面的计费预检（设计 §5.3；计划① Task 9）。
// 语义（共享池）：月度预算与共享池行任一「有效限制行」耗尽即拒单（预检非原子——
// 执行期仍由 relay 网关兜底）；限制未配置（max_limit<=0）放行并告警；
// relay 请求失败即拒（对应调用面 503）。
type ServiceBalanceChecker struct {
	admin   *RelayAdminClient
	pending ServicePendingChargeReader
}

// ServicePendingChargeReader 提供「调用方欠款」(微元)：平台已经记账、但还没真正从
// relay 收到的钱 —— pending_credit（结算器待重试）+ charge_failed（relay 明确拒收，
// 需要人工追缴）。预检时并入已用额度，避免「扣不到就等于免费用」绕过预算。
type ServicePendingChargeReader interface {
	SumPendingCallerChargeMicros(ctx context.Context, payerUID int64) (int64, error)
}

func NewServiceBalanceChecker(admin *RelayAdminClient) *ServiceBalanceChecker {
	return &ServiceBalanceChecker{admin: admin}
}

func NewServiceBalanceCheckerFromEnv() *ServiceBalanceChecker {
	return NewServiceBalanceChecker(NewRelayAdminClientFromEnv())
}

// SetPendingChargeReader 注入待扣金额来源（账本）。
func (c *ServiceBalanceChecker) SetPendingChargeReader(reader ServicePendingChargeReader) {
	c.pending = reader
}

func (c *ServiceBalanceChecker) Check(ctx context.Context, callerUID int64) error {
	if c == nil || c.admin == nil {
		// relay admin 未配置（本地/测试）：无法预检，放行（生产环境始终配置）。
		return nil
	}
	user, err := fetchRelayLimitsForUID(ctx, c.admin, callerUID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRelayUnavailable, err)
	}
	if user == nil {
		return nil
	}
	// 待扣金额（外部执行后端自报）并入已用额度：按会话请求实时看账。
	pendingCNY := 0.0
	if c.pending != nil {
		micros, err := c.pending.SumPendingCallerChargeMicros(ctx, callerUID)
		if err != nil {
			log.Printf("service balance precheck: pending charge uid=%d: %v", callerUID, err)
		} else if micros > 0 {
			pendingCNY = float64(micros) / 1_000_000.0
		}
	}
	exhausted := func(budget commercialRelayBudget) bool {
		return budget.MaxLimit > 0 && budget.CurrentUsage+pendingCNY >= budget.MaxLimit
	}
	checked := 0
	if user.Limits.MonthlyBudget.MaxLimit > 0 {
		checked++
		if exhausted(user.Limits.MonthlyBudget) {
			return ErrQuotaExhausted
		}
	}
	for _, limit := range user.Limits.ModelLimits {
		if !limit.SharedBudget {
			continue // 模型独立预算不预判：执行时 relay 网关按实际模型兜底
		}
		if limit.Budget.MaxLimit > 0 {
			checked++
		}
		if exhausted(limit.Budget) {
			return ErrQuotaExhausted
		}
	}
	if checked == 0 {
		log.Printf("service balance precheck: uid=%d has no configured limits; allowing", callerUID)
	}
	return nil
}
