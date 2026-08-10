CREATE TABLE IF NOT EXISTS commercial_order_request_ids (
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    client_request_id VARCHAR(64) NOT NULL,
    order_no VARCHAR(40) NOT NULL REFERENCES commercial_orders(order_no) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(uid, client_request_id)
);

INSERT INTO commercial_order_request_ids(uid, client_request_id, order_no)
SELECT uid, client_request_id, order_no
FROM commercial_orders
ON CONFLICT(uid, client_request_id) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_commercial_order_request_ids_order
    ON commercial_order_request_ids(order_no);
