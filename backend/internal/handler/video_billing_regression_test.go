//go:build unit

package handler

import (
	"context"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRegressionVideoPendingModelSurvivesStatusExtraction(t *testing.T) {
	h, _, _, _ := newGrokMediaSlotHandler(t, false, false)
	c, _ := grokMediaSlotContext(context.Background(), false)
	key, _ := middleware.GetAPIKeyFromContext(c)
	subject := middleware.AuthSubject{UserID: 10, Concurrency: 5}
	require.NoError(t, h.gatewayService.StoreGrokVideoPendingBilling(context.Background(), "task", subject.UserID, key.ID, service.GrokVideoPendingBilling{
		Model: "MX-SD-2.5-B", BillingModel: "MX-SD-2.5-B", VideoDurationSeconds: 10, VideoResolution: "720p",
	}))
	result := service.ExtractGrokVideoBillingFromStatusBody([]byte(`{"id":"task","status":"completed","video_url":"https://relay.example/v1/videos/task/content"}`), nil, "task")
	require.NotNil(t, result)
	bill := prepareGrokVideoCompletionBilling(context.Background(), h, zap.NewNop(), key, subject, "task", result)
	require.NotNil(t, bill)
	t.Logf("create billing model=MX-SD-2.5-B; final billing model=%s", bill.BillingModel)
	require.Equal(t, "MX-SD-2.5-B", bill.BillingModel)
}
