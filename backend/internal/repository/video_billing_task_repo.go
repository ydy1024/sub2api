package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *usageBillingRepository) CreateVideoBillingTask(ctx context.Context, task *service.VideoBillingTask) error {
	if task == nil || task.ID == "" || task.UserID <= 0 || task.APIKeyID <= 0 || task.AccountID <= 0 || task.HoldAmount < 0 || math.IsNaN(task.HoldAmount) || math.IsInf(task.HoldAmount, 0) {
		return errors.New("invalid video billing task")
	}
	payload, err := json.Marshal(task.Snapshot)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var balance float64
	if err := tx.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", task.UserID).Scan(&balance); err != nil {
		return err
	}
	task.HoldAmount = service.QuantizeUsageBillingAmount(task.HoldAmount)
	if err := checkVideoTaskLimits(ctx, tx, task); err != nil {
		return err
	}
	if task.SubscriptionID == nil && task.HoldAmount > 0 {
		if balance < task.HoldAmount {
			return service.ErrInsufficientBalance
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance-$1, frozen_balance=COALESCE(frozen_balance,0)+$1 WHERE id=$2`, task.HoldAmount, task.UserID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO video_billing_tasks
		(id,user_id,api_key_id,account_id,group_id,subscription_id,hold_amount,snapshot)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, task.ID, task.UserID, task.APIKeyID, task.AccountID, task.GroupID, task.SubscriptionID, task.HoldAmount, payload)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *usageBillingRepository) BindVideoBillingTask(ctx context.Context, id, taskID string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE video_billing_tasks SET task_id=$2,state='pending',updated_at=NOW()
		WHERE id=$1 AND (state='submitting' OR (state='pending' AND task_id=$2))`, id, taskID)
	return requireVideoTaskUpdate(res, err)
}

const videoTaskColumns = `id,COALESCE(task_id,''),user_id,api_key_id,account_id,group_id,subscription_id,hold_amount,state,created_at,snapshot,result`

func readVideoTask(row interface{ Scan(...any) error }) (*service.VideoBillingTask, error) {
	task := &service.VideoBillingTask{}
	var payload []byte
	var result []byte
	err := row.Scan(&task.ID, &task.TaskID, &task.UserID, &task.APIKeyID, &task.AccountID, &task.GroupID, &task.SubscriptionID, &task.HoldAmount, &task.State, &task.CreatedAt, &payload, &result)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(payload, &task.Snapshot); err != nil {
		return nil, err
	}
	if len(result) > 0 {
		if err = json.Unmarshal(result, &task.Result); err != nil {
			return nil, err
		}
	}
	return task, nil
}

func (r *usageBillingRepository) GetVideoBillingTask(ctx context.Context, taskID string, userID, apiKeyID int64) (*service.VideoBillingTask, error) {
	task, err := readVideoTask(r.db.QueryRowContext(ctx, `SELECT `+videoTaskColumns+` FROM video_billing_tasks WHERE task_id=$1 AND user_id=$2 AND api_key_id=$3`, taskID, userID, apiKeyID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return task, err
}

func (r *usageBillingRepository) ListDueVideoBillingTasks(ctx context.Context, limit int) ([]*service.VideoBillingTask, error) {
	rows, err := r.db.QueryContext(ctx, `WITH due AS (
		SELECT id FROM video_billing_tasks WHERE state IN ('pending','refund_pending','settled') AND next_attempt_at<=NOW()
		ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED
	) UPDATE video_billing_tasks t SET next_attempt_at=NOW()+INTERVAL '2 minutes'
	FROM due WHERE t.id=due.id RETURNING t.id,COALESCE(t.task_id,''),t.user_id,t.api_key_id,t.account_id,t.group_id,t.subscription_id,t.hold_amount,t.state,t.created_at,t.snapshot,t.result`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*service.VideoBillingTask
	for rows.Next() {
		task, err := readVideoTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *usageBillingRepository) SaveVideoBillingResult(ctx context.Context, id string, result *service.OpenAIForwardResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE video_billing_tasks SET result=$2,updated_at=NOW() WHERE id=$1 AND result IS NULL AND state='pending'`, id, payload)
	return err
}

func (r *usageBillingRepository) DeferVideoBillingTask(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE video_billing_tasks SET next_attempt_at=NOW()+INTERVAL '15 seconds' WHERE id=$1 AND state IN ('pending','refund_pending','settled')`, id)
	return err
}

func (r *usageBillingRepository) FinishVideoBillingTask(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE video_billing_tasks SET state='completed',updated_at=NOW() WHERE id=$1 AND state IN ('settled','completed')`, id)
	return requireVideoTaskUpdate(res, err)
}

func (r *usageBillingRepository) FailVideoBillingTask(ctx context.Context, id string) error {
	// 先持久化失败决定，后续退款事务即使失败也可由后台继续重试。
	if _, err := r.db.ExecContext(ctx, `UPDATE video_billing_tasks SET state='refund_pending',next_attempt_at=NOW(),updated_at=NOW() WHERE id=$1 AND state IN ('submitting','pending') AND result IS NULL`, id); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID int64
	var subID *int64
	var hold float64
	var state string
	var hasResult bool
	err = tx.QueryRowContext(ctx, `SELECT user_id,subscription_id,hold_amount,state,result IS NOT NULL FROM video_billing_tasks WHERE id=$1 FOR UPDATE`, id).Scan(&userID, &subID, &hold, &state, &hasResult)
	if err != nil {
		return err
	}
	if state == "failed" {
		return nil
	}
	if hasResult || state != "refund_pending" {
		return errors.New("video task already settled")
	}
	if subID == nil && hold > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1,frozen_balance=frozen_balance-$1 WHERE id=$2 AND frozen_balance >= $1`, hold, userID)
		if err := requireVideoTaskUpdate(res, err); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE video_billing_tasks SET state='failed',updated_at=NOW() WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func requireVideoTaskUpdate(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return errors.New("video task transition rejected")
	}
	return err
}
