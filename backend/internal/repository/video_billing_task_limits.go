package repository

import (
	"context"
	"database/sql"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func checkVideoTaskLimits(ctx context.Context, tx *sql.Tx, task *service.VideoBillingTask) error {
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT
		(k.quota<=0 OR k.quota_used+p.amount+$2<=k.quota) AND
		(k.rate_limit_5h<=0 OR (CASE WHEN k.window_5h_start+INTERVAL '5 hours'>NOW() THEN k.usage_5h ELSE 0 END)+p.amount+$2<=k.rate_limit_5h) AND
		(k.rate_limit_1d<=0 OR (CASE WHEN k.window_1d_start+INTERVAL '24 hours'>NOW() THEN k.usage_1d ELSE 0 END)+p.amount+$2<=k.rate_limit_1d) AND
		(k.rate_limit_7d<=0 OR (CASE WHEN k.window_7d_start+INTERVAL '7 days'>NOW() THEN k.usage_7d ELSE 0 END)+p.amount+$2<=k.rate_limit_7d)
		FROM api_keys k CROSS JOIN (SELECT COALESCE(SUM(hold_amount),0) amount FROM video_billing_tasks WHERE api_key_id=$1 AND state IN ('submitting','pending','refund_pending')) p
		WHERE k.id=$1 AND k.deleted_at IS NULL`, task.APIKeyID, task.HoldAmount).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return service.ErrAPIKeyQuotaExhausted
	}
	if task.SubscriptionID == nil {
		return nil
	}
	err = tx.QueryRowContext(ctx, `SELECT
		(g.daily_limit_usd IS NULL OR g.daily_limit_usd<=0 OR s.daily_usage_usd+p.amount+$2<=g.daily_limit_usd) AND
		(g.weekly_limit_usd IS NULL OR g.weekly_limit_usd<=0 OR s.weekly_usage_usd+p.amount+$2<=g.weekly_limit_usd) AND
		(g.monthly_limit_usd IS NULL OR g.monthly_limit_usd<=0 OR s.monthly_usage_usd+p.amount+$2<=g.monthly_limit_usd)
		FROM user_subscriptions s JOIN groups g ON g.id=s.group_id
		CROSS JOIN (SELECT COALESCE(SUM(hold_amount),0) amount FROM video_billing_tasks WHERE subscription_id=$1 AND state IN ('submitting','pending','refund_pending')) p
		WHERE s.id=$1 AND s.deleted_at IS NULL`, *task.SubscriptionID, task.HoldAmount).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return service.ErrDailyLimitExceeded
	}
	return nil
}

// settleVideoTaskHold 与扣费共用事务，释放预留后立即应用实际金额。
func settleVideoTaskHold(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	if cmd.VideoTaskID == "" {
		return nil
	}
	var hold float64
	var subID *int64
	err := tx.QueryRowContext(ctx, `UPDATE video_billing_tasks SET state='settled',updated_at=NOW()
		WHERE id=$1 AND user_id=$2 AND api_key_id=$3 AND state='pending' RETURNING hold_amount,subscription_id`, cmd.VideoTaskID, cmd.UserID, cmd.APIKeyID).Scan(&hold, &subID)
	if err != nil {
		return err
	}
	if subID != nil || hold <= 0 {
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1,frozen_balance=frozen_balance-$1 WHERE id=$2 AND frozen_balance>=$1`, hold, cmd.UserID)
	return requireVideoTaskUpdate(res, err)
}
