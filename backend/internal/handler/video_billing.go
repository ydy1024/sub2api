package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func sameOptionalGroupID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (h *OpenAIGatewayHandler) checkGrokMediaBilling(c *gin.Context, key *service.APIKey, sub *service.UserSubscription, endpoint service.GrokMediaEndpoint) error {
	if endpoint.IsVideoLookupRequest() {
		return nil
	}
	return h.billingCacheService.CheckBillingEligibility(c.Request.Context(), key.User, key, key.Group, sub, service.QuotaPlatform(c.Request.Context(), key))
}

func (h *OpenAIGatewayHandler) handleVideoCreateFailure(c *gin.Context, parent context.Context, task *service.VideoBillingTask, err error) {
	status := task.UpstreamStatus
	// 网络错误或 5xx 可能已创建任务，不能退款后再次生成。保留 submitting 供核对。
	if !task.SubmissionStarted || (status >= 400 && status < 500) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
		defer cancel()
		if refundErr := h.gatewayService.FailVideoBilling(ctx, task); refundErr != nil {
			slog.Error("video reservation refund failed", "id", task.ID, "error", refundErr)
		}
	} else {
		slog.Error("video creation outcome unknown; reservation retained", "id", task.ID, "error", err)
	}
	if !service.IsResponseCommitted(c) {
		h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Video creation could not be confirmed")
	}
}
