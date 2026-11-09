package service

import (
	"context"
	"fmt"
	"math"
	"time"
)

func (s *OpenAIGatewayService) quoteVideoTask(ctx context.Context, key *APIKey, user *User, info GrokMediaRequestInfo, seedance bool) (VideoBillingQuote, error) {
	rate := 1.0
	if s.cfg != nil {
		rate = s.cfg.Default.RateMultiplier
	}
	if key.Group != nil && key.GroupID != nil {
		rate = s.ResolveUserGroupRateMultiplier(ctx, user.ID, *key.GroupID, key.Group.RateMultiplier)
	}
	resolved := s.resolveOpenAIChannelPricing(ctx, info.Model, key)
	if seedance || (resolved != nil && resolved.Mode == BillingModeToken) {
		return s.quoteVideoTokens(ctx, key, info.Model, rate, resolved)
	}
	result := &OpenAIForwardResult{VideoCount: 1, VideoDurationSeconds: 1, VideoResolution: info.Resolution}
	cost, err := s.calculateOpenAIVideoCost(ctx, info.Model, key, result, resolveVideoRateMultiplier(key, rate))
	if err != nil {
		return VideoBillingQuote{}, err
	}
	if cost.TotalCost < 0 || cost.ActualCost < 0 || math.IsNaN(cost.TotalCost) || math.IsInf(cost.TotalCost, 0) || math.IsNaN(cost.ActualCost) || math.IsInf(cost.ActualCost, 0) {
		return VideoBillingQuote{}, fmt.Errorf("invalid video pricing")
	}
	mode := BillingModeVideo
	if resolved != nil && (resolved.Mode == BillingModePerRequest || resolved.Mode == BillingModeImage) && !apiKeyHasConfiguredVideoPrice(key, info.Model, info.Resolution) {
		mode = BillingModePerRequest
	}
	quote := VideoBillingQuote{Mode: mode, UnitTotal: cost.TotalCost, UnitActual: cost.ActualCost, Multiplier: resolveVideoRateMultiplier(key, rate),
		ResolutionPrices: map[string]float64{}, ResolutionActualPrices: map[string]float64{}}
	for _, resolution := range []string{VideoBillingResolution480P, VideoBillingResolution720P, VideoBillingResolution1080P} {
		result.VideoResolution = resolution
		if tier, err := s.calculateOpenAIVideoCost(ctx, info.Model, key, result, quote.Multiplier); err == nil {
			quote.ResolutionPrices[resolution], quote.ResolutionActualPrices[resolution] = tier.TotalCost, tier.ActualCost
		}
	}
	return quote, nil
}

func (s *OpenAIGatewayService) quoteVideoTokens(ctx context.Context, key *APIKey, model string, rate float64, resolved *ResolvedPricing) (VideoBillingQuote, error) {
	var price *ModelPricing
	if resolved != nil && s.resolver != nil {
		price = s.resolver.GetIntervalPricing(resolved, 0)
	} else {
		price, _ = s.billingService.GetModelPricing(model)
	}
	if price == nil {
		return VideoBillingQuote{}, fmt.Errorf("%w for video model: %s", ErrModelPricingUnavailable, model)
	}
	copy := *price
	rate, _ = computePeakAwareMultipliers(key, rate, time.Now())
	quote := VideoBillingQuote{Mode: BillingModeToken, TokenPricing: &copy, Multiplier: rate, TokenLongContext: key.Group == nil || key.Group.LongContextPricingEnabled}
	if resolved != nil {
		quote.Multiplier *= resolvedChannelTimeMultiplier(resolved, time.Now())
		if quote.TokenLongContext {
			quote.TokenIntervals = resolved.Intervals
		}
		if resolved.channelPricing != nil {
			ch := resolved.channelPricing.Clone()
			quote.TokenChannelPricing = &ch
		}
	}
	return quote, nil
}
