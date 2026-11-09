//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestVideoTaskConcurrentReservationsAndSettlement(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	repo := &usageBillingRepository{db: integrationDB}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	tasks := make([]service.VideoBillingTask, 8)
	for i := range tasks {
		tasks[i] = service.VideoBillingTask{ID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, HoldAmount: 6}
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = repo.CreateVideoBillingTask(ctx, &tasks[i]) }()
	}
	wg.Wait()
	var winner *service.VideoBillingTask
	for i, err := range errs {
		if err == nil {
			require.Nil(t, winner)
			winner = &tasks[i]
		}
	}
	require.NotNil(t, winner)
	require.NoError(t, repo.BindVideoBillingTask(ctx, winner.ID, "task-"+winner.ID))
	results := make([]*service.UsageBillingApplyResult, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = repo.Apply(ctx, &service.UsageBillingCommand{
				RequestID: "video:" + winner.ID, VideoTaskID: winner.ID, UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, BalanceCost: 5,
			})
		}()
	}
	wg.Wait()
	applied := 0
	for i, result := range results {
		require.NoError(t, errs[i])
		if result.Applied {
			applied++
		}
	}
	require.Equal(t, 1, applied)
	var balance, frozen float64
	require.NoError(t, integrationDB.QueryRow("SELECT balance,frozen_balance FROM users WHERE id=$1", user.ID).Scan(&balance, &frozen))
	require.Equal(t, 5.0, balance)
	require.Zero(t, frozen)
}

func TestVideoTaskSubscriptionHoldAndSettlement(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Balance: 10})
	limit := 10.0
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformOpenAI, SubscriptionType: service.SubscriptionTypeSubscription, DailyLimitUSD: &limit})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	var subID int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO user_subscriptions(user_id,group_id,starts_at,expires_at,status) VALUES($1,$2,NOW(),NOW()+INTERVAL '1 month','active') RETURNING id`, user.ID, group.ID).Scan(&subID))
	repo := &usageBillingRepository{db: integrationDB}
	task := &service.VideoBillingTask{ID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, SubscriptionID: &subID, HoldAmount: 6}
	require.NoError(t, repo.CreateVideoBillingTask(ctx, task))
	other := *task
	other.ID = uuid.NewString()
	require.Error(t, repo.CreateVideoBillingTask(ctx, &other))
	require.NoError(t, repo.BindVideoBillingTask(ctx, task.ID, "task-"+task.ID))
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{RequestID: "video:" + task.ID, VideoTaskID: task.ID, UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, SubscriptionID: &subID, SubscriptionCost: 5})
	require.NoError(t, err)
	var balance, usage float64
	require.NoError(t, integrationDB.QueryRow("SELECT balance FROM users WHERE id=$1", user.ID).Scan(&balance))
	require.NoError(t, integrationDB.QueryRow("SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1", subID).Scan(&usage))
	require.Equal(t, 10.0, balance)
	require.Equal(t, 5.0, usage)
}
