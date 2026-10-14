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
	admin *RelayAdminClient
}

func NewServiceBalanceChecker(admin *RelayAdminClient) *ServiceBalanceChecker {
	return &ServiceBalanceChecker{admin: admin}
}

func NewServiceBalanceCheckerFromEnv() *ServiceBalanceChecker {
	return NewServiceBalanceChecker(NewRelayAdminClientFromEnv())
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
	exhausted := func(budget commercialRelayBudget) bool {
		return budget.MaxLimit > 0 && budget.CurrentUsage >= budget.MaxLimit
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
