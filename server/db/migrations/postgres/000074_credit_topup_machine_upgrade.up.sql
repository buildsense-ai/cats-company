-- 点数充值（credit top-up）与云机器点数升配（machine upgrade）。
--
-- 两件事共用一条原则：钱换到的点数与「买套餐」是两笔独立的产品。
--   * 充值 = 直接把 CNY 分换成 paid 桶点数（1 分 = 1 点 = 1000000 micro units），
--     不带赠送、不带机器、不带订阅、不改任何已有套餐/租期。为了不改动
--     CreateCommercialOrder 里那套套餐校验，充值订单在 commercial_orders 上
--     用 kind='credit_topup' 标记，并且没有 plan_id（plan_id 允许 NULL）。
--   * 升配 = 用点数把「已经买到的机器」原地换更大的档位，只收剩余时长的差价；
--     它不产生新套餐、不延长租期，所以报价、操作与权益各有一张表，和套餐
--     的 commercial_entitlements 完全分开。

-- ---------------------------------------------------------------------------
-- 1. commercial_orders: 订单种类 + 可空 plan_id
-- ---------------------------------------------------------------------------
ALTER TABLE commercial_orders ADD COLUMN IF NOT EXISTS kind VARCHAR(24) NOT NULL DEFAULT 'plan';

ALTER TABLE commercial_orders ALTER COLUMN plan_id DROP NOT NULL;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_orders_kind' AND conrelid = 'commercial_orders'::regclass) THEN
        ALTER TABLE commercial_orders ADD CONSTRAINT chk_commercial_orders_kind
            CHECK (kind IN ('plan','credit_topup'));
    END IF;
    -- 充值订单没有商品行；套餐订单必须有。这条约束替掉了原来 plan_id NOT NULL
    -- 的强度，又不允许两种订单互相冒充。
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_orders_kind_plan' AND conrelid = 'commercial_orders'::regclass) THEN
        ALTER TABLE commercial_orders ADD CONSTRAINT chk_commercial_orders_kind_plan
            CHECK ((kind = 'credit_topup') = (plan_id IS NULL));
    END IF;
END $$;

-- 一份 client_request_id 只对应一笔订单，这一点不因 kind 而变；但同一串 id
-- 用在另一种金额上必须被拒绝（付款接口负责判断，这里只把查询变快）。
CREATE INDEX IF NOT EXISTS idx_commercial_orders_uid_kind
    ON commercial_orders (uid, kind, created_at DESC);

-- ---------------------------------------------------------------------------
-- 2. 云机器升配：报价、持久操作、独立升配权益
-- ---------------------------------------------------------------------------

