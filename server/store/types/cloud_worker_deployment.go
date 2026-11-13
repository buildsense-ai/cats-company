package types

import (
	"strings"
	"time"
)

// Deployment profiles are operator-issued capabilities, never a public create input.
const (
	CloudWorkerPrivateNAT = "private_nat"
	CloudWorkerPublicIP   = "public_ip"
	// CloudWorkerPackedShared runs the worker inside the shared container
	// cluster instead of a dedicated ECS instance. Operators can hand it out
	// through the same invite/credit paths as the dedicated profiles, so both
	// backends coexist on one fleet.
	CloudWorkerPackedShared = "packed_shared"
	CloudWorkerMonthly      = "month"
	CloudWorkerOnDemand     = "ondemand"
)

// Cloud worker term modes decide what a worker's expiry actually follows.
//
// A dedicated ECS worker buys a cloud subscription, so the only term it can
// honour is the one prepaid at creation (fixed) or the historical behaviour
// where the term came from whatever package was current (legacy). The shared
// container fleet has no subscription per worker: the cluster already owns the
// machine, so an operator can pin a worker to its own window (fixed), let it
// ride along with the active primary package (follow_plan — including a
// perpetual one) or grant it without any deadline (permanent).
//
// legacy is never requested: it is the value historical rows and credits keep
// so nothing written before this field existed changes behaviour.
const (
	CloudWorkerExpiryLegacy     = "legacy"
	CloudWorkerExpiryFixed      = "fixed"
	CloudWorkerExpiryFollowPlan = "follow_plan"
	CloudWorkerExpiryPermanent  = "permanent"
)

// NormalizeCloudWorkerExpiryMode validates an operator-supplied term mode.
//
// An empty value stays empty on purpose. It is the honest representation of a
// request written before the field existed, and rewriting it to a concrete mode
// would change the durable fingerprint of an already-journaled operation (and
// silently re-term every worker those requests own).
func NormalizeCloudWorkerExpiryMode(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return "", true
	case CloudWorkerExpiryFixed, CloudWorkerExpiryFollowPlan, CloudWorkerExpiryPermanent:
		return strings.ToLower(strings.TrimSpace(mode)), true
	default:
		return mode, false
	}
}

// CloudWorkerExpiryModeRequiresShared reports whether a mode is only meaningful
// on the shared container fleet. "Follow the package" and "permanent" are not
// terms a prepaid ECS instance can honour.
func CloudWorkerExpiryModeRequiresShared(mode string) bool {
	return mode == CloudWorkerExpiryFollowPlan || mode == CloudWorkerExpiryPermanent
}

// CloudWorkerExpiryModeOrDefault maps an empty (historical) request mode to the
// row value legacy rows carry, so persisted credits and lifecycles keep their
// meaning instead of turning into a new term by accident.
func CloudWorkerExpiryModeOrDefault(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return CloudWorkerExpiryLegacy
	}
	return mode
}

// CloudWorkerExpiryModeIndependentOfPlan reports whether a paid-period
// extension must leave the row's own term alone. A fixed cluster worker was
// sold its own window and a permanent grant has no window at all; neither is
// extended, shortened or rescheduled by a package purchase.
func CloudWorkerExpiryModeIndependentOfPlan(mode string) bool {
	return mode == CloudWorkerExpiryFixed || mode == CloudWorkerExpiryPermanent
}
func NormalizeCloudWorkerBilling(mode string) (string, bool) {
	if mode == "" || mode == CloudWorkerMonthly {
		return CloudWorkerMonthly, true
	}
	return mode, mode == CloudWorkerOnDemand
}

// CloudWorkerBillingForProfile collapses the machine-billing dimension for the
// shared-container fleet.
//
// month/ondemand exists because a dedicated ECS worker buys a cloud
// subscription: month pre-pays cycles, ondemand runs a trial that converts
// later. A packed container has no subscription to buy — the cluster already
// owns the machine and only the package window matters. Carrying "ondemand"
// there is not merely meaningless, it is harmful: the expiry sweeper routes
// ondemand lifecycles into the ECS billing scripts, which do not exist on the
// packed path, so the worker would never expire.
//
// Callers should run this after the profile is resolved and before persisting.
func CloudWorkerBillingForProfile(profile, billing string) string {
	normalizedProfile, _ := NormalizeCloudWorkerProfile(profile)
	normalizedBilling, _ := NormalizeCloudWorkerBilling(billing)
	if normalizedProfile == CloudWorkerPackedShared {
		return CloudWorkerMonthly
	}
	return normalizedBilling
}

