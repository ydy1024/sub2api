package service

import (
	"context"
	"time"
)

type videoBillingTaskContextKey struct{}

func markVideoSubmissionStarted(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if task, _ := ctx.Value(videoBillingTaskContextKey{}).(*VideoBillingTask); task != nil {
		task.SubmissionStarted = true
	}
	return nil
}

func WithVideoBillingTask(ctx context.Context, task *VideoBillingTask) context.Context {
	return context.WithValue(ctx, videoBillingTaskContextKey{}, task)
}

func recordVideoSubmissionStatus(ctx context.Context, status int) {
	if task, _ := ctx.Value(videoBillingTaskContextKey{}).(*VideoBillingTask); task != nil {
		task.UpstreamStatus = status
	}
}

// persistVideoTaskID 在响应发送给客户端之前保存上游 ID。
func (s *OpenAIGatewayService) persistVideoTaskID(ctx context.Context, taskID string) error {
	task, _ := ctx.Value(videoBillingTaskContextKey{}).(*VideoBillingTask)
	if task == nil {
		return nil
	}
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = s.BindVideoBilling(storeCtx, task, taskID)
		if err == nil {
			return nil
		}
	}
	return err
}
