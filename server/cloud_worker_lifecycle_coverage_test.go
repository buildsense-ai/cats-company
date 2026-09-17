package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestCloudWorkerProvisionCyclesCoverPaidWindow(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		paidUntil time.Time
		want      int
	}{
		{name: "no paid window", paidUntil: time.Time{}, want: 1},
		{name: "twenty days", paidUntil: now.Add(20 * 24 * time.Hour), want: 1},
		{name: "one hour short of a month", paidUntil: now.Add(30*24*time.Hour - time.Hour), want: 1},
		{name: "a whole month", paidUntil: now.AddDate(0, 0, 30), want: 1},
		{name: "two-month package remnant", paidUntil: now.Add(47 * 24 * time.Hour), want: 2},
		{name: "ninety days", paidUntil: now.AddDate(0, 0, 90), want: 3},
		{name: "capped at the provider contract", paidUntil: now.AddDate(0, 0, 3650), want: 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cloudWorkerProvisionCycles(now, tc.paidUntil); got != tc.want {
				t.Fatalf("cycles=%d want %d", got, tc.want)
			}
		})
	}
}

func TestCloudWorkerCreatePrepaysMonthsCoveringReservedCredit(t *testing.T) {
	cfg := workerScriptCfg(t, "", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, store := newCloudWorkerTestHandlerCfg(cfg)
	db := &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{}}
	h.db = db
	expiresAt := time.Now().UTC().Add(90 * 24 * time.Hour)
	credits := &configuredCreditStub{
		profileCreditStub: profileCreditStub{quotaCreditStub: quotaCreditStub{total: 1, available: 1}, profile: "private_nat"},
		billing:           "month",
		expiresAt:         &expiresAt,
	}
	h.credits = credits

	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{"username": "bot-x"})
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	assertWorkerDeploymentEnv(t, db.deployments, "CTYUN_WORKER_CYCLE_COUNT", "3")
	assertWorkerDeploymentEnv(t, db.deployments, "CTYUN_WORKER_BILLING_MODE", "month")
}

func TestCloudWorkerCreateRegistersLifecycleForStaticQuotaWorker(t *testing.T) {
	cfg := workerScriptCfg(t, "38=1", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, store := newCloudWorkerTestHandlerCfg(cfg)
	packageExpiresAt := time.Now().UTC().Add(45 * 24 * time.Hour)
	db := &packageStoreTestDouble{
		deploymentTestStore: &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{}},
		packageExpiresAt:    packageExpiresAt,
	}
	h.db = db
	credits := &lifecycleCaptureStub{}
	h.credits = credits

	req := cloudWorkerRequest(38, http.MethodPost, "/api/cloud-workers", map[string]string{"username": "bot-jack", "display_name": "杰克"})
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	if len(credits.registrations) != 1 {
		t.Fatalf("registrations=%+v", credits.registrations)
	}
	registration := credits.registrations[0]
	if registration.ownerUID != 38 || registration.workerUID <= 0 || registration.tenantName != "bot-bot-jack" {
		t.Fatalf("registration=%+v", registration)
	}
	if !registration.expiresAt.Equal(packageExpiresAt) || registration.billingMode != "month" || registration.graceDays != cloudWorkerExpiryGraceDays {
		t.Fatalf("registration=%+v", registration)
	}
	// The static path carries no credit, so the worker prepays the months the
	// owner's package still covers (45 days -> 2 cycles).
	assertWorkerDeploymentEnv(t, db.deployments, "CTYUN_WORKER_CYCLE_COUNT", "2")
}

func TestCloudWorkerCreateRegistersPackageLifecycleForPerpetualCredit(t *testing.T) {
	cfg := workerScriptCfg(t, "", map[string]string{"provision": writeWorkerOpScript(t, "ok")})
	if cfg.ProvisionScript == "" {
		t.Skip("no POSIX shell")
	}
	h, store := newCloudWorkerTestHandlerCfg(cfg)
	packageExpiresAt := time.Now().UTC().Add(30 * 24 * time.Hour)
	db := &packageStoreTestDouble{
		deploymentTestStore: &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{}},
		packageExpiresAt:    packageExpiresAt,
	}
	h.db = db
	credits := &perpetualConfiguredCreditStub{lifecycleCaptureStub: lifecycleCaptureStub{quotaCreditStub: quotaCreditStub{total: 1, available: 1}}}
	h.credits = credits

	req := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{"username": "bot-monica"})
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	if len(credits.registrations) != 1 {
		t.Fatalf("registrations=%+v", credits.registrations)
	}
	registration := credits.registrations[0]
	if registration.ownerUID != 7 || registration.workerUID <= 0 || registration.tenantName != "bot-bot-monica" {
		t.Fatalf("registration=%+v", registration)
	}
	if !registration.expiresAt.Equal(packageExpiresAt) || registration.graceDays != cloudWorkerExpiryGraceDays {
		t.Fatalf("registration=%+v", registration)
	}
}

