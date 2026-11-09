package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type adminRuntimeCredit struct {
	plan, state string
}

type adminRuntimeCreditStore struct {
	configuredCreditStub
	credits                                                   []adminRuntimeCredit
	grantCalls, legacyGrants, legacyReserves, runtimeReserves int
	grantProfile, grantBilling, grantPlan, requestedPlan      string
	grantExpiry                                               *time.Time
	reserveNone                                               bool
	reserveErr                                                error
	wrongPlan                                                 *string
	queued, committed                                         int
}

func newAdminRuntimeCreditStore(plans ...string) *adminRuntimeCreditStore {
	s := &adminRuntimeCreditStore{}
	for _, plan := range plans {
		s.credits = append(s.credits, adminRuntimeCredit{plan: plan, state: "available"})
	}
	return s
}

func (s *adminRuntimeCreditStore) CloudWorkerCreditSummary(int64) (int, int, error) {
	available := 0
	for _, credit := range s.credits {
		if credit.state == "available" {
			available++
		}
	}
	return len(s.credits), available, nil
}

func (s *adminRuntimeCreditStore) GrantCloudWorkerCredits(int64, int, string, *time.Time) (int, error) {
	s.legacyGrants++
	return 1, nil
}

func (s *adminRuntimeCreditStore) CloudWorkerRuntimeCreditSummary(_ int64, profile, billing, plan string) (int, int, error) {
	if profile != types.CloudWorkerPackedShared || billing != types.CloudWorkerMonthly {
		return 0, 0, errors.New("unexpected runtime dimensions")
	}
	available := 0
	for _, credit := range s.credits {
		if credit.state == "available" && adminRuntimeMatches(plan, credit.plan) {
			available++
		}
	}
	return available, available, nil
}

func adminRuntimeMatches(requested, stored string) bool {
	return requested == "" || requested == stored || (requested == types.CloudWorkerRuntimeStandard && stored == "")
}

func (s *adminRuntimeCreditStore) GrantCloudWorkerRuntimeCredits(_ int64, count int, _ string, expiry *time.Time, profile, billing, plan string) (int, error) {
	s.grantCalls++
	s.grantProfile, s.grantBilling, s.grantPlan, s.grantExpiry = profile, billing, plan, expiry
	for i := 0; i < count; i++ {
		s.credits = append(s.credits, adminRuntimeCredit{plan: plan, state: "available"})
	}
	return count, nil
}

func (s *adminRuntimeCreditStore) ReserveCloudWorkerConfiguredCredit(uid int64, ref, profile, billing string) (types.CloudWorkerCreditSelection, bool, error) {
	s.legacyReserves++
	return s.reserve("")
}

func (s *adminRuntimeCreditStore) ReserveCloudWorkerRuntimeCredit(_ int64, _, profile, billing, plan string) (types.CloudWorkerCreditSelection, bool, error) {
	s.runtimeReserves++
	s.requestedPlan = plan
	if profile != types.CloudWorkerPackedShared || billing != types.CloudWorkerMonthly {
		return types.CloudWorkerCreditSelection{}, false, errors.New("unexpected runtime dimensions")
	}
	return s.reserve(plan)
}

func (s *adminRuntimeCreditStore) reserve(plan string) (types.CloudWorkerCreditSelection, bool, error) {
	if s.reserveNone || s.reserveErr != nil {
		return types.CloudWorkerCreditSelection{}, false, s.reserveErr
	}
	for i, credit := range s.credits {
		if credit.state == "available" && adminRuntimeMatches(plan, credit.plan) {
			s.credits[i].state = "reserved"
			selected := credit.plan
			if s.wrongPlan != nil {
				selected = *s.wrongPlan
			}
			return types.CloudWorkerCreditSelection{Profile: types.CloudWorkerPackedShared, BillingMode: types.CloudWorkerMonthly, RuntimePlan: selected, ExpiresAt: s.grantExpiry}, true, nil
		}
	}
	return types.CloudWorkerCreditSelection{}, false, nil
}

