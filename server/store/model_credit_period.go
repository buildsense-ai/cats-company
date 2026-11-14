package store

import "time"

// ModelCreditPeriod 是「此刻」的月额度窗口，外加下一期额度是否已经被付过。
//
// 它只说事实，不做推算：周期由订阅自己的锚点算出来
// （ModelCreditMonthlyPeriodAt），所以 1/31 → 2/28 → 3/31 的月末收敛和实际发放
// 完全一致；提前续费产生的新周期还没开始，因此既不会改写当前窗口，也不会被当成
// 「当下有效」。
//
// NextRefreshAt 只在下一周期边界已经落在某个生效订阅的覆盖范围内时才给出；订阅
// 恰好在本期结束时到期（最常见的「没有续费」状态）就没有下一个确定刷新点，调用方
// 只能拿 SubscriptionExpiresAt / MonthlyPeriodEndsAt 告诉用户「续费后才会刷新」，
// 不能自己按 +1 个月推算。
type ModelCreditPeriod struct {
	// MonthlyPeriodStartsAt / MonthlyPeriodEndsAt 是当前自然月周期的半开区间
	// [start, end)，end 是下一周期开始的那个边界。
	MonthlyPeriodStartsAt time.Time
	MonthlyPeriodEndsAt   time.Time
	// SubscriptionExpiresAt 是覆盖当下的那条订阅的到期时间；nil 表示没有固定期限
	// （internal/admin 的长期订阅）。
	SubscriptionExpiresAt *time.Time
	// NextRefreshAt 是下一次确定会发额度的时刻；nil 表示没有已付订阅覆盖下一
	// 周期，刷新要等用户续费。
	NextRefreshAt *time.Time
}
