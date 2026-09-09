package types

import "errors"

var ErrCommercialPlanNotFound = errors.New("套餐不存在或已删除")
var ErrCommercialPlanDeleteConflict = errors.New("套餐仍在展示、使用中或有未完成的关联业务，不能删除")

// Admin-only statistics; never attach UID lists to public plan responses.
type CommercialPlanUsage struct {
	PlanID          int64  `json:"plan_id"`
	ActiveUsers     int64  `json:"active_users"`
	ScheduledUsers  int64  `json:"scheduled_users"`
	ActiveInvites   int64  `json:"active_invites"`
	OpenOrders      int64  `json:"open_orders"`
	CanDelete       bool   `json:"can_delete"`
	DeleteBlockedBy string `json:"delete_blocked_by,omitempty"`
}

type CommercialPlanUsers struct {
	UIDs    []int64 `json:"uids"`
	HasMore bool    `json:"has_more"`
	NextUID int64   `json:"next_uid"`
}
