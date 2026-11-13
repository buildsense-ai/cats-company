package store

import (
	"testing"
	"time"
)

func creditTestTime(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
}

// 月额度周期必须是「自然月对日」：日号取自最初订阅时刻，该月没有这一天时取月末，
// 且永远不会因为上一个周期被收敛而漂移。
func TestModelCreditMonthlyBoundaryKeepsAnchorDay(t *testing.T) {
	anchor := creditTestTime(2027, time.January, 31, 10)
	cases := []struct {
		index int
		want  time.Time
	}{
		{0, creditTestTime(2027, time.January, 31, 10)},
		{1, creditTestTime(2027, time.February, 28, 10)},
		{2, creditTestTime(2027, time.March, 31, 10)},
		{3, creditTestTime(2027, time.April, 30, 10)},
		{4, creditTestTime(2027, time.May, 31, 10)},
		// A leap February still clamps, and March returns to the anchor day.
		{13, creditTestTime(2028, time.February, 29, 10)},
		{14, creditTestTime(2028, time.March, 31, 10)},
	}
	for _, tc := range cases {
		if got := ModelCreditMonthlyBoundary(anchor, tc.index); !got.Equal(tc.want) {
			t.Fatalf("boundary index %d = %s, want %s", tc.index, got, tc.want)
		}
	}
}

func TestModelCreditMonthlyPeriodAt(t *testing.T) {
	anchor := creditTestTime(2027, time.January, 31, 10)
	cases := []struct {
		name  string
		now   time.Time
		start time.Time
		end   time.Time
	}{
		{
			name:  "purchase instant starts the first period",
			now:   creditTestTime(2027, time.January, 31, 10),
			start: creditTestTime(2027, time.January, 31, 10),
			end:   creditTestTime(2027, time.February, 28, 10),
		},
		{
			name:  "mid period",
			now:   creditTestTime(2027, time.February, 14, 9),
			start: creditTestTime(2027, time.January, 31, 10),
			end:   creditTestTime(2027, time.February, 28, 10),
		},
		{
			name:  "clamped boundary opens the next anchored period",
			now:   creditTestTime(2027, time.February, 28, 10),
			start: creditTestTime(2027, time.February, 28, 10),
			end:   creditTestTime(2027, time.March, 31, 10),
		},
		{
			name:  "day after the clamped boundary stays in the anchored period",
			now:   creditTestTime(2027, time.March, 1, 0),
			start: creditTestTime(2027, time.February, 28, 10),
			end:   creditTestTime(2027, time.March, 31, 10),
		},
	}
	for _, tc := range cases {
		start, end, ok := ModelCreditMonthlyPeriodAt(anchor, tc.now)
		if !ok {
			t.Fatalf("%s: expected a period for %s", tc.name, tc.now)
		}
		if !start.Equal(tc.start) || !end.Equal(tc.end) {
			t.Fatalf("%s: period = [%s, %s), want [%s, %s)", tc.name, start, end, tc.start, tc.end)
		}
	}
	if _, _, ok := ModelCreditMonthlyPeriodAt(anchor, anchor.Add(-time.Second)); ok {
		t.Fatal("a time before the subscription must not have a billable period")
	}
}

// 提前续费只延长：付费时刻落在当前周期里，所以「下一个边界」不会因为续费而提前，
// 下一期额度只会在到达边界时才发。
func TestModelCreditMonthlyNextBoundaryForEarlyRenewal(t *testing.T) {
	anchor := creditTestTime(2027, time.January, 31, 10)
	next, ok := ModelCreditMonthlyNextBoundary(anchor, creditTestTime(2027, time.February, 20, 8))
	if !ok || !next.Equal(creditTestTime(2027, time.February, 28, 10)) {
		t.Fatalf("early renewal inside the current period must end at the anchored boundary, got %s ok=%t", next, ok)
	}
	// 续费从上一个周期终点起算时，仍然回到锚点日号，而不是漂移到 28 号。
	next, ok = ModelCreditMonthlyNextBoundary(anchor, creditTestTime(2027, time.February, 28, 10))
	if !ok || !next.Equal(creditTestTime(2027, time.March, 31, 10)) {
		t.Fatalf("renewal anchored on the clamped day must return to the anchor day, got %s ok=%t", next, ok)
	}
}

func TestModelCreditMonthlyGrantKeyRoundTrip(t *testing.T) {
	periodStart := creditTestTime(2027, time.March, 31, 10)
	key := ModelCreditMonthlyGrantKey(42, 7, periodStart)
	if key != "grant:monthly:42:7:20270331T100000.000000000Z" {
		t.Fatalf("grant key = %q", key)
	}
	parsed, ok := ParseModelCreditMonthlyGrantKey(42, key)
	if !ok || !parsed.Equal(periodStart) {
		t.Fatalf("parsed period start = %s ok=%t", parsed, ok)
	}
	if _, ok := ParseModelCreditMonthlyGrantKey(43, key); ok {
		t.Fatal("a grant key must only parse for its own uid")
	}
	if _, ok := ParseModelCreditMonthlyGrantKey(42, "expire:monthly:42:20270331"); ok {
		t.Fatal("an expire key is not a grant key")
	}
	if prefix := ModelCreditMonthlyGrantKeyPrefix(42); prefix != "grant:monthly:42:" {
		t.Fatalf("grant key prefix = %q", prefix)
	}
}

// 邀请与免费/历史基线都不发月额度：它们只是权限，月额度只跟着付过钱的订阅。
func TestModelCreditMonthlyGrantableSource(t *testing.T) {
	for _, source := range []string{"order", "admin", "internal", " Order "} {
		if !ModelCreditMonthlyGrantableSource(source) {
			t.Fatalf("source %q must be able to grant a monthly allowance", source)
		}
	}
	for _, source := range []string{"invite", "free", "legacy", ""} {
		if ModelCreditMonthlyGrantableSource(source) {
			t.Fatalf("source %q must not grant a monthly allowance", source)
		}
	}
}
