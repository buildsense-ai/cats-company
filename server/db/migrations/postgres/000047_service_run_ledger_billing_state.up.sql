-- 000047_service_run_ledger_billing_state (up)
-- 结算状态（设计 §4.5）：按会话请求实时结算。
--   free             = 未定价（默认免费分享），无需扣费
--   settled_platform = 调用方额度已由 relay 在模型调用时实时扣除（codex 类 agent）
--   pending_credit   = 外部执行后端（runner.type=http）自报用量，平台侧尚无扣费通道，
--                      金额计入该调用方的"待扣"累计，预检时一并算作已用额度
ALTER TABLE service_run_ledger
    ADD COLUMN IF NOT EXISTS billing_state VARCHAR(24) NOT NULL DEFAULT 'free';

CREATE INDEX IF NOT EXISTS idx_service_run_ledger_pending
    ON service_run_ledger (payer_uid) WHERE billing_state = 'pending_credit';
