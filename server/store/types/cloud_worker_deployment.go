package types

import "time"

// Deployment profiles are operator-issued capabilities, never a public create input.
const (
	CloudWorkerPrivateNAT = "private_nat"
	CloudWorkerPublicIP   = "public_ip"
	CloudWorkerMonthly    = "month"
	CloudWorkerOnDemand   = "ondemand"
)

func NormalizeCloudWorkerBilling(mode string) (string, bool) {
	if mode == "" || mode == CloudWorkerMonthly {
		return CloudWorkerMonthly, true
	}
	return mode, mode == CloudWorkerOnDemand
}

type CloudWorkerCreditSelection struct {
	Profile     string
	BillingMode string
	// ExpiresAt carries the paid window of the reserved credit so the create
	// path can prepay the provider for every month the credit covers. It is
	// nil for perpetual manual grants.
	ExpiresAt *time.Time
}

func NormalizeCloudWorkerProfile(profile string) (string, bool) {
	if profile == "" || profile == CloudWorkerPrivateNAT {
		return CloudWorkerPrivateNAT, true
	}
	return profile, profile == CloudWorkerPublicIP
}

// CloudWorkerDeployment is an internal, non-secret snapshot. It is deliberately
// separate from public worker summaries and must never contain credential values.
type CloudWorkerDeployment struct {
	Profile string            `json:"profile"`
	Env     map[string]string `json:"env"`
}
