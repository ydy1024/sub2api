//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestVideoTaskReserveSettleAndRetry(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("video-%s@example.com", uuid.NewString()), PasswordHash: "hash", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "video"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Type: service.AccountTypeAPIKey})
	repo := &usageBillingRepository{db: integrationDB}
	task := &service.VideoBillingTask{ID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, HoldAmount: 6,
		Snapshot: service.VideoBillingSnapshot{APIKey: service.APIKey{ID: key.ID}, User: service.User{ID: user.ID}}, CreatedAt: time.Now()}
	require.NoError(t, repo.CreateVideoBillingTask(ctx, task))
	var balance, frozen float64
	readBalance := func() {
		require.NoError(t, integrationDB.QueryRow("SELECT balance, frozen_balance FROM users WHERE id=$1", user.ID).Scan(&balance, &frozen))
	}
	readBalance()
	require.Equal(t, 4.0, balance)
	require.Equal(t, 6.0, frozen)
	other := *task
	other.ID = uuid.NewString()
	require.Error(t, repo.CreateVideoBillingTask(ctx, &other))
	require.NoError(t, repo.BindVideoBillingTask(ctx, task.ID, "video-"+task.ID))
	loaded, err := repo.GetVideoBillingTask(ctx, "video-"+task.ID, user.ID, key.ID)
	require.NoError(t, err)
	require.Equal(t, task.ID, loaded.ID)
	missing, err := repo.GetVideoBillingTask(ctx, "video-"+task.ID, user.ID+1, key.ID)
	require.NoError(t, err)
	require.Nil(t, missing)
	due, err := repo.ListDueVideoBillingTasks(ctx, 10)
	require.NoError(t, err)
	require.NotEmpty(t, due)
	due, err = repo.ListDueVideoBillingTasks(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, due)
	cmd := &service.UsageBillingCommand{RequestID: "grok-video:" + task.ID, APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, VideoTaskID: task.ID, BalanceCost: 5}
	result, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	readBalance()
	require.Equal(t, 5.0, balance)
	require.Zero(t, frozen)
	result, err = repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.Error(t, repo.FailVideoBillingTask(ctx, task.ID))
	readBalance()
	require.Equal(t, 5.0, balance)

	// 失败任务只能退款一次。
	refund := *task
	refund.ID, refund.HoldAmount = uuid.NewString(), 2
	require.NoError(t, repo.CreateVideoBillingTask(ctx, &refund))
	require.NoError(t, repo.FailVideoBillingTask(ctx, refund.ID))
	require.NoError(t, repo.FailVideoBillingTask(ctx, refund.ID))
	readBalance()
	require.Equal(t, 5.0, balance)
	require.Zero(t, frozen)
}

func TestVideoBillingSettledLogRepairsZeroCost(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	repo := newUsageLogRepositoryWithSQL(client, integrationDB)
	row := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "grok-imagine-video", TotalCost: 1, RateMultiplier: 1, CreatedAt: time.Now()}
	_, err := repo.Create(ctx, row)
	require.NoError(t, err)
	row.BillingSettled, row.ActualCost = true, 1
	require.NoError(t, repo.CreateBestEffort(ctx, row))
	var amount float64
	require.NoError(t, integrationDB.QueryRow("SELECT actual_cost FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", row.RequestID, key.ID).Scan(&amount))
	require.Equal(t, 1.0, amount)
	row.ActualCost = 2
	_, err = repo.Create(ctx, row)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRow("SELECT actual_cost FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", row.RequestID, key.ID).Scan(&amount))
	require.Equal(t, 1.0, amount)
}
