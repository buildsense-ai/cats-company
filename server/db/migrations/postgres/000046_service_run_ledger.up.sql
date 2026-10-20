-- 000046_service_run_ledger (up)
-- run 级账本（设计 §4.5）：谁调用谁付费、按量计费。
-- 只记账不动钱：金额在写入时按 agent pricing 快照计算好，后续结算直接读这张表。
CREATE TABLE IF NOT EXISTS service_run_ledger (
    run_id            VARCHAR(64) PRIMARY KEY,
    session_id        VARCHAR(64) NOT NULL,
    agent_id          BIGINT NOT NULL,
    agent_slug        VARCHAR(64) NOT NULL,
    owner_kind        VARCHAR(16) NOT NULL DEFAULT 'internal',
    owner_uid         BIGINT NOT NULL DEFAULT 0,
    caller_uid        BIGINT NOT NULL,
    caller_bot_uid    BIGINT NOT NULL DEFAULT 0,
    runner_type       VARCHAR(16) NOT NULL DEFAULT 'codex',
    status            VARCHAR(16) NOT NULL,
    prompt_tokens     BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    duration_ms       BIGINT NOT NULL DEFAULT 0,
    -- 计费快照：单价与金额都与当时的 definition 绑定，后续改价不影响历史账。
    pricing_json      JSONB,
    currency          VARCHAR(8) NOT NULL DEFAULT 'CNY',
    gross_micros      BIGINT NOT NULL DEFAULT 0,
    fee_micros        BIGINT NOT NULL DEFAULT 0,
    net_micros        BIGINT NOT NULL DEFAULT 0,
    -- 谁调用谁给钱：payer=调用者；payee=agent owner（internal → 0 表示平台自营）
    payer_uid         BIGINT NOT NULL DEFAULT 0,
    payee_uid         BIGINT NOT NULL DEFAULT 0,
    finished_at       TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_service_run_ledger_payer
    ON service_run_ledger (payer_uid, finished_at DESC);
CREATE INDEX IF NOT EXISTS idx_service_run_ledger_payee
    ON service_run_ledger (payee_uid, finished_at DESC);
CREATE INDEX IF NOT EXISTS idx_service_run_ledger_agent
    ON service_run_ledger (agent_id, finished_at DESC);
