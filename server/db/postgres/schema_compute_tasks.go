package postgres

// createComputeTaskTables is migration 000083 applied to a database created from
// scratch: CreateSchema builds the final shape, so the statements here are the
// same idempotent CREATE pair the migration file carries (a parity test keeps the
// two byte-identical).
//
// 两张表分开是因为两条事实的寿命不同：报价一旦卖出去就不能变（改价等于改合同），
// 而任务状态会一路推进（quoted → queued → active → completed/failed）。报价行的
// 条款写进 JSONB 快照并由触发器禁止 UPDATE；任务行的 state/lease/charged_units
// 才是可变部分。钱不在这里：预占/结算/释放走共享的 model credit 账本（PostgreSQL
// 独有），用 quote_id 当账本幂等键，重放任何一步都不会二次扣费。
const createComputeTaskTables = `
CREATE TABLE IF NOT EXISTS compute_task_quotes (
    quote_id VARCHAR(64) PRIMARY KEY CHECK (quote_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$'),
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    spec JSONB NOT NULL CHECK (jsonb_typeof(spec) = 'object'),
    sku VARCHAR(96) NOT NULL CHECK (length(sku) > 0),
    sku_logical_cpus INT NOT NULL CHECK (sku_logical_cpus > 0),
    sku_memory_mib INT NOT NULL CHECK (sku_memory_mib > 0),
    units_per_node_period BIGINT NOT NULL CHECK (units_per_node_period > 0),
    period_seconds BIGINT NOT NULL CHECK (period_seconds > 0),
    increment_seconds BIGINT NOT NULL CHECK (increment_seconds > 0),
    max_units BIGINT NOT NULL CHECK (max_units > 0),
    max_node_seconds BIGINT NOT NULL CHECK (max_node_seconds > 0),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_compute_task_quotes_uid
    ON compute_task_quotes (uid, expires_at);
-- 只挡 UPDATE：用户删除时 uid 的 ON DELETE CASCADE 还必须能把报价带走，
-- 挡掉 DELETE 会让删号直接失败。
CREATE OR REPLACE FUNCTION guard_compute_task_quote() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'compute task quote is immutable';
END;
$$;
DROP TRIGGER IF EXISTS guard_compute_task_quote ON compute_task_quotes;
CREATE TRIGGER guard_compute_task_quote BEFORE UPDATE ON compute_task_quotes
FOR EACH ROW EXECUTE FUNCTION guard_compute_task_quote();

CREATE TABLE IF NOT EXISTS compute_tasks (
    quote_id VARCHAR(64) PRIMARY KEY REFERENCES compute_task_quotes(quote_id) ON DELETE CASCADE,
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    state VARCHAR(16) NOT NULL DEFAULT 'quoted'
        CHECK (state IN ('quoted','queued','active','completed','failed')),
    progress JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(progress) = 'object'),
    lease VARCHAR(64) NOT NULL DEFAULT '',
    lease_expires_at TIMESTAMPTZ DEFAULT NULL,
    confirmed_max_units BIGINT NOT NULL DEFAULT 0 CHECK (confirmed_max_units >= 0),
    confirmed_at TIMESTAMPTZ DEFAULT NULL,
    charged_units BIGINT DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((state = 'active') = (lease <> '')),
    CHECK ((state = 'active') = (lease_expires_at IS NOT NULL)),
    CHECK ((state = 'quoted') = (confirmed_at IS NULL)),
    CHECK (charged_units IS NULL OR (charged_units >= 0 AND state IN ('completed','failed')))
);
-- 租约是 worker 的排他凭证：同一个租约只能挂在一个任务上，换 worker 必然换租约。
CREATE UNIQUE INDEX IF NOT EXISTS uk_compute_tasks_lease
    ON compute_tasks (lease) WHERE lease <> '';
CREATE INDEX IF NOT EXISTS idx_compute_tasks_claim
    ON compute_tasks (state, updated_at) WHERE state IN ('queued','active');
CREATE INDEX IF NOT EXISTS idx_compute_tasks_uid
    ON compute_tasks (uid, created_at DESC);

CREATE OR REPLACE FUNCTION guard_unfinished_compute_task_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state IN ('queued', 'active') THEN
        RAISE EXCEPTION 'compute task must settle before deletion';
    END IF;
    RETURN OLD;
END;
$$;
DROP TRIGGER IF EXISTS guard_unfinished_compute_task_delete ON compute_tasks;
CREATE TRIGGER guard_unfinished_compute_task_delete BEFORE DELETE ON compute_tasks
FOR EACH ROW EXECUTE FUNCTION guard_unfinished_compute_task_delete();
`
