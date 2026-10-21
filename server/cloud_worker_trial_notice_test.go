package server

import (
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// The one-minute sweep cadence is what makes this promise true; keeping the
// assertion next to the notice means a future slowdown fails here instead of
// silently misleading users in production.
func TestTrialNoticeConversionPromisesOneMinute(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	item := types.CloudWorkerLifecycle{
		BillingMode:       types.CloudWorkerOnDemand,
		ConversionPending: true,
	}
	got := trialNotice(item, now)
	if !strings.Contains(got, "1 分钟") {
		t.Fatalf("conversion notice must set the one-minute expectation, got %q", got)
	}
}

func TestTrialNoticeCountdownNearExpiry(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	item := types.CloudWorkerLifecycle{BillingMode: types.CloudWorkerOnDemand}

	// Under 48h the countdown drops to hours so a same-day expiry is obvious.
	item.PackageExpiresAt = now.Add(30 * time.Hour)
	if got := trialNotice(item, now); !strings.Contains(got, "试用剩余") || !strings.Contains(got, "30 小时") {
		t.Fatalf("expected an hour countdown inside the window, got %q", got)
	}

	// Between 48h and the 72h window boundary it reports whole days.
	item.PackageExpiresAt = now.Add(70 * time.Hour)
	if got := trialNotice(item, now); !strings.Contains(got, "2 天") {
		t.Fatalf("expected a day countdown above 48h, got %q", got)
	}

	// A sub-hour remainder must never render as "0 天".
	item.PackageExpiresAt = now.Add(30 * time.Minute)
	if got := trialNotice(item, now); !strings.Contains(got, "不足 1 小时") {
		t.Fatalf("sub-hour remainder must stay explicit, got %q", got)
	}
}

func TestTrialNoticeOmitsCountdownFarFromExpiry(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	item := types.CloudWorkerLifecycle{
		BillingMode:      types.CloudWorkerOnDemand,
		PackageExpiresAt: now.Add(10 * 24 * time.Hour),
	}
	got := trialNotice(item, now)
	if strings.Contains(got, "试用剩余") {
		t.Fatalf("far-from-expiry copy should keep the plain date, got %q", got)
	}
	if !strings.Contains(got, "付费可继续使用") {
		t.Fatalf("renewal hint missing from the pre-expiry copy, got %q", got)
	}
}

func TestTrialNoticeExpiredExplainsPauseAndDeletion(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	item := types.CloudWorkerLifecycle{
		BillingMode:      types.CloudWorkerOnDemand,
		PackageExpiresAt: now.Add(-time.Hour),
		DeleteAfter:      now.Add(70 * time.Hour),
	}
	got := trialNotice(item, now)
	if !strings.Contains(got, "已暂停") {
		t.Fatalf("expired notice must state the worker is paused, got %q", got)
	}
	if !strings.Contains(got, "释放实例并删除数据") {
		t.Fatalf("expired notice must state the deletion outcome, got %q", got)
	}
}

func TestTrialNoticeSilentForMonthlyWorkers(t *testing.T) {
	item := types.CloudWorkerLifecycle{
		BillingMode:       types.CloudWorkerMonthly,
		ConversionPending: true,
	}
	if got := trialNotice(item, time.Now()); got != "" {
		t.Fatalf("monthly workers must not show trial notices, got %q", got)
	}
}
