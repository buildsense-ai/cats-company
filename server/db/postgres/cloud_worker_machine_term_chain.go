package postgres

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// This file holds the coverage arithmetic behind cloudWorkerMachineTermQuery.
//
// A machine grant is not always one row: an early renewal, and the operator
// extension that mirrors it, append a segment that starts exactly where the
// current period ends. The worker's real term is the end of the chain of bought
// windows that starts at the grant in force today - not the end of the single
// row that happens to be effective - and the chain must stop at the first gap,
// otherwise a package bought for next month would authorize a machine today.
//
// Keeping the arithmetic out of the SQL is what makes the "does this segment
// join the previous one" question testable in one place, and it keeps the
// eligibility rule itself in a single statement (cloudWorkerMachineTermQuery).

// cloudWorkerMachineSegment is one already-bought window of machine access, as
// the eligibility query returns it. An invalid ExpiresAt means the window never
// ends (a perpetual package).
type cloudWorkerMachineSegment struct {
	EntitlementID int64
	PlanID        int64
	StartsAt      time.Time
	ExpiresAt     sql.NullTime
	// RetentionDays is already resolved from the plan's configured value and its
	// length, so two readers cannot derive it differently.
	RetentionDays int
}

// covers reports whether this window is in force at now.
func (s cloudWorkerMachineSegment) covers(now time.Time) bool {
	if s.StartsAt.After(now) {
		return false
	}
	return !s.ExpiresAt.Valid || s.ExpiresAt.Time.After(now)
}

// loadCloudWorkerMachineSegments reads every window that is eligible to grant a
// machine, on whichever side of "now" it starts. The rows are already in the
// start-time order the coverage walk wants; it re-sorts locally anyway so the
// arithmetic does not depend on the statement's ORDER BY surviving an edit.
func loadCloudWorkerMachineSegments(q rowQuerier, ownerUID int64) ([]cloudWorkerMachineSegment, error) {
	rows, err := q.Query(cloudWorkerMachineTermQuery, ownerUID)
	if err != nil {
		return nil, fmt.Errorf("load cloud worker machine package: %w", err)
	}
	defer rows.Close()
	var segments []cloudWorkerMachineSegment
	for rows.Next() {
		var segment cloudWorkerMachineSegment
		var configuredRetention, durationDays int
		if err := rows.Scan(
			&segment.EntitlementID,
			&segment.PlanID,
			&segment.StartsAt,
			&segment.ExpiresAt,
			&configuredRetention,
			&durationDays,
		); err != nil {
			return nil, fmt.Errorf("scan cloud worker machine package: %w", err)
		}
		segment.StartsAt = segment.StartsAt.UTC()
		if segment.ExpiresAt.Valid {
			segment.ExpiresAt.Time = segment.ExpiresAt.Time.UTC()
		}
		segment.RetentionDays = types.CloudWorkerRetentionDays(configuredRetention, durationDays)
		segments = append(segments, segment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read cloud worker machine package: %w", err)
	}
	return segments, nil
}

// selectCloudWorkerMachinePrimary picks the machine package in force right now,
// using the precedence the rest of the store uses for the primary package: the
// latest window wins, a perpetual one sorts last so a dated package is preferred
// while it lasts, then the most recent start. This is the package - and
// therefore the retention - the account is told it is on; the merged coverage
// changes when that package ends, never which package it is.
func selectCloudWorkerMachinePrimary(segments []cloudWorkerMachineSegment, now time.Time) (cloudWorkerMachineSegment, bool) {
	var best cloudWorkerMachineSegment
	found := false
	for _, segment := range segments {
		if !segment.covers(now) {
			continue
		}
		if !found || cloudWorkerMachineSegmentOutranks(segment, best) {
			best = segment
			found = true
		}
	}
	return best, found
}

// cloudWorkerMachineSegmentOutranks is "expires_at DESC NULLS LAST, starts_at
// DESC, id DESC" in Go, so the primary pick stays one comparable rule.
func cloudWorkerMachineSegmentOutranks(a, b cloudWorkerMachineSegment) bool {
	switch {
	case a.ExpiresAt.Valid && !b.ExpiresAt.Valid:
		return true
	case !a.ExpiresAt.Valid && b.ExpiresAt.Valid:
		return false
	case a.ExpiresAt.Valid && b.ExpiresAt.Valid && !a.ExpiresAt.Time.Equal(b.ExpiresAt.Time):
		return a.ExpiresAt.Time.After(b.ExpiresAt.Time)
	case !a.StartsAt.Equal(b.StartsAt):
		return a.StartsAt.After(b.StartsAt)
	default:
		return a.EntitlementID > b.EntitlementID
	}
}

// cloudWorkerMachineCoverageEnd returns the end of the contiguous machine
// coverage that starts at the package in force now. An invalid result means
// "no deadline at all", which is how a perpetual package has always been
// represented.
//
// Only segments that have not started yet can lengthen the chain: an already
// started one is covered by the primary pick above, which is also why a
// perpetual grant that is already in force does not swallow the dated window the
// ordering prefers. Walking starts at the primary's own expiry and follows the
// bought segments in start order, so:
//
//   - a segment that begins at or before the current end joins the chain (this
//     is what makes an early renewal, and a run of them, keep the time the buyer
//     paid for);
//   - a segment that begins after the current end is a gap: nothing buys that
//     time yet, so the chain stops there and the worker is not granted the later
//     window early;
//   - a joined perpetual segment removes the deadline.
func cloudWorkerMachineCoverageEnd(segments []cloudWorkerMachineSegment, primary cloudWorkerMachineSegment, now time.Time) sql.NullTime {
	if !primary.ExpiresAt.Valid {
		return sql.NullTime{}
	}
	end := primary.ExpiresAt.Time
	pending := make([]cloudWorkerMachineSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.StartsAt.After(now) {
			pending = append(pending, segment)
		}
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if !pending[i].StartsAt.Equal(pending[j].StartsAt) {
			return pending[i].StartsAt.Before(pending[j].StartsAt)
		}
		return pending[i].EntitlementID < pending[j].EntitlementID
	})
	for _, segment := range pending {
		if segment.StartsAt.After(end) {
			break
		}
		if !segment.ExpiresAt.Valid {
			return sql.NullTime{}
		}
		if segment.ExpiresAt.Time.After(end) {
			end = segment.ExpiresAt.Time
		}
	}
	return sql.NullTime{Time: end, Valid: true}
}
