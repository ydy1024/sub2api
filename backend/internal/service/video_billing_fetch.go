package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

func (s *OpenAIGatewayService) fetchVideoBillingResult(ctx context.Context, task *VideoBillingTask) (*OpenAIForwardResult, bool, error) {
	account, err := s.accountRepo.GetByID(ctx, task.AccountID)
	if err != nil {
		return nil, false, err
	}
	body, err := s.fetchVideoTaskStatus(ctx, task, account)
	if err != nil {
		return nil, false, err
	}
	status := strings.ToLower(gjson.GetBytes(body, "status").String())
	if status == "failed" || status == "expired" || status == "cancelled" || status == "canceled" {
		return nil, true, nil
	}
	var result *OpenAIForwardResult
	if task.Snapshot.Seedance {
		if status != "succeeded" {
			return nil, false, nil
		}
		result = &OpenAIForwardResult{Usage: OpenAIUsage{OutputTokens: int(gjson.GetBytes(body, "usage.completion_tokens").Int())}}
		if result.Usage.OutputTokens <= 0 {
			return nil, false, fmt.Errorf("completed Seedance task is missing completion_tokens")
		}
	} else {
		result = ExtractGrokVideoBillingFromStatusBody(body, &task.Snapshot.Pending, task.TaskID)
		if result == nil {
			return nil, false, nil
		}
		result.Usage, _ = extractOpenAIUsageFromJSONBytes(body)
		if task.Snapshot.Quote.Mode == BillingModeVideo && len(task.Snapshot.Quote.ResolutionPrices) > 0 {
			if _, ok := task.Snapshot.Quote.ResolutionPrices[result.VideoResolution]; !ok {
				return nil, false, fmt.Errorf("video result resolution has no snapshot price: %s", result.VideoResolution)
			}
		}
		if task.Snapshot.Quote.Mode == BillingModeToken && result.Usage.InputTokens+result.Usage.OutputTokens <= 0 {
			return nil, false, fmt.Errorf("completed token-priced video task has no usage")
		}
	}
	result.RequestID = StableGrokVideoBillingRequestID(task.TaskID)
	result.ResponseID = task.TaskID
	result.Model = task.Snapshot.Pending.Model
	result.BillingModel = task.Snapshot.Pending.BillingModel
	result.UpstreamModel = task.Snapshot.Pending.UpstreamModel
	result.Duration = time.Since(task.CreatedAt)
	return result, false, nil
}

func (s *OpenAIGatewayService) fetchVideoTaskStatus(ctx context.Context, task *VideoBillingTask, account *Account) ([]byte, error) {
	var target string
	var err error
	if task.Snapshot.Seedance {
		var base string
		base, err = s.validateUpstreamBaseURL(account.GetCredential("base_url"))
		if err == nil {
			target, err = buildSeedanceURL(base, SeedanceEndpointStatus, strings.TrimPrefix(task.TaskID, "seedance:"))
		}
	} else {
		target, err = buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideoStatus, task.TaskID)
	}
	if err != nil {
		return nil, err
	}
	token, _, err := s.getRequestCredential(ctx, nil, account)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(target) {
		applyGrokCLIHeaders(req.Header)
	}
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("video status HTTP %d", resp.StatusCode)
	}
	return ReadUpstreamResponseBody(resp.Body, s.cfg, nil, openAITooLargeError)
}
