package types

import "time"

// CommercialAutoRenewConfig is the per-user opt-in switch for the scheduled
// package renewer. Internal accounts opt in first through the relay-admin
// console; once self-serve renewal opens the same row carries user consent.
type CommercialAutoRenewConfig struct {
	UID       int64     `json:"uid"`
	Enabled   bool      `json:"enabled"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CommercialAutoRenewDue is one enabled user whose latest paid window
// expires inside the renewal lead time and should be extended this pass.
type CommercialAutoRenewDue struct {
	UID       int64
	ExpiresAt time.Time
}

// CommercialAutoRenewRun is the detailed audit record of one scheduled pass
// for one user, surfaced in the relay-admin console.
type CommercialAutoRenewRun struct {
	ID             int64      `json:"id"`
	UID            int64      `json:"uid"`
	Action         string     `json:"action"`
	Status         string     `json:"status"`
	PreviousExpiry *time.Time `json:"previous_expiry,omitempty"`
	NewExpiry      *time.Time `json:"new_expiry,omitempty"`
	Message        string     `json:"message,omitempty"`
	OperationID    string     `json:"operation_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}
