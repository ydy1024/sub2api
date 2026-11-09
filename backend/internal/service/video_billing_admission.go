package service

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type VideoBillingAdmission struct {
	APIKey          *APIKey
	Account         *Account
	Subscription    *UserSubscription
	Info            GrokMediaRequestInfo
	Endpoint        GrokMediaEndpoint
	InboundEndpoint string
	QuotaPlatform   string
	ContentType     string
	Body            []byte
}

func (s *OpenAIGatewayService) videoTaskRepo() VideoBillingTaskRepository {
	repo, _ := s.usageBillingRepo.(VideoBillingTaskRepository)
	return repo
}

// PrepareVideoBilling 在发送上游请求之前固化价格并预留资金。
func (s *OpenAIGatewayService) PrepareVideoBilling(ctx context.Context, in VideoBillingAdmission) (*VideoBillingTask, error) {
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil, nil
	}
	if in.APIKey == nil || in.APIKey.User == nil || in.Account == nil {
		return nil, fmt.Errorf("video billing context is incomplete")
	}
	if s.videoTaskRepo() == nil {
		return nil, fmt.Errorf("durable video billing unavailable")
	}
	if usesOpenAIVideoMediaAPI(in.Account) && !in.Endpoint.IsSeedance() {
		if err := validateOpenAIVideoBillingParams(in.ContentType, in.Body); err != nil {
			return nil, err
		}
	}
	in.APIKey = s.apiKeyWithFreshGroupMediaPricing(ctx, in.APIKey)
	quote, err := s.quoteVideoTask(ctx, in.APIKey, in.APIKey.User, in.Info, in.Endpoint.IsSeedance())
	if err != nil {
		return nil, err
	}
	task := newVideoBillingTask(in, quote)
	if err := s.videoTaskRepo().CreateVideoBillingTask(ctx, task); err != nil {
		return nil, err
	}
	s.invalidateVideoBalance(ctx, task.UserID)
	return task, nil
}

func validateOpenAIVideoBillingParams(contentType string, body []byte) error {
	var info GrokMediaRequestInfo
	if gjson.ValidBytes(body) {
		parseGrokMediaJSONRequest(body, &info)
	} else {
		parseGrokMediaMultipartRequest(contentType, body, &info)
	}
	if info.DurationSeconds <= 0 {
		return fmt.Errorf("video seconds or duration must be explicitly positive")
	}
	resolution := info.Resolution
	if resolution == "" {
		resolution = videoResolutionFromSize(info.Size)
	}
	if _, ok := LookupVideoBillingResolution(resolution); !ok {
		return fmt.Errorf("video resolution or size must specify a supported billing resolution")
	}
	return nil
}

func newVideoBillingTask(in VideoBillingAdmission, quote VideoBillingQuote) *VideoBillingTask {
	key := *in.APIKey
	key.Key, key.User, key.Group = "", nil, nil
	group := &Group{RateMultiplier: quote.Multiplier, VideoRateIndependent: true, VideoRateMultiplier: quote.Multiplier}
	if in.APIKey.Group != nil {
		group.ID, group.Platform, group.SubscriptionType = in.APIKey.Group.ID, in.APIKey.Group.Platform, in.APIKey.Group.SubscriptionType
	}
	key.Group = group
	accountRate := in.Account.BillingRateMultiplier()
	account := Account{ID: in.Account.ID, Platform: in.Account.Platform, Type: in.Account.Type, RateMultiplier: &accountRate,
		Extra: map[string]any{"quota_limit": in.Account.GetQuotaLimit(), "quota_daily_limit": in.Account.GetQuotaDailyLimit(), "quota_weekly_limit": in.Account.GetQuotaWeeklyLimit()}}
	task := &VideoBillingTask{ID: uuid.NewString(), UserID: in.APIKey.User.ID, APIKeyID: key.ID, AccountID: account.ID, GroupID: in.APIKey.GroupID, CreatedAt: time.Now()}
	task.Snapshot = VideoBillingSnapshot{APIKey: key, User: User{ID: task.UserID}, Account: account, Quote: quote, Seedance: in.Endpoint.IsSeedance(), QuotaPlatform: in.QuotaPlatform, InboundEndpoint: in.InboundEndpoint}
	task.Snapshot.Pending = GrokVideoPendingBilling{Model: in.Info.Model, BillingModel: in.Info.Model, UpstreamModel: in.Account.GetMappedModel(in.Info.Model), VideoResolution: in.Info.Resolution, VideoDurationSeconds: in.Info.DurationSeconds, CreatedAt: GrokVideoPendingCreatedAtNow()}
	if in.Subscription != nil && group.IsSubscriptionType() {
		task.SubscriptionID = &in.Subscription.ID
		task.Snapshot.Subscription = &UserSubscription{ID: in.Subscription.ID, UserID: task.UserID, GroupID: group.ID}
	}
	task.HoldAmount = quote.Cost(&OpenAIForwardResult{VideoDurationSeconds: in.Info.DurationSeconds, VideoResolution: in.Info.Resolution}).ActualCost
	if quote.Mode == BillingModeToken {
		// 原生视频无可靠 token 上限：预留当前余额，禁止同一资金重复提交异步任务。
		task.HoldAmount = math.Max(0, in.APIKey.User.Balance)
		if task.SubscriptionID != nil {
			task.HoldAmount = videoSubscriptionHeadroom(in.APIKey.Group, in.Subscription)
		}
		task.HoldAmount = videoKeyHeadroom(in.APIKey, task.HoldAmount)
	}
	return task
}