func TestCloudWorkerFallbackLifecycleDefaults(t *testing.T) {
	h, _ := newCloudWorkerTestHandler("")
	credits := &lifecycleCaptureStub{}
	h.credits = credits
	before := time.Now().UTC()
	h.registerFallbackCloudWorkerLifecycle(38, 101, "bot-bot-jack", types.CloudWorkerMonthly)
	if len(credits.registrations) != 1 {
		t.Fatalf("registrations=%+v", credits.registrations)
	}
	got := credits.registrations[0]
	// The owner has no reachable package store in this handler, so the worker
	// gets the bounded 30-day default instead of an eternal orphan row.
	if got.expiresAt.Before(before.AddDate(0, 0, 30)) || got.expiresAt.After(time.Now().UTC().AddDate(0, 0, 30).Add(time.Minute)) {
		t.Fatalf("fallback expiry=%s want ~now+30d", got.expiresAt)
	}
	if got.graceDays != cloudWorkerExpiryGraceDays || got.billingMode != types.CloudWorkerMonthly {
		t.Fatalf("registration=%+v", got)
	}

	credits = &lifecycleCaptureStub{}
	h.credits = credits
	h.registerFallbackCloudWorkerLifecycle(7, 102, "bot-trial", types.CloudWorkerOnDemand)
	if got := credits.registrations[0]; got.graceDays != 3 || got.billingMode != types.CloudWorkerOnDemand {
		t.Fatalf("trial fallback registration=%+v", got)
	}
}

func TestParseCloudWorkerRenewalResultKeepsAutoRenewSignalWithoutExpiry(t *testing.T) {
	result, err := parseCloudWorkerRenewalResult(`{"expires_at":"","auto_renew_disabled":false}`)
	if err == nil {
		t.Fatal("an empty expiry must not parse as a renewal result")
	}
	if result.AutoRenewDisabled == nil || *result.AutoRenewDisabled {
		t.Fatalf("auto-renew signal lost on malformed expiry: %v", result.AutoRenewDisabled)
	}
}

func assertWorkerDeploymentEnv(t *testing.T, deployments map[string]types.CloudWorkerDeployment, key, want string) {
	t.Helper()
	if len(deployments) != 1 {
		t.Fatalf("deployments=%v", deployments)
	}
	for _, deployment := range deployments {
		if got := deployment.Env[key]; got != want {
			t.Fatalf("%s=%q want %q", key, got, want)
		}
	}
}

type lifecycleRegistration struct {
	workerUID   int64
	ownerUID    int64
	tenantName  string
	expiresAt   time.Time
	billingMode string
	graceDays   int
}

type lifecycleCaptureStub struct {
	quotaCreditStub
	registrations []lifecycleRegistration
}

func (s *lifecycleCaptureStub) RegisterCloudWorkerLifecycle(workerUID, ownerUID int64, tenantName string, packageExpiresAt time.Time, billingMode string, graceDays int) error {
	s.registrations = append(s.registrations, lifecycleRegistration{
		workerUID:   workerUID,
		ownerUID:    ownerUID,
		tenantName:  tenantName,
		expiresAt:   packageExpiresAt,
		billingMode: billingMode,
		graceDays:   graceDays,
	})
	return nil
}

// perpetualConfiguredCreditStub reserves a configured credit with no expiry
// (the legacy perpetual manual grant) while capturing the fallback lifecycle
// registrations the create path must produce for it.
type perpetualConfiguredCreditStub struct {
	lifecycleCaptureStub
}

func (s *perpetualConfiguredCreditStub) CloudWorkerConfiguredCreditSummary(int64, string, string) (int, int, error) {
	return 1, 1, nil
}
func (s *perpetualConfiguredCreditStub) GrantCloudWorkerConfiguredCredits(int64, int, string, *time.Time, string, string) (int, error) {
	return 1, nil
}
func (s *perpetualConfiguredCreditStub) ReserveCloudWorkerConfiguredCredit(int64, string, string, string) (types.CloudWorkerCreditSelection, bool, error) {
	return types.CloudWorkerCreditSelection{Profile: "private_nat", BillingMode: "month"}, true, nil
}

// packageStoreTestDouble exposes an active package expiry through the
// CommercialStore surface the create path consults when no bounded credit is
// available.
type packageStoreTestDouble struct {
	*deploymentTestStore
	packageExpiresAt time.Time
}

func (s *packageStoreTestDouble) GetCommercialSummary(uid int64) (*types.CommercialSummary, error) {
	expires := s.packageExpiresAt
	return &types.CommercialSummary{
		UID:          uid,
		Entitlements: []*types.CommercialEntitlement{{UID: uid, State: "active", ExpiresAt: &expires}},
	}, nil
}

func (s *packageStoreTestDouble) ListCommercialPlans(bool) ([]*types.CommercialPlan, error) {
	return nil, nil
}
func (s *packageStoreTestDouble) CreateCommercialPlan(*types.CommercialPlan) (int64, error) {
	return 0, errors.New("not implemented")
}
func (s *packageStoreTestDouble) ListCommercialInviteCodes(int) ([]*types.CommercialInviteCode, error) {
	return nil, nil
}
func (s *packageStoreTestDouble) CreateCommercialInviteCode(*types.CommercialInviteCode) (int64, error) {
	return 0, errors.New("not implemented")
}
func (s *packageStoreTestDouble) GrantCommercialQuota(*types.CommercialQuotaGrant) (*types.CommercialQuotaGrant, error) {
	return nil, errors.New("not implemented")
}
func (s *packageStoreTestDouble) RedeemCommercialInvite(int64, string) (*types.CommercialSummary, error) {
	return nil, errors.New("not implemented")
}
