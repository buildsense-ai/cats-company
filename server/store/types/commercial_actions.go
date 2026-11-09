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
	default:
		return bad("unsupported intent")
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
	WorkerExpiresAt   *time.Time      `json:"worker_expires_at"`
	DeploymentProfile string          `json:"deployment_profile"`
	BillingMode       string          `json:"billing_mode"`
	RuntimePlan       string          `json:"runtime_plan"`
	PackageEffect     string          `json:"package_effect"`
	WorkerEffect      string          `json:"worker_effect"`
	CurrentTotalCNY   float64         `json:"current_total_cny"`
	NextTotalCNY      float64         `json:"next_total_cny"`
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
