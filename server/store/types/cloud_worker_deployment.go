package types

import "time"

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
	// RetentionDays is how long the worker's data survives package expiry; it
	// travels with the credit so the create path schedules the same window the
	// purchased package promised.
	RetentionDays int
	// ExpiresAt carries the paid window of the reserved credit so the create
	// path can prepay the provider for every month the credit covers. It is
	// nil for perpetual manual grants.
	ExpiresAt *time.Time
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
