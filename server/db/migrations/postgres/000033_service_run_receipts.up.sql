-- 生命周期回注收件箱（设计 §3.3-2 回流通道 / §4.8-2 回注送达）。
-- 背景：run 以 completed / failed / awaiting_input 收尾时，平台需把回执主动推回
-- 调用方（唤醒注入），且满足"至少一次投递 + 幂等去重 + 离线待取"：
--   收件箱（本表）持久化每份回执（跟账号 caller_uid），投递 worker 在线即推、
--   离线留存，调用方上线后补投；拉取（catsco_run_status）永远是真相通道。
-- receipt_id 由 run_id 确定性派生（rcp_<run_id 去前缀>）→ 天然幂等去重载体。

CREATE TABLE IF NOT EXISTS service_run_receipts (
    receipt_id VARCHAR(64) PRIMARY KEY,
    run_id VARCHAR(64) NOT NULL REFERENCES service_runs(run_id) ON DELETE CASCADE,
    session_id VARCHAR(64) NOT NULL,
    caller_uid BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL,
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    delivery_attempts INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    delivered_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_service_run_receipts_pending
    ON service_run_receipts (created_at) WHERE delivered_at IS NULL;
