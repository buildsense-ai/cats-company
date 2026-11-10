package types

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// legacyCommercialActionRequest is the request shape that existed before
// WorkerExpiryMode was added.
//
// Replaying a journaled operation compares fingerprints, so a request written
// without the new field must hash exactly as it did before the field existed.
// Changing that would answer every in-flight retry with "operation_id already
// belongs to different parameters" and force an operator to open a new
// operation for work that was already committed.
type legacyCommercialActionRequest struct {
	UID               int64      `json:"uid"`
	Intent            string     `json:"intent"`
	PlanID            int64      `json:"plan_id"`
	DurationDays      int        `json:"duration_days"`
	WorkerAction      string     `json:"worker_action"`
	DeploymentProfile string     `json:"deployment_profile"`
	BillingMode       string     `json:"billing_mode"`
	RuntimePlan       string     `json:"runtime_plan"`
	WorkerExpiresAt   *time.Time `json:"worker_expires_at"`
	Note              string     `json:"note"`
	OperationID       string     `json:"operation_id"`
	Preview           bool       `json:"preview"`
	PreviewToken      string     `json:"preview_token"`
}

func TestCommercialActionFingerprintUnchangedWithoutExpiryMode(t *testing.T) {
	expiry := time.Date(2026, time.December, 1, 1, 2, 3, 0, time.UTC)
	request := CommercialActionRequest{
		UID:               7,
		Intent:            "ensure_worker",
		DurationDays:      30,
		WorkerAction:      "ensure",
		DeploymentProfile: CloudWorkerPackedShared,
		BillingMode:       CloudWorkerMonthly,
		RuntimePlan:       CloudWorkerRuntimeLight,
		WorkerExpiresAt:   &expiry,
		Note:              "operator note",
	}
	// Fingerprint deliberately clears the transport-only fields, so the legacy
	// shape is compared with the same values it would have hashed.
	legacy := legacyCommercialActionRequest{
		UID:               request.UID,
		Intent:            request.Intent,
		PlanID:            request.PlanID,
		DurationDays:      request.DurationDays,
		WorkerAction:      request.WorkerAction,
		DeploymentProfile: request.DeploymentProfile,
		BillingMode:       request.BillingMode,
		RuntimePlan:       request.RuntimePlan,
		WorkerExpiresAt:   request.WorkerExpiresAt,
		Note:              request.Note,
	}
	if got, want := request.Fingerprint(), CommercialActionHash(legacy); got != want {
		t.Fatalf("legacy request fingerprint changed: got %s want %s", got, want)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "worker_expiry_mode") {
		t.Fatalf("an empty mode must not appear in the durable request: %s", encoded)
	}
}

