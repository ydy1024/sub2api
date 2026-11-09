//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSettledUsageBatchKeepsIdentifiersParameterized(t *testing.T) {
	requestID := "request'; SELECT secret --"
	settled := prepareUsageLogInsert(&service.UsageLog{APIKeyID: 2, RequestID: requestID, ActualCost: 1, BillingSettled: true})
	unsettled := prepareUsageLogInsert(&service.UsageLog{APIKeyID: 3, RequestID: "unsettled", ActualCost: 0})
	query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{settled, unsettled})
	require.Contains(t, query, "DO UPDATE SET")
	require.Contains(t, query, "jsonb_to_recordset($")
	require.NotContains(t, query, requestID)
	require.Contains(t, args[len(args)-1], requestID)
	require.NotContains(t, args[len(args)-1], "unsettled")
}
