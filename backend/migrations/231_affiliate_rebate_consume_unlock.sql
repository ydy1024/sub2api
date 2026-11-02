-- 邀请返利解冻条件：从「时间到期」改为「被邀请人消费完其充值额度」。
--
-- 背景：原实现（133_affiliate_rebate_freeze.sql）用 frozen_until 做时间冻结，
-- 充值后等固定小时数即解冻。这无法阻止「充值 → 拿返利 → 退款/提走」的套利。
-- 新规则：被邀请人每笔充值产生的返利，需等该被邀请人实际消费掉这笔充值额度的
-- N%（affiliate_rebate_unlock_consume_percent，默认 100%）后才解冻。

-- 1) 用户累计消费计数器（单调递增，只增不减）。
-- 余额可以充值/退款/提现来回变动，无法用它判断「额度是否用完」，
-- 因此需要一个独立的、只增不减的累计消费额。
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS total_consumed DECIMAL(20,8) NOT NULL DEFAULT 0;

COMMENT ON COLUMN users.total_consumed IS '累计消费额（单调递增，含扣减余额的消费与订阅计划用量）';

-- 2) 返利条目的消费解冻门槛。
-- unlock_consume_required 非 NULL 表示该条目仍处于冻结中（等待消费达标）；
-- NULL 表示已解冻或从未冻结 —— 与原 frozen_until 的语义保持一致。
-- 解冻判定：被邀请人 total_consumed >= unlock_consume_baseline + unlock_consume_required
ALTER TABLE user_affiliate_ledger
    ADD COLUMN IF NOT EXISTS unlock_consume_required DECIMAL(20,8) NULL,
    ADD COLUMN IF NOT EXISTS unlock_consume_baseline DECIMAL(20,8) NULL;

COMMENT ON COLUMN user_affiliate_ledger.unlock_consume_required IS '解冻所需的被邀请人消费额；NULL 表示已解冻或未冻结';
COMMENT ON COLUMN user_affiliate_ledger.unlock_consume_baseline IS '计提时被邀请人的 total_consumed 快照，用于计算增量消费';

-- 3) 解冻查询用的部分索引（只覆盖仍冻结的行）。
-- 解冻按 user_id（邀请人）过滤，故 user_id 放在首列；
-- source_user_id 跟随以便回查被邀请人的 total_consumed。
CREATE INDEX IF NOT EXISTS idx_ual_consume_unlock
    ON user_affiliate_ledger (user_id, source_user_id)
    WHERE unlock_consume_required IS NOT NULL;

-- 4) 存量迁移：把当前处于时间冻结中的返利转为消费解冻条件。
--
-- 需要还原「这笔返利对应多少充值额」：
--   a) 有 source_order_id 的，取 payment_orders.amount（准确值）；
--   b) 没有的（如兑换码/管理员充值），用 返利额 / 返利比例 反推。
-- baseline 取 0：total_consumed 是本次新增的列，所有用户从 0 起算，
-- 因此存量冻结返利需要被邀请人在迁移后新产生对应消费才解冻。
-- 返利比例取不到或非法时回退 20（AffiliateRebateRateDefault）。
WITH rate AS (
    SELECT COALESCE(
        (SELECT NULLIF(value, '')::numeric
         FROM settings
         WHERE key = 'affiliate_rebate_rate'
           AND value ~ '^[0-9]+(\.[0-9]+)?$'
           AND value::numeric > 0
         LIMIT 1),
        20
    ) AS pct
),
target AS (
    SELECT l.id,
           GREATEST(
               ROUND(
                   COALESCE(
                       (SELECT po.amount FROM payment_orders po WHERE po.id = l.source_order_id),
                       l.amount / (r.pct / 100)
                   )::numeric,
                   8
               ),
               0
           ) AS required
    FROM user_affiliate_ledger l
    CROSS JOIN rate r
    WHERE l.frozen_until IS NOT NULL
      AND l.unlock_consume_required IS NULL
)
UPDATE user_affiliate_ledger l
SET unlock_consume_required = t.required,
    unlock_consume_baseline = 0,
    frozen_until = NULL,
    updated_at = NOW()
FROM target t
WHERE l.id = t.id;

-- 5) 新增系统设置：解冻所需消费比例（百分比，0 = 不冻结立即可用）。
-- settings 表只有 id/key/value/updated_at（见 005_schema_parity.sql），无 created_at。
INSERT INTO settings (key, value)
VALUES ('affiliate_rebate_unlock_consume_percent', '100')
ON CONFLICT (key) DO NOTHING;
