package store

import (
	"strconv"
	"strings"
	"time"
)

// 套餐月额度按「自然月对日」结算（docs/credit-plan-rules-20261109.md P0-5）：
// 周期不是固定 30 天，而是从订阅最初生效那一刻起，每个月同一个日子；该月没有
// 这个日子时取当月最后一天。日锚点始终来自最初订阅时刻，而不是上一个周期终点，
// 所以 1/31 → 2/28 → 3/31 不会永久漂移到 28 号。
//
// 这里只做日历计算，不碰账本：PostgreSQL 的月额度发放/到期（
// db/postgres/model_credit_monthly_periods.go）与订单履约的自然月到期都复用这
// 一份实现，避免两处各算一套。

// ModelCreditMonthlyMaxSpan 是世界锚点后允许回溯的月数上限，纯粹是防御性上界，
// 避免坏数据（零值时间、未来锚点）让循环失控。
const ModelCreditMonthlyMaxSpan = 2400

// ModelCreditMonthlyBoundary 返回锚点之后第 index 个月的周期边界（index=0 就是
// 锚点本身）。日取锚点的日号，超过当月天数时收敛到月末。
func ModelCreditMonthlyBoundary(anchor time.Time, index int) time.Time {
	if index <= 0 {
		return anchor
	}
	// 从当月 1 号加月份，避免 AddDate 在 31 号上溢出到下个月。
	base := time.Date(anchor.Year(), anchor.Month(), 1,
		anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), anchor.Location())
	month := base.AddDate(0, index, 0)
	day := anchor.Day()
	if last := daysInModelCreditMonth(month.Year(), month.Month()); day > last {
		day = last
	}
	return time.Date(month.Year(), month.Month(), day,
		anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), anchor.Location())
}

func daysInModelCreditMonth(year int, month time.Month) int {
	// 下个月 0 号 = 本月最后一天。
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// ModelCreditMonthlyPeriodAt 返回 now 落在的周期 [start, end)。now 早于锚点时
// 返回 ok=false：订阅还没开始，没有可发放的周期。
func ModelCreditMonthlyPeriodAt(anchor, now time.Time) (start, end time.Time, ok bool) {
	if anchor.IsZero() || now.Before(anchor) {
		return time.Time{}, time.Time{}, false
	}
	index := (now.Year()-anchor.Year())*12 + int(now.Month()) - int(anchor.Month())
	if index < 0 {
		index = 0
	}
	for step := 0; step < ModelCreditMonthlyMaxSpan; step++ {
		start = ModelCreditMonthlyBoundary(anchor, index)
		if now.Before(start) {
			if index == 0 {
				return time.Time{}, time.Time{}, false
			}
			index--
			continue
		}
		end = ModelCreditMonthlyBoundary(anchor, index+1)
		if now.Before(end) {
			return start, end, true
		}
		index++
	}
	return time.Time{}, time.Time{}, false
}

// ModelCreditMonthlyNextBoundary 返回 from 之后的下一个周期边界。订单履约把月付
// 套餐的到期时间对齐到自然月边界时用它（提前续费只延长，不提前发额度）。
func ModelCreditMonthlyNextBoundary(anchor, from time.Time) (time.Time, bool) {
	if anchor.IsZero() || from.IsZero() || from.Before(anchor) {
		return time.Time{}, false
	}
	index := (from.Year()-anchor.Year())*12 + int(from.Month()) - int(anchor.Month())
	if index < 0 {
		index = 0
	}
	for step := 0; step < ModelCreditMonthlyMaxSpan; step++ {
		boundary := ModelCreditMonthlyBoundary(anchor, index)
		if boundary.After(from) {
			return boundary, true
		}
		index++
	}
	return time.Time{}, false
}

const (
	modelCreditMonthlyGrantKeyPrefix   = "grant:monthly:"
	modelCreditMonthlyExpireKeyPfx     = "expire:monthly:"
	modelCreditMonthlyPeriodDateLayout = "20060102T150405.000000000Z"
)

// ModelCreditMonthlyGrantKey identifies the exact subscription period, including
// its time of day, so repeated reconciliation cannot expire a fresh grant.
func ModelCreditMonthlyGrantKey(uid, planID int64, periodStart time.Time) string {
	return modelCreditMonthlyGrantKeyPrefix + strconv.FormatInt(uid, 10) + ":" +
		strconv.FormatInt(planID, 10) + ":" + periodStart.UTC().Format(modelCreditMonthlyPeriodDateLayout)
}

// ModelCreditMonthlyExpireKey 是「作废某个周期剩余月额度」的幂等键。
func ModelCreditMonthlyExpireKey(uid int64, periodStart time.Time) string {
	return modelCreditMonthlyExpireKeyPfx + strconv.FormatInt(uid, 10) + ":" +
		periodStart.UTC().Format(modelCreditMonthlyPeriodDateLayout)
}

// ModelCreditMonthlyGrantKeyPrefix 是该用户全部月额度发放键的前缀，用来在账本里
// 找回「上次发的是哪个周期」。
func ModelCreditMonthlyGrantKeyPrefix(uid int64) string {
	return modelCreditMonthlyGrantKeyPrefix + strconv.FormatInt(uid, 10) + ":"
}

// ParseModelCreditMonthlyGrantKey recovers the exact UTC period start.
// The recovered instant determines whether monthly credit is still current.
func ParseModelCreditMonthlyGrantKey(uid int64, key string) (time.Time, bool) {
	trimmed := strings.TrimSpace(key)
	if !strings.HasPrefix(trimmed, modelCreditMonthlyGrantKeyPrefix) {
		return time.Time{}, false
	}
	parts := strings.Split(strings.TrimPrefix(trimmed, modelCreditMonthlyGrantKeyPrefix), ":")
	if len(parts) != 3 || parts[0] != strconv.FormatInt(uid, 10) {
		return time.Time{}, false
	}
	at, err := time.ParseInLocation(modelCreditMonthlyPeriodDateLayout, parts[2], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// ModelCreditMonthlyPeriodKeyDate returns the canonical UTC period identity.
// The time of day distinguishes same-day subscriptions and replacements.
func ModelCreditMonthlyPeriodKeyDate(at time.Time) string {
	return at.UTC().Format(modelCreditMonthlyPeriodDateLayout)
}

// ModelCreditMonthlyGrantableSource 说明哪些订阅来源会发月额度：真正付过钱的
// 订单、运营发放的套餐。邀请码与免费/历史基线不发月额度——邀请只发一次 gift，
// 既不继承套餐的月额度（例如 22500），也不能把旧用户迁移到 credit 账本。
func ModelCreditMonthlyGrantableSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "order", "admin", "internal":
		return true
	default:
		return false
	}
}