func (s *adminRuntimeCreditStore) CommitCloudWorkerCredit(int64, string, int64, string, int) error {
	s.committed++
	for i := range s.credits {
		if s.credits[i].state == "reserved" {
			s.credits[i].state = "consumed"
		}
	}
	return nil
}
func (s *adminRuntimeCreditStore) ReleaseCloudWorkerCredit(int64, string) error {
	for i := range s.credits {
		if s.credits[i].state == "reserved" {
			s.credits[i].state = "available"
		}
	}
	return nil
}
func (s *adminRuntimeCreditStore) QueueCloudWorkerCredit(int64, string) (bool, error) {
	for i := range s.credits {
		if s.credits[i].state == "reserved" {
			s.credits[i].state = "queued"
			s.queued++
			return true, nil
		}
	}
	return false, nil
}
func (s *adminRuntimeCreditStore) CloudWorkerQueuePosition(int64) (int, error) { return 0, nil }
func (s *adminRuntimeCreditStore) ListQueuedCloudWorkerCredits(time.Duration, int) ([]types.CloudWorkerQueuedCredit, error) {
	var items []types.CloudWorkerQueuedCredit
	for _, credit := range s.credits {
		if credit.state == "queued" {
			items = append(items, types.CloudWorkerQueuedCredit{UID: 7, Tier: credit.plan, QueuedAt: time.Now().Add(-time.Hour)})
		}
	}
	return items, nil
}
func (s *adminRuntimeCreditStore) ClaimQueuedCloudWorkerCredit(int64) (bool, error) {
	for i := range s.credits {
		if s.credits[i].state == "queued" {
			s.credits[i].state = "available"
			return true, nil
		}
	}
	return false, nil
}

func TestCloudWorkerRuntimeAdminRejectsInvalidTierWithoutGrant(t *testing.T) {
	for _, endpoint := range []string{"provision", "credits"} {
		for _, tc := range []struct{ profile, plan string }{
			{"packed_shared", "ultra"}, {"private_nat", "pro"}, {"public_ip", "standard"}, {"", "light"},
		} {
			t.Run(endpoint+"/"+tc.profile+"/"+tc.plan, func(t *testing.T) {
				credits := newAdminRuntimeCreditStore("standard")
				body := fmt.Sprintf(`{"uid":7,"count":1,"source_ref":"runtime-invalid","deployment_profile":%q,"runtime_plan":%q}`, tc.profile, tc.plan)
				req := httptest.NewRequest(http.MethodPost, "/internal/"+endpoint, strings.NewReader(body))
				req.RemoteAddr = "127.0.0.1:1234"
				rec := httptest.NewRecorder()
				if endpoint == "provision" {
					h, _ := newCloudWorkerTestHandler("")
					h.credits = credits
					h.HandleAdminProvision(rec, req)
				} else {
					h := NewAccountAdminHandler(nil, nil, nil)
					h.SetCloudWorkerCreditAdmin(credits)
					h.HandleCloudWorkerCredits(rec, req)
				}
				if rec.Code != http.StatusBadRequest || credits.grantCalls != 0 || credits.legacyGrants != 0 || credits.runtimeReserves != 0 {
					t.Fatalf("status=%d grants=%d legacy=%d reserves=%d body=%s", rec.Code, credits.grantCalls, credits.legacyGrants, credits.runtimeReserves, rec.Body.String())
				}
			})
		}
	}
}

