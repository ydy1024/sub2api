//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRegressionVideoLongDuration(t *testing.T) {
	info := ParseGrokMediaRequest("application/json", []byte(`{"model":"MX-SD-2.5-B","seconds":"30","resolution":"720p"}`))
	price := 0.1
	cost := (&BillingService{}).CalculateVideoCost(info.Model, info.Resolution, 1, info.DurationSeconds, &VideoPriceConfig{Price720P: &price}, 1)
	t.Logf("requested=30s, parsed=%ds, billed=$%.2f, expected=$3.00", info.DurationSeconds, cost.ActualCost)
	require.Equal(t, 30, info.DurationSeconds)
	require.InDelta(t, 3.0, cost.ActualCost, 1e-8)
}

func TestRegressionVideoMultipartSeconds(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("model", "sora-2"))
	require.NoError(t, w.WriteField("seconds", "12"))
	require.NoError(t, w.WriteField("size", "720x1280"))
	require.NoError(t, w.Close())
	info := ParseGrokMediaRequest(w.FormDataContentType(), body.Bytes())
	t.Logf("requested seconds=12, size=720x1280; parsed duration=%d, resolution=%s", info.DurationSeconds, info.Resolution)
	require.Equal(t, 12, info.DurationSeconds)
	require.Equal(t, "720p", info.Resolution)
}

func TestRegressionVideoContentDeliveredWithoutBilling(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"id":"task-1","status":"completed","model":"sora-2","seconds":"12","size":"720x1280"}`),
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"video/mp4"}}, Body: io.NopCloser(bytes.NewBufferString("video-payload"))},
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := grokMediaContentTestAccount()
	account.Platform = PlatformOpenAI
	account.Credentials[GrokMediaAPIStyleCredential] = GrokMediaAPIStyleOpenAIVideo
	c, response := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task-1/content", nil)
	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideoContent, "task-1", nil, "")
	require.NoError(t, err)
	require.Equal(t, "video-payload", response.Body.String())
	t.Logf("delivered=%q, billable video count=%d", response.Body.String(), result.VideoCount)
	require.Equal(t, 1, result.VideoCount)
}

type auditDedupeUsageRepo struct {
	UsageLogRepository
	row *UsageLog
}

func (r *auditDedupeUsageRepo) Create(_ context.Context, row *UsageLog) (bool, error) {
	// Matches usage_logs SQL: ON CONFLICT (request_id, api_key_id) DO NOTHING.
	if r.row != nil && !(row.BillingSettled && r.row.ActualCost == 0) {
		return false, nil
	}
	copy := *row
	r.row = &copy
	return true, nil
}

func TestRegressionBillingRetryKeepsUnsettledLog(t *testing.T) {
	usage := &auditDedupeUsageRepo{}
	billing := &openAIRecordUsageBillingRepoStub{err: errors.New("transient billing transaction failure")}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usage, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	input := &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "grok-video:audit-task", Model: "grok-imagine-video", VideoCount: 1, VideoDurationSeconds: 10, VideoResolution: "720p"},
		APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 3},
	}
	require.Error(t, svc.RecordUsage(context.Background(), input))
	require.Zero(t, usage.row.ActualCost)
	billing.err = nil
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	require.Positive(t, billing.lastCmd.BalanceCost)
	t.Logf("successful retry charged=$%.3f, stored usage ActualCost=$%.3f", billing.lastCmd.BalanceCost, usage.row.ActualCost)
	require.InDelta(t, billing.lastCmd.BalanceCost, usage.row.ActualCost, 1e-8)
}

func TestRegressionVideoUnknownPricingRejected(t *testing.T) {
	svc := newOpenAIRecordUsageServiceForTest(&openAIRecordUsageLogRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{Model: "MX-SD-2.5-B", VideoCount: 1, VideoDurationSeconds: 10, VideoResolution: "720p"},
		APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 3},
	})
	require.Error(t, err, "新视频模型没有明确价格时不能回退到图片价格")
}
