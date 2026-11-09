CREATE TABLE IF NOT EXISTS video_billing_tasks (
    id TEXT PRIMARY KEY,
    task_id TEXT,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    group_id BIGINT,
    subscription_id BIGINT,
    hold_amount NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (hold_amount >= 0),
    state TEXT NOT NULL DEFAULT 'submitting'
        CHECK (state IN ('submitting', 'pending', 'refund_pending', 'settled', 'completed', 'failed')),
    snapshot JSONB NOT NULL,
    result JSONB,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (api_key_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_video_billing_tasks_due
    ON video_billing_tasks(next_attempt_at) WHERE state IN ('pending', 'refund_pending', 'settled');
CREATE INDEX IF NOT EXISTS idx_video_billing_tasks_user_hold
    ON video_billing_tasks(user_id) WHERE state IN ('submitting', 'pending', 'refund_pending');
