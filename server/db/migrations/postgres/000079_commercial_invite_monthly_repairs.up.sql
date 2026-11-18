CREATE TABLE IF NOT EXISTS commercial_invite_monthly_repairs (
    entitlement_id BIGINT PRIMARY KEY REFERENCES commercial_entitlements(id) ON DELETE RESTRICT,
    units BIGINT NOT NULL CHECK (units > 0),
    term_ends_at TIMESTAMPTZ NOT NULL,
    audit_ref TEXT NOT NULL CHECK (length(audit_ref) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
