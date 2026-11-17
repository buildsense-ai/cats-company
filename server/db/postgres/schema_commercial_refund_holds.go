package postgres

// createCommercialRefundHolds is migration 000075 applied to a database created
// from scratch: CreateSchema builds the final shape, so the statements here are
// the same idempotent CREATE pair the migration file carries.
//
// Why the table exists at all: a refund calls the payment channel, and the
// credit it reverses lives in a second subsystem. Doing the channel call first
// and the credit reversal afterwards leaves a window where the money is gone but
// the credit is still spendable — and when the reversal fails, the order used to
// be released back to 'fulfilled' with the cash already refunded.
//
// The hold closes that window. Before the channel is touched, the paid units the
// order may have to reverse are moved out of the spendable paid bucket and this
// row records the freeze. Completion consumes the row; a refund that cannot
// finish keeps both the row and the 'refunding' status, so the frozen credit can
// never be spent while the channel outcome is unknown.
//
// units = 0 is a real row: an order that never booked paid credit (a
// subscription, whose allowance belongs to its monthly period) still records
// that it was refunded under the safe protocol, so "is refunding without a hold"
// stays a meaningful, fail-closed check for rows written by older builds.
const createCommercialRefundHolds = `
CREATE TABLE IF NOT EXISTS commercial_refund_holds (
    order_no VARCHAR(40) PRIMARY KEY REFERENCES commercial_orders(order_no) ON DELETE CASCADE,
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    state VARCHAR(16) NOT NULL DEFAULT 'held',
    units BIGINT NOT NULL DEFAULT 0,
    refund_request_no VARCHAR(64) NOT NULL,
    reserve_event_key VARCHAR(192) NOT NULL DEFAULT '',
    consumed_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_refund_holds_state CHECK (state IN ('held','consumed')),
    CONSTRAINT chk_commercial_refund_holds_units CHECK (units >= 0),
    CONSTRAINT chk_commercial_refund_holds_consumed CHECK ((state = 'consumed') = (consumed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_commercial_refund_holds_state
    ON commercial_refund_holds (state, updated_at);
CREATE INDEX IF NOT EXISTS idx_commercial_refund_holds_uid
    ON commercial_refund_holds (uid, created_at DESC);
`
