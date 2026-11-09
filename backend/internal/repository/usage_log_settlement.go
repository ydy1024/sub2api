package repository

import (
	"encoding/json"
	"strconv"
	"strings"
)

// 只有成功结算的调用方可以更新零金额占位行；重复请求不覆盖已扣费金额。
// 使用原行 created_at，使日报聚合的 UPDATE 触发器修正原消费日。
const settledUsageLogConflictSQL = `ON CONFLICT (request_id, api_key_id) DO UPDATE SET
	actual_cost = EXCLUDED.actual_cost,
	total_cost = EXCLUDED.total_cost,
	input_cost = EXCLUDED.input_cost,
	output_cost = EXCLUDED.output_cost,
	cache_creation_cost = EXCLUDED.cache_creation_cost,
	cache_read_cost = EXCLUDED.cache_read_cost,
	image_input_cost = EXCLUDED.image_input_cost,
	image_output_cost = EXCLUDED.image_output_cost,
	rate_multiplier = EXCLUDED.rate_multiplier,
	account_rate_multiplier = EXCLUDED.account_rate_multiplier,
	account_stats_cost = EXCLUDED.account_stats_cost,
	billing_mode = EXCLUDED.billing_mode,
	billing_type = EXCLUDED.billing_type,
	video_count = EXCLUDED.video_count,
	video_resolution = EXCLUDED.video_resolution,
	video_duration_seconds = EXCLUDED.video_duration_seconds
WHERE usage_logs.actual_cost = 0 AND EXCLUDED.actual_cost > 0`

func preparedByKeyValues(values map[string]usageLogInsertPrepared) []usageLogInsertPrepared {
	result := make([]usageLogInsertPrepared, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func settledRecentKey(key string, settled bool) string {
	if settled {
		return key + "\x1fsettled"
	}
	return key
}

// 只将本批中有成功扣费证据的请求纳入 UPSERT，保留通用日志批处理吞吐。
func applySettledUsageConflict(query string, args []any, rows []usageLogInsertPrepared) (string, []any) {
	var keys []struct {
		RequestID string `json:"request_id"`
		APIKeyID  any    `json:"api_key_id"`
	}
	for _, row := range rows {
		if !row.billingSettled || row.requestID == "" {
			continue
		}
		keys = append(keys, struct {
			RequestID string `json:"request_id"`
			APIKeyID  any    `json:"api_key_id"`
		}{row.requestID, row.args[1]})
	}
	if len(keys) == 0 {
		return query, args
	}
	payload, _ := json.Marshal(keys)
	args = append(args, string(payload))
	conflict := settledUsageLogConflictSQL + " AND EXISTS (SELECT 1 FROM jsonb_to_recordset($" + strconv.Itoa(len(args)) + "::jsonb) AS settled(request_id text,api_key_id bigint) WHERE settled.request_id=EXCLUDED.request_id AND settled.api_key_id=EXCLUDED.api_key_id)"
	return strings.Replace(query, "ON CONFLICT (request_id, api_key_id) DO NOTHING", conflict, 1), args
}
