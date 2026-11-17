-- 000075: paid-credit hold for commercial refunds.
--
-- A refund used to call the payment channel first and revoke the credit
-- afterwards, so a failure in between left the money paid out while the
-- purchased credit was still spendable. The hold turns the order around: the
-- paid units an order may have to reverse are moved out of the spendable
-- bucket and recorded here BEFORE any refund call, and completion consumes the
-- hold. `units = 0` is a real row too: an order that never booked paid credit
-- (a subscription, whose allowance lives in its monthly period) still records
-- that it was refunded under the safe protocol.
--
-- A refund refuses to start when the credit is not provably reversible (already
-- consumed, held by an open model call, held by a machine upgrade, or spent on
-- arrears), and then writes no row here.
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
