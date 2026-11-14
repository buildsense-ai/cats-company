package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// CommercialActionRequest is the complete operator decision. Preview controls
// transport only; all business fields participate in the durable fingerprint.
type CommercialActionRequest struct {
	UID               int64  `json:"uid"`
	Intent            string `json:"intent"`
	PlanID            int64  `json:"plan_id"`
	DurationDays      int    `json:"duration_days"`
	WorkerAction      string `json:"worker_action"`
	DeploymentProfile string `json:"deployment_profile"`
	BillingMode       string `json:"billing_mode"`
	RuntimePlan       string `json:"runtime_plan"`
	// WorkerExpiryMode is the term an operator explicitly asked for:
	// fixed / follow_plan / permanent. It is omitempty because a request
	// written before this field existed must keep its durable fingerprint
	// and its original term semantics (the row default is legacy).
	WorkerExpiryMode string     `json:"worker_expiry_mode,omitempty"`
	WorkerExpiresAt  *time.Time `json:"worker_expires_at"`
	Note             string     `json:"note"`
	OperationID      string     `json:"operation_id"`
	Preview          bool       `json:"preview"`
	PreviewToken     string     `json:"preview_token"`
}

var commercialActionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,95}$`)

func ValidCommercialActionID(s string) bool { return commercialActionID.MatchString(s) }
func (r *CommercialActionRequest) Normalize() error {
	bad := func(s string) error { return &CommercialAdjustmentError{Code: "invalid_request", Message: s} }
	r.Intent = strings.ToLower(strings.TrimSpace(r.Intent))
	r.WorkerAction = strings.ToLower(strings.TrimSpace(r.WorkerAction))
	r.Note = strings.TrimSpace(r.Note)
	r.OperationID = strings.TrimSpace(r.OperationID)
	r.DeploymentProfile = strings.ToLower(strings.TrimSpace(r.DeploymentProfile))
	r.BillingMode = strings.ToLower(strings.TrimSpace(r.BillingMode))
	r.RuntimePlan = strings.ToLower(strings.TrimSpace(r.RuntimePlan))
	// The term mode stays empty when the request did not send one: a rewrite
	// would change the durable fingerprint and re-term existing workers.
	expiryMode, validExpiryMode := NormalizeCloudWorkerExpiryMode(r.WorkerExpiryMode)
	if !validExpiryMode {
		return bad("invalid worker_expiry_mode (fixed, follow_plan or permanent)")
	}
	r.WorkerExpiryMode = expiryMode
	if r.UID <= 0 {
		return bad("uid is required")
	}
	if r.DurationDays < 0 || r.DurationDays > 3650 {
		return bad("duration_days must be 1..3650 when specified")
	}
	switch r.Intent {
	case "grant_plan", "renew_plan":
		if r.Intent == "grant_plan" && r.PlanID <= 0 {
			return bad("plan_id is required")
		}
		if r.Intent == "grant_plan" && r.DurationDays != 0 {
			return bad("duration_days is only supported for renewal and worker intents")
		}
		if r.WorkerAction == "" {
			r.WorkerAction = "none"
		}
		if r.WorkerAction != "none" && r.WorkerAction != "ensure" && r.WorkerAction != "additional" {
			return bad("invalid worker_action")
		}
		if r.DeploymentProfile != "" || r.BillingMode != "" || r.RuntimePlan != "" {
			return bad("package worker configuration is derived from the plan")
		}
	case "test_worker", "ensure_worker", "add_worker":
		expected := "additional"
		if r.Intent == "ensure_worker" {
			expected = "ensure"
		}
		if r.WorkerAction != "" && r.WorkerAction != expected {
			return bad("worker_action conflicts with intent")
		}
		r.WorkerAction = expected
		if r.PlanID != 0 {
			return bad("plan_id is only supported for package intents")
		}
		if r.Intent == "test_worker" && (r.DurationDays < 1 || r.DurationDays > 90) {
			return bad("test duration_days must be 1..90")
		}
		var ok bool
		r.DeploymentProfile, ok = NormalizeCloudWorkerProfile(r.DeploymentProfile)
		if !ok {
			return bad("invalid deployment_profile")
		}
		r.BillingMode, ok = NormalizeCloudWorkerBilling(r.BillingMode)
		if !ok {
			return bad("invalid billing_mode")
		}
		r.BillingMode = CloudWorkerBillingForProfile(r.DeploymentProfile, r.BillingMode)
		r.RuntimePlan, ok = NormalizeCloudWorkerRuntimePlan(r.RuntimePlan)
		if !ok || (r.RuntimePlan != "" && r.DeploymentProfile != CloudWorkerPackedShared) {
			return bad("invalid runtime_plan")
		}
		if r.DeploymentProfile == CloudWorkerPackedShared && r.RuntimePlan == "" {
			r.RuntimePlan = CloudWorkerRuntimeStandard
		}
		// follow_plan and permanent are terms only the shared container fleet
		// can honour: a dedicated ECS instance would have to buy a cloud
		// subscription for either of them, which is exactly what an operator
		// must not trigger by accident.
		if CloudWorkerExpiryModeRequiresShared(r.WorkerExpiryMode) && r.DeploymentProfile != CloudWorkerPackedShared {
			return bad("worker_expiry_mode follow_plan/permanent requires deployment_profile=packed_shared")
		}
		if r.Intent == "test_worker" && r.WorkerExpiryMode != "" && r.WorkerExpiryMode != CloudWorkerExpiryFixed {
			return bad("test_worker only supports worker_expiry_mode=fixed")
		}
	default:
		return bad("unsupported intent")
	}
	// No worker, no term: a mode only describes what an actual machine's
	// expiry follows, so it cannot travel with worker_action=none.
	if r.WorkerAction == "none" && r.WorkerExpiryMode != "" {
		return bad("worker_action=none cannot carry worker_expiry_mode")
	}
	// Only a fixed term is pinned by the operator. Every other mode derives its
	// deadline from the package (or from nothing at all), so an explicit
	// duration or date would contradict it.
	if r.WorkerExpiryMode != "" && r.WorkerExpiryMode != CloudWorkerExpiryFixed && (r.DurationDays != 0 || r.WorkerExpiresAt != nil) {
		return bad("worker_expiry_mode other than fixed cannot carry duration_days or worker_expires_at")
	}
	if r.WorkerExpiresAt != nil {
		v := r.WorkerExpiresAt.UTC()
		r.WorkerExpiresAt = &v
	}
	if !r.Preview && (!ValidCommercialActionID(r.OperationID) || r.Note == "" || r.PreviewToken == "") {
		return bad("apply requires a safe operation_id (<=96), note and preview_token")
	}
	if len(r.Note) > 4096 {
		return bad("note is too long")
	}
	return nil
}
func (r CommercialActionRequest) Fingerprint() string {
	r.Preview = false
	r.PreviewToken = ""
	r.OperationID = ""
	return CommercialActionHash(r)
}
func CommercialActionHash(v interface{}) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func CommercialActionSource(uid int64, operationID string) string {
	return fmt.Sprintf("ops:%d:%s", uid, CommercialActionHash(operationID)[:32])
}
func CommercialActionUsername(uid int64, operationID string) string {
	return "ops-" + CommercialActionHash(fmt.Sprintf("%d:%s", uid, operationID))[:20]
}

// CommercialActionWorker is one existing machine the action snapshot read. The
// operator action needs more than a count: a shared-container worker and a
// dedicated ECS instance are different products, so "keep the machine this
// account already has" is only answerable once every machine's identity and
// deployment profile are known.
//
// Both fields take part in the preview token, which is why a machine replaced by
// another one of the same deployment class (same count, different buyer-owned
// identity) or a machine switched to another backend invalidates a stale
// preview instead of being accepted as "there is still one machine".
type CommercialActionWorker struct {
	UID            int64  `json:"uid"`
	Profile        string `json:"profile"`
	LifecycleState string `json:"lifecycle_state,omitempty"`
	BillingAction  string `json:"billing_action,omitempty"`
}

// DeploymentClass reports the machine class (dedicated/shared) of this worker.
// An unknown recorded profile is reported as such instead of being guessed.
func (w CommercialActionWorker) DeploymentClass() (string, bool) {
	return CloudWorkerDeploymentClass(w.Profile)
}

// Missing lifecycle rows retain the historical ECS behavior. Once recorded,
// only states before physical deletion and without a billing claim are adoptable.
func CloudWorkerCanBeAdopted(state, billingAction string) bool {
	if strings.TrimSpace(billingAction) != "" {
		return false
	}
	switch strings.TrimSpace(state) {
	case "", "active", "delete_pending", "delete_failed":
		return true
	default:
		return false
	}
}

type CommercialActionPreview struct {
	UID               int64           `json:"uid"`
	Intent            string          `json:"intent"`
	CanApply          bool            `json:"can_apply"`
	Warnings          []string        `json:"warnings"`
	PreviewToken      string          `json:"preview_token"`
	CurrentPlan       *CommercialPlan `json:"current_plan"`
	TargetPlan        *CommercialPlan `json:"target_plan"`
	PreviousExpiresAt *time.Time      `json:"previous_expires_at"`
	ExpiresAt         *time.Time      `json:"expires_at"`
	DurationDays      int             `json:"duration_days"`
	WorkerAction      string          `json:"worker_action"`
	WorkerCount       int             `json:"worker_count"`
	// WorkerCount is every machine the account owns; WorkerMatchingCount is how
	// many of them belong to the deployment class of DeploymentProfile. They are
	// separate numbers because "the account already has a machine" is not the
	// same question as "the account already has a machine this operation may
	// keep": an ECS-only account must not turn a shared operation into
	// keep_existing, and the other way round.
	//
	// Both machine fields are omitempty on purpose. An action that sees no
	// machine (and every package-only action) keeps the historical JSON shape,
	// and therefore the durable preview token an already-journaled request was
	// written with; only an account that really owns machines adds their
	// identity, which is exactly what a stale preview has to notice.
	WorkerMatchingCount int `json:"worker_matching_count,omitempty"`
	// WorkerMachines pins the identity ("<uid>:<profile>") of every machine the
	// snapshot saw. A count cannot distinguish "the machine that was previewed"
	// from "a different machine of the same type", so replacing machines
	// one-for-one used to keep a stale preview valid.
	WorkerMachines    []string   `json:"worker_machines,omitempty"`
	WorkerExpiresAt   *time.Time `json:"worker_expires_at"`
	DeploymentProfile string     `json:"deployment_profile"`
	BillingMode       string     `json:"billing_mode"`
	RuntimePlan       string     `json:"runtime_plan"`
	// WorkerExpiryMode mirrors the request. Empty means the request predates
	// the field, so the preview keeps its historical shape and token.
	WorkerExpiryMode string  `json:"worker_expiry_mode,omitempty"`
	PackageEffect    string  `json:"package_effect"`
	WorkerEffect     string  `json:"worker_effect"`
	CurrentTotalCNY  float64 `json:"current_total_cny"`
	NextTotalCNY     float64 `json:"next_total_cny"`
}
type CommercialActionOperation struct {
	UID            int64  `json:"uid"`
	OperationID    string `json:"operation_id"`
	Intent         string `json:"intent"`
	Status         string `json:"status"`
	PackageApplied bool   `json:"package_applied"`
	RelaySynced    bool   `json:"relay_synced"`
	WorkerStatus   string `json:"worker_status"`
	WorkerUID      int64  `json:"worker_uid"`
	Error          string `json:"error"`
}
type CommercialAction struct {
	Operation CommercialActionOperation          `json:"operation"`
	Result    *CommercialAccountAdjustmentResult `json:"result,omitempty"`
	Request   CommercialActionRequest            `json:"-"`
	Preview   CommercialActionPreview            `json:"-"`
	Lease     string                             `json:"-"`
}