type CloudWorkerCreditSelection struct {
	Profile     string
	BillingMode string
	// RuntimePlan is the shared-fleet slot size the purchased package bought.
	// Empty keeps the fleet default (standard), which is what credits issued
	// before the column existed carry.
	RuntimePlan string
	// RetentionDays is how long the worker's data survives package expiry; it
	// travels with the credit so the create path schedules the same window the
	// purchased package promised.
	RetentionDays int
	// ExpiresAt carries the paid window of the reserved credit so the create
	// path can prepay the provider for every month the credit covers. It is
	// nil for perpetual manual grants.
	ExpiresAt *time.Time
	// ExpiryMode is the term the credit carries (fixed/follow_plan/permanent).
	// Empty or legacy means the historical behaviour: the create path derives
	// the term from the credit's paid window exactly as it always did.
	ExpiryMode string
	// RetainData marks a credit bought with paid model credit: the worker it
	// provisions is a credit environment, so its files and history survive the
	// end of the paid window (approved credit rules 2026-11-09, P1-2). It
	// travels with the credit and is snapshotted onto the lifecycle row, so a
	// later lapse, revocation or plan change cannot lose it. Legacy plans
	// (model_credit_units = 0) and operator grants carry false and keep the
	// historical cleanup schedule.
	RetainData bool
}

func NormalizeCloudWorkerProfile(profile string) (string, bool) {
	if profile == "" || profile == CloudWorkerPrivateNAT {
		return CloudWorkerPrivateNAT, true
	}
	if profile == CloudWorkerPublicIP || profile == CloudWorkerPackedShared {
		return profile, true
	}
	return profile, false
}

// Shared-fleet runtime tiers. These must match the packed manager's
// plib_plan_values so a package's density assumption survives the hand-off:
// light 100 / standard 200 / pro 400 / heavy 800 CPU weight.
const (
	CloudWorkerRuntimeLight    = "light"
	CloudWorkerRuntimeStandard = "standard"
	CloudWorkerRuntimePro      = "pro"
	CloudWorkerRuntimeHeavy    = "heavy"
)

// NormalizeCloudWorkerRuntimePlan validates a runtime tier. An empty string is
// valid and means "let the fleet decide": that is what operator-issued credits
// and every package created before the column existed carry, and rewriting them
// to an explicit tier would change what an already-paid slot gets.
func NormalizeCloudWorkerRuntimePlan(plan string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "":
		return "", true
	case CloudWorkerRuntimeLight, CloudWorkerRuntimeStandard, CloudWorkerRuntimePro, CloudWorkerRuntimeHeavy:
		return strings.ToLower(strings.TrimSpace(plan)), true
	default:
		return plan, false
	}
}

// CloudWorkerRetentionDays resolves how long a worker's data survives package
// expiry before the sweeper destroys it.
//
// Product rule (2026-10-27): a 7-day package keeps its data for 3 days, a
// 30-day package for 7 days — the same shape as the ECS monthly instance's
// retention window. An explicit plan value always wins so an operator can
// override a single package; 0 means "derive from the package length".
func CloudWorkerRetentionDays(configured, durationDays int) int {
	if configured > 0 {
		if configured > 90 {
			return 90
		}
		return configured
	}
	if durationDays <= 0 {
		// Unknown package length: keep the legacy 15-day window rather than
		// shortening a customer's recovery period by accident.
		return 15
	}
	if durationDays <= 7 {
		return 3
	}
	return 7
}

// CloudWorkerDeployment is an internal, non-secret snapshot. It is deliberately
// separate from public worker summaries and must never contain credential values.
type CloudWorkerDeployment struct {
	Profile string            `json:"profile"`
	Env     map[string]string `json:"env"`
}
