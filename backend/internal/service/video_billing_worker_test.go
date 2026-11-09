//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type videoWorkerRepo struct {
	UsageBillingRepository
	VideoBillingTaskRepository
	task     *VideoBillingTask
	dedupe   bool
	charged  float64
	finished int
	failed   int
}

func (r *videoWorkerRepo) Apply(_ context.Context, c *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	if r.dedupe {
		return &UsageBillingApplyResult{}, nil
	}
	r.dedupe = true
	r.charged += c.BalanceCost
	return &UsageBillingApplyResult{Applied: true}, nil
}
func (r *videoWorkerRepo) SaveVideoBillingResult(_ context.Context, _ string, v *OpenAIForwardResult) error {
	r.task.Result = v
	return nil
}
func (r *videoWorkerRepo) GetVideoBillingTask(context.Context, string, int64, int64) (*VideoBillingTask, error) {
	return r.task, nil
}
func (r *videoWorkerRepo) FinishVideoBillingTask(context.Context, string) error {
	r.finished++
	return nil
}
func (r *videoWorkerRepo) FailVideoBillingTask(context.Context, string) error { r.failed++; return nil }

func TestVideoWorkerRetriesLogWithoutChargingAgain(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{err: errors.New("log storage unavailable")}
	repo := &videoWorkerRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	task := &VideoBillingTask{ID: "job", TaskID: "task", UserID: 1, APIKeyID: 2, AccountID: 3, CreatedAt: time.Now(),
		Snapshot: VideoBillingSnapshot{APIKey: APIKey{ID: 2, Group: &Group{RateMultiplier: 1, VideoRateMultiplier: 1}}, User: User{ID: 1}, Account: Account{ID: 3},
			Quote: VideoBillingQuote{Mode: BillingModeVideo, UnitTotal: .1, UnitActual: .1, Multiplier: 1}},
		Result: &OpenAIForwardResult{RequestID: "grok-video:task", ResponseID: "task", Model: "custom-video", VideoCount: 1, VideoDurationSeconds: 30, VideoResolution: "720p"}}
	require.Error(t, svc.settleVideoBillingTask(context.Background(), task))
	require.Equal(t, 3.0, repo.charged)
	require.Zero(t, repo.finished)
	logs.err = nil
	require.NoError(t, svc.settleVideoBillingTask(context.Background(), task))
	require.Equal(t, 3.0, repo.charged)
	require.Equal(t, 1, repo.finished)
	require.Equal(t, 3.0, logs.lastLog.ActualCost)
}

func TestVideoWorkerPollsWithoutClient(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{}
	repo := &videoWorkerRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	account := grokMediaContentTestAccount()
	account.Platform = PlatformOpenAI
	account.Credentials[GrokMediaAPIStyleCredential] = GrokMediaAPIStyleOpenAIVideo
	svc.accountRepo = &openAIRecordUsageAccountRepoStub{account: account}
	svc.httpUpstream = &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"id":"task","status":"completed","seconds":"30","size":"720x1280"}`)}
	repo.task = &VideoBillingTask{ID: "job", TaskID: "task", UserID: 1, APIKeyID: 2, AccountID: 3, CreatedAt: time.Now(),
		Snapshot: VideoBillingSnapshot{APIKey: APIKey{ID: 2, Group: &Group{RateMultiplier: 1, VideoRateMultiplier: 1}}, User: User{ID: 1}, Account: Account{ID: 3},
			Pending: GrokVideoPendingBilling{Model: "custom-video", BillingModel: "custom-video", VideoDurationSeconds: 30, VideoResolution: "720p"},
			Quote:   VideoBillingQuote{Mode: BillingModeVideo, UnitTotal: .1, UnitActual: .1, Multiplier: 1}}}
	require.NoError(t, svc.settleVideoBillingTask(context.Background(), repo.task))
	require.Equal(t, 3.0, repo.charged)
	require.Equal(t, 1, repo.finished)
	require.Equal(t, "custom-video", logs.lastLog.Model)
}

func TestVideoWorkerFailedStatusReleasesHold(t *testing.T) {
	repo := &videoWorkerRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{}, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.accountRepo = &openAIRecordUsageAccountRepoStub{account: grokMediaContentTestAccount()}
	svc.httpUpstream = &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"status":"failed"}`)}
	require.NoError(t, svc.settleVideoBillingTask(context.Background(), &VideoBillingTask{ID: "failed", TaskID: "task", AccountID: 9}))
	require.Equal(t, 1, repo.failed)
	require.Zero(t, repo.charged)
}

func TestVideoWorkerRetriesPersistedRefundWithoutUpstream(t *testing.T) {
	repo := &videoWorkerRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{}, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	require.NoError(t, svc.settleVideoBillingTask(context.Background(), &VideoBillingTask{ID: "refund", State: "refund_pending"}))
	require.Equal(t, 1, repo.failed)
	require.Zero(t, repo.charged)
}
