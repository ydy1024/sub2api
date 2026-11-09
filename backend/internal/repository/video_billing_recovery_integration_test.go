//go:build integration

package repository

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type videoRecoveryUpstream struct{ service.HTTPUpstream }

func (videoRecoveryUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"recovery-task","status":"completed","seconds":"30","size":"720x1280"}`)), Header: http.Header{}}, nil
}

func TestVideoRecoveryAfterServiceRestart(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-upstream", "base_url": "https://relay.example", "video_api_style": "openai_videos"}})
	repo := &usageBillingRepository{db: integrationDB}
	task := &service.VideoBillingTask{ID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, HoldAmount: 3,
		Snapshot: service.VideoBillingSnapshot{APIKey: service.APIKey{ID: key.ID, Group: &service.Group{RateMultiplier: 1, VideoRateMultiplier: 1}}, User: service.User{ID: user.ID}, Account: service.Account{ID: account.ID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey},
			Pending: service.GrokVideoPendingBilling{Model: "custom-video", BillingModel: "custom-video", VideoDurationSeconds: 30, VideoResolution: "720p"},
			Quote:   service.VideoBillingQuote{Mode: service.BillingModeVideo, UnitTotal: .1, UnitActual: .1, Multiplier: 1}}}
	require.NoError(t, repo.CreateVideoBillingTask(ctx, task))
	require.NoError(t, repo.BindVideoBillingTask(ctx, task.ID, "recovery-task"))
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	users := NewUserRepository(client, integrationDB)
	billingCache := service.NewBillingCacheService(nil, users, nil, nil, nil, nil, cfg, nil)
	defer billingCache.Stop()
	gateway := service.NewOpenAIGatewayService(NewAccountRepository(client, integrationDB, nil), NewUsageLogRepository(client, integrationDB), repo, users, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billingCache, videoRecoveryUpstream{}, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	gateway.StartVideoBilling()
	defer gateway.StopVideoBilling()
	require.Eventually(t, func() bool {
		var state string
		err := integrationDB.QueryRow("SELECT state FROM video_billing_tasks WHERE id=$1", task.ID).Scan(&state)
		return err == nil && state == "completed"
	}, 5*time.Second, 20*time.Millisecond)
	var balance, frozen, actual float64
	require.NoError(t, integrationDB.QueryRow("SELECT balance,frozen_balance FROM users WHERE id=$1", user.ID).Scan(&balance, &frozen))
	require.NoError(t, integrationDB.QueryRow("SELECT actual_cost FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", "grok-video:recovery-task", key.ID).Scan(&actual))
	require.Equal(t, 7.0, balance)
	require.Zero(t, frozen)
	require.Equal(t, 3.0, actual)
}
