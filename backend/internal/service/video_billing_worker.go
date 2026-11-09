package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const videoBillingPollInterval = 15 * time.Second
const videoBillingTaskTimeout = time.Minute
const videoBillingWorkers = 8

func (s *OpenAIGatewayService) SetVideoBillingAuthCache(cache APIKeyAuthCacheInvalidator) {
	s.videoAuthCache = cache
}

type videoBillingWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (s *OpenAIGatewayService) StartVideoBilling() {
	if s.videoTaskRepo() == nil || s.videoWorker != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &videoBillingWorker{cancel: cancel, done: make(chan struct{})}
	s.videoWorker = w
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(videoBillingPollInterval)
		defer ticker.Stop()
		for {
			s.runVideoBillingBatch(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *OpenAIGatewayService) StopVideoBilling() {
	if s.videoWorker == nil {
		return
	}
	s.videoWorker.once.Do(s.videoWorker.cancel)
	<-s.videoWorker.done
}

func (s *OpenAIGatewayService) runVideoBillingBatch(ctx context.Context) {
	tasks, err := s.videoTaskRepo().ListDueVideoBillingTasks(ctx, videoBillingWorkers)
	if err != nil {
		slog.Warn("video billing recovery scan failed", "error", err)
		return
	}
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("video billing worker panic", "id", task.ID, "panic", recovered)
				}
			}()
			taskCtx, cancel := context.WithTimeout(ctx, videoBillingTaskTimeout)
			defer cancel()
			if err := s.settleVideoBillingTask(taskCtx, task); err != nil {
				slog.Warn("video billing task deferred", "id", task.ID, "error", err)
			}
			_ = s.videoTaskRepo().DeferVideoBillingTask(taskCtx, task.ID)
		}()
	}
	wg.Wait()
}

func (s *OpenAIGatewayService) settleVideoBillingTask(ctx context.Context, task *VideoBillingTask) error {
	if task.State == "refund_pending" {
		return s.FailVideoBilling(ctx, task)
	}
	result := task.Result
	if result == nil {
		var failed bool
		var err error
		result, failed, err = s.fetchVideoBillingResult(ctx, task)
		if err != nil {
			return err
		}
		if failed {
			return s.FailVideoBilling(ctx, task)
		}
		if result == nil {
			return nil
		}
		if err := s.videoTaskRepo().SaveVideoBillingResult(ctx, task.ID, result); err != nil {
			return err
		}
		// 并发轮询也可能已经保存终态，统一使用第一次持久化的结果。
		saved, err := s.FindVideoBillingTask(ctx, task.TaskID, task.UserID, task.APIKeyID)
		if err != nil || saved == nil || saved.Result == nil {
			return fmt.Errorf("reload completed video task: %w", err)
		}
		result = saved.Result
	}
	snapshot := &task.Snapshot
	cost := snapshot.Quote.Cost(result)
	input := &OpenAIRecordUsageInput{Result: result, APIKey: &snapshot.APIKey, User: &snapshot.User, Account: &snapshot.Account,
		Subscription: snapshot.Subscription, VideoTaskID: task.ID, VideoCost: cost, APIKeyService: videoTaskQuotaUpdater{},
		QuotaPlatform: snapshot.QuotaPlatform, InboundEndpoint: snapshot.InboundEndpoint, PricingAt: task.CreatedAt,
		RequestPayloadHash: HashUsageRequestPayload([]byte(task.TaskID))}
	if err := s.RecordUsage(ctx, input); err != nil {
		return err
	}
	s.invalidateVideoBalance(ctx, task.UserID)
	return s.videoTaskRepo().FinishVideoBillingTask(ctx, task.ID)
}

func (s *OpenAIGatewayService) CheckVideoTaskDeletion(ctx context.Context, task *VideoBillingTask) error {
	if task.State == "completed" || task.State == "failed" {
		return nil
	}
	if err := s.settleVideoBillingTask(ctx, task); err != nil {
		return err
	}
	current, err := s.FindVideoBillingTask(ctx, task.TaskID, task.UserID, task.APIKeyID)
	if err != nil {
		return err
	}
	if current == nil || (current.State != "completed" && current.State != "failed") {
		return fmt.Errorf("video task must reach a settled terminal state before deletion")
	}
	return nil
}

// 实际 Key 计数由 usageBillingRepo 的同一事务写入；此对象仅声明需要更新。
type videoTaskQuotaUpdater struct{}

func (videoTaskQuotaUpdater) UpdateQuotaUsed(context.Context, int64, float64) error      { return nil }
func (videoTaskQuotaUpdater) UpdateRateLimitUsage(context.Context, int64, float64) error { return nil }
