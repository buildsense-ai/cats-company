package server

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type sharedTermPackageStore struct {
	*deploymentTestStore
	summary *types.CommercialSummary
}

func (s *sharedTermPackageStore) GetCommercialSummary(int64) (*types.CommercialSummary, error) {
	return s.summary, nil
}

func (s *sharedTermPackageStore) GetSharedCloudWorkerPackageTerm(int64) (*types.CloudWorkerPackageTerm, error) {
	return s.summary.CloudWorkerPackageTerm, nil
}

func TestSharedTermFollowsActualMachinePackage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script contract")
	}
	now := time.Now().UTC()
	packageDeadline := now.Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	modelDeadline := now.Add(90 * 24 * time.Hour)
	for _, tc := range []struct {
		name          string
		packageExpiry *time.Time
		existing      time.Time
		expected      *time.Time
	}{
		{"finite", &packageDeadline, time.Time{}, &packageDeadline},
		{"permanent", nil, time.Time{}, nil},
		{"preserve fixed remainder", &packageDeadline, packageDeadline.Add(24 * time.Hour), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s := newSharedTermHandler(t)
			s.row.PackageExpiresAt = tc.existing
			h.db = &sharedTermPackageStore{deploymentTestStore: h.db.(*deploymentTestStore), summary: &types.CommercialSummary{
				// The model plan alone says nothing about invite-supplied machine rights.
				Entitlements:           []*types.CommercialEntitlement{{PlanID: 2, State: "active", Source: "invite", StartsAt: now.Add(-time.Hour), ExpiresAt: &modelDeadline}},
				Plans:                  []*types.CommercialPlan{{ID: 2, DurationDays: 90, CloudWorkerIncluded: false, CloudWorkerRetentionDays: 7}},
				CloudWorkerPackageTerm: &types.CloudWorkerPackageTerm{EntitlementID: 8, PlanID: 2, ExpiresAt: tc.packageExpiry, RetentionDays: 9},
			}}
			want := tc.expected
			if tc.name == "preserve fixed remainder" {
				want = &tc.existing
			}
			result := `{"status":"renewed","expires_at":null,"permanent":true}`
			if want != nil {
				result = `{"status":"renewed","expires_at":"` + want.Format(time.RFC3339) + `"}`
			}
			writeSharedTermScript(t, h.renewScript, result, 0)
			w := sharedTermRequest(h, "set_term", "follow_plan", nil)
			if w.Code != http.StatusOK || s.row.ExpiryMode != "follow_plan" || s.row.RetentionDays != 9 || s.row.SyncPending {
				t.Fatalf("status=%d %s row=%+v", w.Code, w.Body, s.row)
			}
			if want == nil {
				if !s.row.PackageExpiresAt.IsZero() {
					t.Fatal("permanent package kept a deadline")
				}
			} else if !s.row.PackageExpiresAt.Equal(*want) {
				t.Fatal("package deadline/remainder lost")
			}
		})
	}
}

func TestSharedTermRejectsPackagesWithoutMachineRights(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, source string
		includes     bool
		missingPlan  bool
	}{
		{"free baseline", "free", true, false},
		{"legacy baseline", "legacy", true, false},
		{"model-only package", "order", false, false},
		{"unknown plan", "invite", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s := newSharedTermHandler(t)
			summary := &types.CommercialSummary{
				Entitlements: []*types.CommercialEntitlement{{PlanID: 2, State: "active", Source: tc.source, StartsAt: now.Add(-time.Hour)}},
			}
			if !tc.missingPlan {
				summary.Plans = []*types.CommercialPlan{{ID: 2, CloudWorkerIncluded: tc.includes}}
			}
			h.db = &sharedTermPackageStore{deploymentTestStore: h.db.(*deploymentTestStore), summary: summary}
			w := sharedTermRequest(h, "set_term", "follow_plan", nil)
			if w.Code != http.StatusConflict || s.sets != 0 || s.claims != 0 || !strings.Contains(w.Body.String(), "cloud_worker_package_required") {
				t.Fatalf("package without rights changed term: %d %s sets=%d claims=%d", w.Code, w.Body, s.sets, s.claims)
			}
		})
	}
}

type sharedTermSweepStub struct {
	*sharedTermStub
	expiryClaim bool
	expiryNote  string
}

func (s *sharedTermSweepStub) ListCloudWorkerLifecycleDue(time.Time, int) ([]CloudWorkerLifecycle, error) {
	return []CloudWorkerLifecycle{s.row}, nil
}
func (s *sharedTermSweepStub) ClaimSharedCloudWorkerExpiry(int64, time.Time) (bool, error) {
	return s.expiryClaim, nil
}
func (s *sharedTermSweepStub) CompleteSharedCloudWorkerExpiry(_ int64, _ time.Time, note string) error {
	s.expiryNote = note
	if note == "" {
		s.row.State = "delete_pending"
	}
	return nil
}
func (s *sharedTermSweepStub) ClaimCloudWorkerLifecycleDeletion(int64) (bool, error) {
	s.row.State = "delete_running"
	return true, nil
}
func (s *sharedTermSweepStub) MarkCloudWorkerLifecycleDeleted(_ int64, note string) error {
	s.errorText = note
	s.row.State = "deleted"
	if note != "" {
		s.row.State = "delete_failed"
	}
	return nil
}

func TestSharedTermSweepKeepsWorkerWhenClusterSkipsOldOperation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script contract")
	}
	for _, state := range []string{"active", "delete_pending"} {
		t.Run(state, func(t *testing.T) {
			h, base := newSharedTermHandler(t)
			h.renewScript = ""
			script := filepath.Join(t.TempDir(), "skip.sh")
			// Verify guarded actions carry the expected old deadline.
			body := "#!/bin/sh\ncase \"$*\" in *--expected-expires-at*) ;; *) exit 19;; esac\nprintf '%s\\n' '{\"status\":\"skipped\"}'\n"
			if err := os.WriteFile(script, []byte(body), 0755); err != nil {
				t.Fatal(err)
			}
			h.expireScript = script
			h.destroyScript = script
			base.row.State = state
			base.row.PackageExpiresAt = time.Now().UTC().Add(-8 * 24 * time.Hour)
			base.row.DeleteAfter = time.Now().UTC().Add(-24 * time.Hour)
			s := &sharedTermSweepStub{sharedTermStub: base, expiryClaim: true}
			h.credits = s
			h.SweepExpiredWorkers(time.Now().UTC())
			if state == "active" {
				if s.row.State != "active" || !strings.Contains(s.expiryNote, "跳过") {
					t.Fatalf("old expiry archived worker: %+v note=%q", s.row, s.expiryNote)
				}
			} else if s.row.State != "delete_failed" || s.errorText == "" {
				t.Fatalf("skipped deletion marked deleted: %+v note=%q", s.row, s.errorText)
			}
			db := h.db.(*deploymentTestStore).cloudWorkerTestStore
			if len(db.deletedBots) != 0 {
				t.Fatalf("skipped hook removed bot: %v", db.deletedBots)
			}
		})
	}
}
