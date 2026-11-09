package service

import (
	"context"
	"time"
)

// VideoBillingTask 不保存提示词、用户密钥或上游凭据。
type VideoBillingTask struct {
	ID                string
	TaskID            string
	UserID            int64
	APIKeyID          int64
	AccountID         int64
	GroupID           *int64
	SubscriptionID    *int64
	HoldAmount        float64
	State             string
	CreatedAt         time.Time
	Snapshot          VideoBillingSnapshot
	Result            *OpenAIForwardResult
	SubmissionStarted bool `json:"-"`
	UpstreamStatus    int  `json:"-"`
}

type VideoBillingSnapshot struct {
	APIKey          APIKey
	User            User
	Account         Account
	Subscription    *UserSubscription
	Pending         GrokVideoPendingBilling
	Quote           VideoBillingQuote
	Seedance        bool
	QuotaPlatform   string
	InboundEndpoint string
}

type VideoBillingTaskRepository interface {
	CreateVideoBillingTask(context.Context, *VideoBillingTask) error
	BindVideoBillingTask(context.Context, string, string) error
	SaveVideoBillingResult(context.Context, string, *OpenAIForwardResult) error
	GetVideoBillingTask(context.Context, string, int64, int64) (*VideoBillingTask, error)
	ListDueVideoBillingTasks(context.Context, int) ([]*VideoBillingTask, error)
	DeferVideoBillingTask(context.Context, string) error
	FinishVideoBillingTask(context.Context, string) error
	FailVideoBillingTask(context.Context, string) error
}

type VideoBillingQuote struct {
	Mode                   BillingMode
	UnitTotal              float64
	UnitActual             float64
	ResolutionPrices       map[string]float64
	ResolutionActualPrices map[string]float64
	TokenPricing           *ModelPricing
	TokenIntervals         []PricingInterval
	TokenChannelPricing    *ChannelModelPricing
	TokenLongContext       bool
	Multiplier             float64
}

func (q VideoBillingQuote) Cost(result *OpenAIForwardResult) *CostBreakdown {
	if q.Mode == BillingModeToken {
		contextTokens := result.Usage.InputTokens
		resolved := &ResolvedPricing{BasePricing: q.TokenPricing, Intervals: q.TokenIntervals, channelPricing: q.TokenChannelPricing}
		pricing := (&ModelPricingResolver{}).GetIntervalPricing(resolved, contextTokens)
		cost := (&BillingService{}).computeTokenBreakdown(pricing, UsageTokens{
			InputTokens:  max(0, result.Usage.InputTokens-result.Usage.CacheReadInputTokens-result.Usage.CacheCreationInputTokens),
			OutputTokens: result.Usage.OutputTokens, CacheReadTokens: result.Usage.CacheReadInputTokens, CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		}, q.Multiplier, "", q.TokenLongContext && len(q.TokenIntervals) == 0)
		cost.BillingMode = string(BillingModeToken)
		return cost
	}
	units := 1.0
	unitTotal, unitActual := q.UnitTotal, q.UnitActual
	if q.Mode == BillingModeVideo {
		units = float64(NormalizeVideoBillingDurationSecondsOrDefault(result.VideoDurationSeconds))
		if price, ok := q.ResolutionPrices[result.VideoResolution]; ok {
			unitTotal, unitActual = price, q.ResolutionActualPrices[result.VideoResolution]
		}
	}
	return &CostBreakdown{TotalCost: unitTotal * units, ActualCost: unitActual * units, BillingMode: string(q.Mode)}
}