-- 报价只活 5 分钟，落库是为了让 POST 能重新核对「谁、哪台机器、从哪档到哪档、
-- 多少钱」四件事没有被换掉。过期不删除也不影响正确性：读的时候一律再比一次
-- expires_at，写入时才校验。
CREATE TABLE IF NOT EXISTS cloud_worker_machine_upgrade_quotes (
    quote_id VARCHAR(64) PRIMARY KEY CHECK (quote_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$'),
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_name VARCHAR(80) NOT NULL,
    lifecycle_id BIGINT NOT NULL,
    from_plan VARCHAR(16) NOT NULL,
    to_plan VARCHAR(16) NOT NULL,
    cost_units BIGINT NOT NULL CHECK (cost_units > 0),
    worker_expires_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (from_plan IN ('light','standard','pro','heavy')),
    CHECK (to_plan IN ('light','standard','pro','heavy')),
    CHECK (from_plan <> to_plan)
);
CREATE INDEX IF NOT EXISTS idx_machine_upgrade_quotes_uid
    ON cloud_worker_machine_upgrade_quotes (uid, expires_at);

-- 一台机器同一时刻只允许一个升配操作：唯一索引把跨进程的串行交给数据库，
-- 进程内的 owner lock 只是省一次往返。
CREATE TABLE IF NOT EXISTS cloud_worker_machine_upgrades (
    operation_id VARCHAR(64) PRIMARY KEY CHECK (operation_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$'),
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_name VARCHAR(80) NOT NULL,
    lifecycle_id BIGINT NOT NULL,
    from_plan VARCHAR(16) NOT NULL,
    to_plan VARCHAR(16) NOT NULL,
    cost_units BIGINT NOT NULL CHECK (cost_units >= 0),
    -- pending     : 已受理，还没开始动余额/机器
    -- running     : 已经预占点数，脚本在跑
    -- completed   : 脚本成功，点数已结算，权益已写
    -- failed      : 明确失败且无副作用，预占已释放
    -- reconciling : 结果未知（超时/进程中断），预占保留，等 reconcile 收尾
    status VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','running','completed','failed','reconciling')),
    -- held 表示这笔操作此刻还在点数账本上占着额度；settled 表示已经转成实际扣费。
    held BOOLEAN NOT NULL DEFAULT FALSE,
    settled BOOLEAN NOT NULL DEFAULT FALSE,
    client_request_id VARCHAR(64) NOT NULL,
    quote_id VARCHAR(64) NOT NULL DEFAULT '',
    attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    machine_expires_at TIMESTAMPTZ NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ DEFAULT NULL,
    finished_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (from_plan IN ('light','standard','pro','heavy')),
    CHECK (to_plan IN ('light','standard','pro','heavy')),
    -- 重复提交同一个 client_request_id 不能变成第二次扣费。
    CONSTRAINT uk_machine_upgrades_request UNIQUE (uid, client_request_id)
);
-- 同一台机器不允许两笔未完成的升配（跨进程串行）。
CREATE UNIQUE INDEX IF NOT EXISTS uk_machine_upgrades_open_tenant
    ON cloud_worker_machine_upgrades (tenant_name)
    WHERE status IN ('pending','running','reconciling');
CREATE INDEX IF NOT EXISTS idx_machine_upgrades_reconcile
    ON cloud_worker_machine_upgrades (status, updated_at)
    WHERE status IN ('pending','running','reconciling');

-- 独立升配权益：只属于原机器生命周期，并钳到当前有效租期。
-- 在已购买窗口内 ApplyPlanToOwner 取更高档；套餐续费不免费延长升配窗口。
CREATE TABLE IF NOT EXISTS cloud_worker_machine_plan_terms (
    tenant_name VARCHAR(80) PRIMARY KEY,
    owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- 这条权益属于「当初卖出这台机器」的那一条 lifecycle：读取和生效都要和
    -- 它按 id 对上，并且同时要求 owner 一致、state='active'、package 未到期。
    -- 机器被删、套餐退款/缩期、tenant 名被另一条 lifecycle 复用时，这条权益
    -- 自然失效，不会被下一台机器或下一个主人继承。0 表示没有绑定，永不在效。
    lifecycle_id BIGINT NOT NULL DEFAULT 0,
    plan VARCHAR(16) NOT NULL CHECK (plan IN ('light','standard','pro','heavy')),
    -- base_plan 是这档升配「替换掉」的套餐档位，用于租期结束时判断是否需要回落。
    base_plan VARCHAR(16) NOT NULL DEFAULT ''
        CHECK (base_plan IN ('','light','standard','pro','heavy')),
    operation_id VARCHAR(64) NOT NULL DEFAULT '',
    -- 升配权益的窗口：和机器剩余 term 一致，不延长，也绝不超过 lifecycle 的
    -- package_expires_at（套餐缩期后权益必须跟着缩短）。
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- 已经用旧形状建过这张表的库（迁移可重复执行）补上 lifecycle_id；默认 0
-- 的老行永远 join 不上 lifecycle，也就是「不在效」，需要重新升配才有权益。
ALTER TABLE cloud_worker_machine_plan_terms ADD COLUMN IF NOT EXISTS lifecycle_id BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_machine_plan_terms_lifecycle
    ON cloud_worker_machine_plan_terms (lifecycle_id);
CREATE INDEX IF NOT EXISTS idx_machine_plan_terms_owner
    ON cloud_worker_machine_plan_terms (owner_uid, expires_at);