func videoKeyHeadroom(key *APIKey, amount float64) float64 {
	for _, pair := range []struct{ limit, used float64 }{{key.Quota, key.QuotaUsed}, {key.RateLimit5h, key.Usage5h}, {key.RateLimit1d, key.Usage1d}, {key.RateLimit7d, key.Usage7d}} {
		if pair.limit > 0 {
			amount = math.Min(amount, math.Max(0, pair.limit-pair.used))
		}
	}
	return amount
}

func videoSubscriptionHeadroom(group *Group, sub *UserSubscription) float64 {
	headroom := 0.0
	found := false
	for _, pair := range []struct {
		limit *float64
		used  float64
	}{{group.DailyLimitUSD, sub.DailyUsageUSD}, {group.WeeklyLimitUSD, sub.WeeklyUsageUSD}, {group.MonthlyLimitUSD, sub.MonthlyUsageUSD}} {
		if pair.limit != nil && *pair.limit > 0 {
			remaining := math.Max(0, *pair.limit-pair.used)
			if !found || remaining < headroom {
				headroom = remaining
				found = true
			}
		}
	}
	return headroom
}

func (s *OpenAIGatewayService) BindVideoBilling(ctx context.Context, task *VideoBillingTask, taskID string) error {
	if task == nil {
		return nil
	}
	if taskID == "" {
		return fmt.Errorf("video create response has no task id; reservation retained for reconciliation")
	}
	return s.videoTaskRepo().BindVideoBillingTask(ctx, task.ID, taskID)
}

func (s *OpenAIGatewayService) FailVideoBilling(ctx context.Context, task *VideoBillingTask) error {
	if task == nil {
		return nil
	}
	if err := s.videoTaskRepo().FailVideoBillingTask(ctx, task.ID); err != nil {
		return err
	}
	s.invalidateVideoBalance(ctx, task.UserID)
	return nil
}

func (s *OpenAIGatewayService) invalidateVideoBalance(ctx context.Context, userID int64) {
	if s.videoAuthCache != nil {
		s.videoAuthCache.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateUserBalance(ctx, userID)
	}
}

func (s *OpenAIGatewayService) FindVideoBillingTask(ctx context.Context, taskID string, userID, keyID int64) (*VideoBillingTask, error) {
	if s.videoTaskRepo() == nil {
		return nil, nil
	}
	return s.videoTaskRepo().GetVideoBillingTask(ctx, taskID, userID, keyID)
}

// 已有任务使用创建时账号取回结果，不受生成额度、调度健康和粘性缓存过期影响。
func (s *OpenAIGatewayService) VideoBillingTaskAccount(ctx context.Context, task *VideoBillingTask) (*AccountSelectionResult, error) {
	account, err := s.accountRepo.GetByID(ctx, task.AccountID)
	if err != nil {
		return nil, err
	}
	if account == nil || account.Platform != task.Snapshot.Account.Platform {
		return nil, ErrNoAvailableAccounts
	}
	slot, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
	if err != nil {
		return nil, err
	}
	if slot != nil && slot.Acquired {
		return &AccountSelectionResult{Account: account, Acquired: true, ReleaseFunc: slot.ReleaseFunc}, nil
	}
	cfg := s.schedulingConfig()
	return &AccountSelectionResult{Account: account, WaitPlan: &AccountWaitPlan{AccountID: account.ID, MaxConcurrency: account.Concurrency, Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting}}, nil
}
