package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogVideoMetadataForUserAndAdmin(t *testing.T) {
	for _, mode := range []string{"video", "per_request", "token"} {
		t.Run(mode, func(t *testing.T) {
			resolution, seconds, elapsed := "720p", 10, 519000
			log := &service.UsageLog{
				Model: "test-video", BillingMode: &mode,
				VideoCount: 2, VideoResolution: &resolution, VideoDurationSeconds: &seconds,
				DurationMs: &elapsed,
			}
			for _, value := range []any{UsageLogFromService(log), UsageLogFromServiceAdmin(log)} {
				payload := videoUsageJSON(t, value)
				require.Equal(t, float64(2), payload["video_count"])
				require.Equal(t, "720p", payload["video_resolution"])
				require.Equal(t, float64(10), payload["video_duration_seconds"])
				require.Equal(t, float64(519000), payload["duration_ms"])
				require.Equal(t, mode, payload["billing_mode"])
			}
		})
	}
}

func TestUsageLogVideoMetadataDoesNotInventHistoricalValues(t *testing.T) {
	for _, value := range []any{
		UsageLogFromService(&service.UsageLog{}),
		UsageLogFromServiceAdmin(&service.UsageLog{}),
	} {
		payload := videoUsageJSON(t, value)
		require.Equal(t, float64(0), payload["video_count"])
		require.Contains(t, payload, "video_resolution")
		require.Nil(t, payload["video_resolution"])
		require.Contains(t, payload, "video_duration_seconds")
		require.Nil(t, payload["video_duration_seconds"])
	}
}

func videoUsageJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	return payload
}
