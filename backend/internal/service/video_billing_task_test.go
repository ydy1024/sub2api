package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVideoTaskQuotePreservesPriceAndDuration(t *testing.T) {
	svc := newOpenAIRecordUsageServiceForTest(&openAIRecordUsageLogRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	price := 0.1
	key := &APIKey{ID: 2, GroupID: i64p(1), Group: &Group{ID: 1, RateMultiplier: 2, VideoPrice720P: &price}}
	quote, err := svc.quoteVideoTask(context.Background(), key, &User{ID: 1}, GrokMediaRequestInfo{Model: "MX-SD-2.5-B", Resolution: "720p", DurationSeconds: 30}, false)
	require.NoError(t, err)
	price = 9
	cost := quote.Cost(&OpenAIForwardResult{VideoCount: 1, VideoDurationSeconds: 30})
	require.InDelta(t, 3.0, cost.TotalCost, 1e-8)
	require.InDelta(t, 6.0, cost.ActualCost, 1e-8)
}

func TestVideoTaskQuoteTokenSnapshotSurvivesReload(t *testing.T) {
	quote := VideoBillingQuote{Mode: BillingModeToken, Multiplier: 2, TokenPricing: &ModelPricing{OutputPricePerToken: .002}}
	body, err := json.Marshal(quote)
	require.NoError(t, err)
	var restored VideoBillingQuote
	require.NoError(t, json.Unmarshal(body, &restored))
	cost := restored.Cost(&OpenAIForwardResult{Usage: OpenAIUsage{OutputTokens: 1000}})
	require.Equal(t, 2.0, cost.TotalCost)
	require.Equal(t, 4.0, cost.ActualCost)
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
}

func TestVideoTaskSnapshotExcludesCredentials(t *testing.T) {
	key := &APIKey{ID: 2, Key: "sensitive-key", User: &User{ID: 1, PasswordHash: "sensitive-hash", Balance: 10}}
	account := &Account{ID: 3, Credentials: map[string]any{"api_key": "sensitive-upstream"}}
	task := newVideoBillingTask(VideoBillingAdmission{APIKey: key, Account: account, Info: GrokMediaRequestInfo{Model: "video", DurationSeconds: 30}}, VideoBillingQuote{Mode: BillingModeVideo, UnitActual: .1})
	body, err := json.Marshal(task.Snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(body), "sensitive-")
	require.Equal(t, 3.0, task.HoldAmount)
}

func TestVideoTaskRejectsAmbiguousProviderDefaults(t *testing.T) {
	for _, body := range []string{`{"model":"sora-2"}`, `{"seconds":"0","size":"720x1280"}`, `{"seconds":"10","resolution":"4k"}`} {
		require.Error(t, validateOpenAIVideoBillingParams("application/json", []byte(body)))
	}
	require.NoError(t, validateOpenAIVideoBillingParams("application/json", []byte(`{"seconds":"30","size":"720x1280"}`)))
}

func TestVideoTaskQuoteRejectsUnknownModel(t *testing.T) {
	svc := newOpenAIRecordUsageServiceForTest(&openAIRecordUsageLogRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	_, err := svc.quoteVideoTask(context.Background(), &APIKey{ID: 2}, &User{ID: 1}, GrokMediaRequestInfo{Model: "unknown-video", DurationSeconds: 30}, false)
	require.ErrorIs(t, err, ErrModelPricingUnavailable)
}

func TestVideoTaskKeepsRawUpstreamStatus(t *testing.T) {
	task := &VideoBillingTask{}
	ctx := WithVideoBillingTask(context.Background(), task)
	require.NoError(t, markVideoSubmissionStarted(ctx))
	recordVideoSubmissionStatus(ctx, 500)
	require.True(t, task.SubmissionStarted)
	require.Equal(t, 500, task.UpstreamStatus)
}