func TestCommercialActionNormalizeWorkerExpiryMode(t *testing.T) {
	base := func() CommercialActionRequest {
		return CommercialActionRequest{
			UID:               7,
			Intent:            "ensure_worker",
			WorkerAction:      "ensure",
			DeploymentProfile: CloudWorkerPackedShared,
			BillingMode:       CloudWorkerMonthly,
			Preview:           true,
		}
	}
	cases := []struct {
		name    string
		mutate  func(*CommercialActionRequest)
		wantErr bool
	}{
		{
			name:   "permanent shared worker needs no expiry",
			mutate: func(r *CommercialActionRequest) { r.WorkerExpiryMode = CloudWorkerExpiryPermanent },
		},
		{
			name: "follow_plan shared worker needs no expiry",
			mutate: func(r *CommercialActionRequest) {
				r.WorkerExpiryMode = CloudWorkerExpiryFollowPlan
			},
		},
		{
			name: "fixed keeps the existing duration rule",
			mutate: func(r *CommercialActionRequest) {
				r.WorkerExpiryMode = CloudWorkerExpiryFixed
				r.DurationDays = 30
			},
		},
		{
			name: "permanent is rejected on a dedicated ECS profile",
			mutate: func(r *CommercialActionRequest) {
				r.WorkerExpiryMode = CloudWorkerExpiryPermanent
				r.DeploymentProfile = CloudWorkerPublicIP
			},
			wantErr: true,
		},
		{
			name: "follow_plan is rejected on a dedicated ECS profile",
			mutate: func(r *CommercialActionRequest) {
				r.WorkerExpiryMode = CloudWorkerExpiryFollowPlan
				r.DeploymentProfile = CloudWorkerPrivateNAT
			},
			wantErr: true,
		},
		{
			name: "permanent cannot carry a duration",
			mutate: func(r *CommercialActionRequest) {
				r.WorkerExpiryMode = CloudWorkerExpiryPermanent
				r.DurationDays = 10
			},
			wantErr: true,
		},
		{
			name: "follow_plan cannot carry an explicit expiry",
			mutate: func(r *CommercialActionRequest) {
				r.WorkerExpiryMode = CloudWorkerExpiryFollowPlan
				expiry := time.Now().Add(24 * time.Hour)
				r.WorkerExpiresAt = &expiry
			},
			wantErr: true,
		},
		{
			name: "unknown mode is rejected",
			mutate: func(r *CommercialActionRequest) {
				r.WorkerExpiryMode = "eternal"
			},
			wantErr: true,
		},
		{
			name: "legacy is not a requestable mode",
			mutate: func(r *CommercialActionRequest) {
				r.WorkerExpiryMode = CloudWorkerExpiryLegacy
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := base()
			tc.mutate(&request)
			err := request.Normalize()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestCommercialActionNormalizeKeepsEmptyExpiryModeEmpty(t *testing.T) {
	request := CommercialActionRequest{
		UID:               7,
		Intent:            "ensure_worker",
		WorkerAction:      "ensure",
		DeploymentProfile: CloudWorkerPackedShared,
		DurationDays:      30,
		Preview:           true,
	}
	if err := request.Normalize(); err != nil {
		t.Fatal(err)
	}
	// Normalising an old request must not turn it into a new term: the caller
	// (and the row it later writes) has to keep reading it as "historical".
	if request.WorkerExpiryMode != "" {
		t.Fatalf("empty mode was rewritten to %q", request.WorkerExpiryMode)
	}
}

func TestCommercialActionNormalizeTestWorkerOnlyFixed(t *testing.T) {
	fixed := CommercialActionRequest{UID: 7, Intent: "test_worker", DurationDays: 30, WorkerExpiryMode: CloudWorkerExpiryFixed, Preview: true}
	if err := fixed.Normalize(); err != nil {
		t.Fatalf("fixed test worker rejected: %v", err)
	}
	permanent := CommercialActionRequest{UID: 7, Intent: "test_worker", DurationDays: 30, WorkerExpiryMode: CloudWorkerExpiryPermanent, Preview: true}
	if err := permanent.Normalize(); err == nil {
		t.Fatal("a trial worker cannot be permanent")
	}
	outOfRange := CommercialActionRequest{UID: 7, Intent: "test_worker", DurationDays: 91, WorkerExpiryMode: CloudWorkerExpiryFixed, Preview: true}
	if err := outOfRange.Normalize(); err == nil {
		t.Fatal("the 1..90 trial window must stay bounded")
	}
}

func TestCommercialActionNormalizeRejectsModeWithoutWorker(t *testing.T) {
	request := CommercialActionRequest{UID: 7, Intent: "renew_plan", WorkerExpiryMode: CloudWorkerExpiryPermanent, Preview: true}
	if err := request.Normalize(); err == nil {
		t.Fatal("worker_action=none must not carry a term mode")
	}
}

func TestNormalizeCloudWorkerExpiryModeAcceptsOnlyRequestableModes(t *testing.T) {
	for _, mode := range []string{CloudWorkerExpiryFixed, CloudWorkerExpiryFollowPlan, CloudWorkerExpiryPermanent, ""} {
		if _, ok := NormalizeCloudWorkerExpiryMode(mode); !ok {
			t.Fatalf("%q should be accepted", mode)
		}
	}
	if _, ok := NormalizeCloudWorkerExpiryMode(CloudWorkerExpiryLegacy); ok {
		t.Fatal("legacy is a stored value, never a request")
	}
	if CloudWorkerExpiryModeOrDefault("") != CloudWorkerExpiryLegacy {
		t.Fatal("an empty request mode must persist as legacy")
	}
	if !CloudWorkerExpiryModeRequiresShared(CloudWorkerExpiryPermanent) || CloudWorkerExpiryModeRequiresShared(CloudWorkerExpiryFixed) {
		t.Fatal("only follow_plan/permanent are shared-only terms")
	}
	if !CloudWorkerExpiryModeIndependentOfPlan(CloudWorkerExpiryFixed) || !CloudWorkerExpiryModeIndependentOfPlan(CloudWorkerExpiryPermanent) {
		t.Fatal("a package must not reschedule a fixed term or a permanent grant")
	}
	if CloudWorkerExpiryModeIndependentOfPlan(CloudWorkerExpiryFollowPlan) || CloudWorkerExpiryModeIndependentOfPlan(CloudWorkerExpiryLegacy) {
		t.Fatal("package-driven terms must follow the plan")
	}
}
