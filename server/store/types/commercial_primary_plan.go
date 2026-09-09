package types

import "time"

// PrimaryCommercialEntitlement follows the same ordering as PostgreSQL's
// commercialPrimaryPackageExpiry and same-plan guard. Baselines must not hide
// a package obtained from an order, invitation, or operator assignment.
func PrimaryCommercialEntitlement(items []*CommercialEntitlement, now time.Time) *CommercialEntitlement {
	var selected *CommercialEntitlement
	baseline := func(item *CommercialEntitlement) bool { return item.Source == "free" || item.Source == "legacy" }
	before := func(a, b *CommercialEntitlement) bool {
		if baseline(a) != baseline(b) {
			return !baseline(a)
		}
		if a.ExpiresAt == nil && b.ExpiresAt != nil {
			return false
		}
		if a.ExpiresAt != nil && b.ExpiresAt == nil {
			return true
		}
		if a.ExpiresAt != nil && !a.ExpiresAt.Equal(*b.ExpiresAt) {
			return a.ExpiresAt.After(*b.ExpiresAt)
		}
		if !a.StartsAt.Equal(b.StartsAt) {
			return a.StartsAt.After(b.StartsAt)
		}
		return a.ID > b.ID
	}
	for _, item := range items {
		if item == nil || item.State != "active" || item.StartsAt.After(now) || (item.ExpiresAt != nil && !item.ExpiresAt.After(now)) {
			continue
		}
		if selected == nil || before(item, selected) {
			selected = item
		}
	}
	return selected
}