func TestCloudWorkerRuntimeManualGrantReturnsNormalizedTier(t *testing.T) {
	credits := newAdminRuntimeCreditStore()
	h := NewAccountAdminHandler(nil, nil, nil)
	h.SetCloudWorkerCreditAdmin(credits)
	req := httptest.NewRequest(http.MethodPost, "/internal/credits", strings.NewReader(`{"uid":7,"count":2,"source_ref":"runtime-manual","deployment_profile":"packed_shared","billing_mode":"ondemand","runtime_plan":" Pro ","expires_at":"2099-01-01T00:00:00Z"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.HandleCloudWorkerCredits(rec, req)
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || payload["runtime_plan"] != "pro" || payload["granted"] != float64(2) || credits.grantPlan != "pro" || credits.grantBilling != "month" || credits.grantProfile != "packed_shared" || credits.grantExpiry == nil || credits.legacyGrants != 0 {
		t.Fatalf("status=%d payload=%v store=%+v", rec.Code, payload, credits)
	}
}

func writeAdminRuntimeScript(t *testing.T, capacityFull bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.log")
	t.Setenv("CATS_TEST_RUNTIME_ARGS_PATH", argsPath)
	script := filepath.Join(dir, "runtime.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CATS_TEST_RUNTIME_ARGS_PATH\"\n"
	if capacityFull {
		body += "echo '[packed] error: tier pro is full on every member'\nexit 75\n"
	}
	if runtime.GOOS == "windows" {
		script = filepath.Join(dir, "runtime.cmd")
		body = "@echo off\r\necho %* >> \"%CATS_TEST_RUNTIME_ARGS_PATH%\"\r\n"
		if capacityFull {
			body += "echo [packed] error: tier pro is full on every member\r\nexit /b 75\r\n"
		}
	}
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return script, argsPath
}

func TestCloudWorkerRuntimeProvisionDoesNotSpendStandardForPro(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("capacity-full=%v", full), func(t *testing.T) {
			script, argsPath := writeAdminRuntimeScript(t, full)
			h, store := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{ProvisionScript: script})
			h.db = &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{}}
			credits := newAdminRuntimeCreditStore("standard")
			h.credits = credits
			req := httptest.NewRequest(http.MethodPost, "/internal/provision", strings.NewReader(`{"uid":7,"username":"bot-runtime","source_ref":"runtime-pro","deployment_profile":"packed_shared","runtime_plan":" pro ","expires_at":"2099-01-01T00:00:00Z"}`))
			rec := httptest.NewRecorder()
			h.HandleAdminProvision(rec, req)
			want := http.StatusCreated
			if full {
				want = http.StatusAccepted
			}
			if rec.Code != want || credits.grantCalls != 1 || credits.grantPlan != "pro" || credits.runtimeReserves != 1 || credits.legacyReserves != 0 || credits.credits[0].state != "available" {
				t.Fatalf("status=%d want=%d store=%+v body=%s", rec.Code, want, credits, rec.Body.String())
			}
			args, err := os.ReadFile(argsPath)
			if err != nil || !strings.Contains(string(args), "--plan pro") {
				t.Fatalf("args=%s err=%v", args, err)
			}
			if full {
				if credits.queued != 1 || credits.credits[1].plan != "pro" || credits.credits[1].state != "queued" || !strings.Contains(rec.Body.String(), `"tier":"pro"`) {
					t.Fatalf("queue lost tier: %+v body=%s", credits, rec.Body.String())
				}
				h.provisionScript, argsPath = writeAdminRuntimeScript(t, false)
				h.retryQueuedCloudWorkers()
				args, err = os.ReadFile(argsPath)
				if err != nil || !strings.Contains(string(args), "--plan pro") || credits.legacyReserves != 0 || credits.committed != 1 || credits.credits[0].state != "available" || credits.credits[1].state != "consumed" {
					t.Fatalf("retry changed tier: args=%s err=%v store=%+v", args, err, credits)
				}
			}
		})
	}
}

func TestCloudWorkerRuntimeStandardConsumesLegacyOrExplicitStandard(t *testing.T) {
	for _, stored := range []string{"", "standard"} {
		t.Run("stored="+stored, func(t *testing.T) {
			h, _ := newCloudWorkerTestHandler("")
			credits := newAdminRuntimeCreditStore(stored)
			h.credits = credits
			rec := httptest.NewRecorder()
			h.HandleAdminProvision(rec, httptest.NewRequest(http.MethodPost, "/internal/provision", strings.NewReader(`{"uid":7,"source_ref":"runtime-standard","deployment_profile":"packed_shared","runtime_plan":"standard"}`)))
			// Reaching the missing-script check proves no expiry/grant was needed,
			// the matching credit was reserved and bot creation was attempted.
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "cloud_worker_provisioning_unconfigured") || credits.grantCalls != 0 || credits.runtimeReserves != 1 || credits.legacyReserves != 0 {
				t.Fatalf("status=%d store=%+v body=%s", rec.Code, credits, rec.Body.String())
			}
		})
	}
}

func TestCloudWorkerRuntimeProvisionRequiresBoundedExpiryForNewTier(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	credits := newAdminRuntimeCreditStore("standard")
	h.credits = credits
	rec := httptest.NewRecorder()
	h.HandleAdminProvision(rec, httptest.NewRequest(http.MethodPost, "/internal/provision", strings.NewReader(`{"uid":7,"source_ref":"runtime-unbounded","deployment_profile":"packed_shared","runtime_plan":"pro"}`)))
	if rec.Code != http.StatusBadRequest || credits.grantCalls != 0 || credits.runtimeReserves != 0 {
		t.Fatalf("status=%d store=%+v body=%s", rec.Code, credits, rec.Body.String())
	}
}

func TestCloudWorkerRuntimeFailsClosedDespiteStaticQuota(t *testing.T) {
	for _, failure := range []string{"unsupported", "no-credit", "reserve-error", "wrong-tier"} {
		t.Run(failure, func(t *testing.T) {
			h, store := newCloudWorkerTestHandler("7=5")
			credits := newAdminRuntimeCreditStore("pro")
			switch failure {
			case "unsupported":
				h.credits = &quotaCreditStub{total: 1, available: 1}
			case "no-credit":
				credits.reserveNone = true
				h.credits = credits
			case "reserve-error":
				credits.reserveErr = errors.New("reservation unavailable")
				h.credits = credits
			case "wrong-tier":
				wrong := "standard"
				credits.wrongPlan = &wrong
				h.credits = credits
			}
			req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{"username": "bot-runtime"})
			ctx := context.WithValue(req.Context(), cloudWorkerProfileContextKey{}, types.CloudWorkerPackedShared)
			ctx = context.WithValue(ctx, cloudWorkerBillingContextKey{}, types.CloudWorkerMonthly)
			ctx = context.WithValue(ctx, cloudWorkerRuntimeContextKey{}, "pro")
			rec := httptest.NewRecorder()
			h.HandleCreate(rec, req.WithContext(ctx))
			want := http.StatusInternalServerError
			if failure == "unsupported" {
				want = http.StatusServiceUnavailable
			}
			if failure == "no-credit" {
				want = http.StatusForbidden
			}
			if rec.Code != want || store.nextUID != 0 || credits.legacyReserves != 0 {
				t.Fatalf("status=%d want=%d created=%d legacy=%d body=%s", rec.Code, want, store.nextUID, credits.legacyReserves, rec.Body.String())
			}
		})
	}
}

func TestCloudWorkerRuntimePublicBodyCannotSelectTier(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	credits := newAdminRuntimeCreditStore("standard", "pro")
	h.credits = credits
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{"username": "bot-runtime", "runtime_plan": "pro"}))
	if credits.runtimeReserves != 0 || credits.legacyReserves != 1 || credits.requestedPlan != "" || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("public tier selection leaked: store=%+v status=%d body=%s", credits, rec.Code, rec.Body.String())
	}
}

func TestCloudWorkerRuntimeAdminBlankPreservesLegacyStores(t *testing.T) {
	for _, endpoint := range []string{"provision", "credits"} {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/internal/"+endpoint, strings.NewReader(`{"uid":7,"count":1,"source_ref":"runtime-blank","runtime_plan":"   "}`))
			req.RemoteAddr = "127.0.0.1:1234"
			rec := httptest.NewRecorder()
			if endpoint == "credits" {
				store := &cloudWorkerCreditAdminTestStore{}
				h := NewAccountAdminHandler(nil, nil, nil)
				h.SetCloudWorkerCreditAdmin(store)
				h.HandleCloudWorkerCredits(rec, req)
				if rec.Code != http.StatusOK || store.count != 1 {
					t.Fatalf("legacy grant failed: %d %s", rec.Code, rec.Body.String())
				}
			} else {
				h, _ := newCloudWorkerTestHandler("")
				credits := &configuredCreditStub{profileCreditStub: profileCreditStub{quotaCreditStub: quotaCreditStub{total: 1, available: 1}, profile: types.CloudWorkerPrivateNAT}, billing: types.CloudWorkerMonthly}
				h.credits = credits
				h.HandleAdminProvision(rec, req)
				if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "cloud_worker_provisioning_unconfigured") {
					t.Fatalf("legacy provision failed: %d %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}

func (s *adminRuntimeCreditStore) ClaimQueuedCloudWorkerRuntimeCredit(_ int64, plan string) (bool, error) {
	for i := range s.credits {
		if s.credits[i].state == "queued" && adminRuntimeMatches(plan, s.credits[i].plan) {
			s.credits[i].state = "available"
			return true, nil
		}
	}
	return false, nil
}

func TestCloudWorkerRuntimeQueueRetriesEachTierForSameOwner(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("capacity-full=%v", full), func(t *testing.T) {
			script, argsPath := writeAdminRuntimeScript(t, full)
			h, store := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{ProvisionScript: script})
			h.db = &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{}}
			credits := newAdminRuntimeCreditStore("standard", "pro", "heavy")
			credits.credits[1].state, credits.credits[2].state = "queued", "queued"
			h.credits = credits
			h.retryQueuedCloudWorkers()
			args, err := os.ReadFile(argsPath)
			if err != nil || !strings.Contains(string(args), "--plan pro") || !strings.Contains(string(args), "--plan heavy") || credits.legacyReserves != 0 || credits.credits[0].state != "available" {
				t.Fatalf("retry lost a tier: args=%s err=%v store=%+v", args, err, credits)
			}
			wantState := "consumed"
			if full {
				wantState = "queued"
			}
			if credits.credits[1].state != wantState || credits.credits[2].state != wantState {
				t.Fatalf("retry released a different tier: want=%s credits=%+v", wantState, credits.credits)
			}
		})
	}
}

func TestCloudWorkerRuntimeQueueKeepsLegacyStandardTier(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("capacity-full=%v", full), func(t *testing.T) {
			script, argsPath := writeAdminRuntimeScript(t, full)
			h, store := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{ProvisionScript: script})
			h.db = &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{}}
			credits := newAdminRuntimeCreditStore("pro", "")
			credits.credits[1].state = "queued"
			h.credits = credits
			h.retryQueuedCloudWorkers()
			args, err := os.ReadFile(argsPath)
			if err != nil || strings.Contains(string(args), "--plan") || credits.requestedPlan != "standard" || credits.runtimeReserves != 1 || credits.legacyReserves != 0 || credits.credits[0].state != "available" {
				t.Fatalf("legacy standard retry changed tier: args=%s err=%v store=%+v", args, err, credits)
			}
			wantState := "consumed"
			if full {
				wantState = "queued"
			}
			if credits.credits[1].plan != "" || credits.credits[1].state != wantState {
				t.Fatalf("legacy credit changed: want state=%s credit=%+v", wantState, credits.credits[1])
			}
		})
	}
}

type legacyAdminRuntimeCreditStore struct {
	quotaCreditStub
	cloudWorkerCreditAdminTestStore
}

func TestCloudWorkerRuntimeAdminRequiresRuntimeStore(t *testing.T) {
	for _, endpoint := range []string{"provision", "credits"} {
		t.Run(endpoint, func(t *testing.T) {
			store := &legacyAdminRuntimeCreditStore{quotaCreditStub: quotaCreditStub{total: 1, available: 1}}
			req := httptest.NewRequest(http.MethodPost, "/internal/"+endpoint, strings.NewReader(`{"uid":7,"count":1,"source_ref":"runtime-unsupported","deployment_profile":"packed_shared","runtime_plan":"pro","expires_at":"2099-01-01T00:00:00Z"}`))
			req.RemoteAddr = "127.0.0.1:1234"
			rec := httptest.NewRecorder()
			if endpoint == "provision" {
				h, _ := newCloudWorkerTestHandler("7=5")
				h.credits = store
				h.HandleAdminProvision(rec, req)
			} else {
				h := NewAccountAdminHandler(nil, nil, nil)
				h.SetCloudWorkerCreditAdmin(store)
				h.HandleCloudWorkerCredits(rec, req)
			}
			if rec.Code != http.StatusServiceUnavailable || store.count != 0 {
				t.Fatalf("status=%d grant count=%d body=%s", rec.Code, store.count, rec.Body.String())
			}
		})
	}
}
