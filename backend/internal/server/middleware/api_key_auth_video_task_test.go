//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestVideoTaskReadSkipsOnlyBilling(t *testing.T) {
	for _, path := range []string{"/v1/videos/task", "/v1/videos/task/content", "/videos/task", "/api/v3/contents/generations/tasks/task"} {
		require.True(t, isVideoTaskRead(http.MethodGet, path))
		require.False(t, isVideoTaskRead(http.MethodPost, path))
	}
	require.False(t, isVideoTaskRead(http.MethodGet, "/v1/videos"))
	require.False(t, isVideoTaskRead(http.MethodGet, "/v1/chat/completions"))
}

func TestVideoTaskAuthAllowsExhaustedBalanceButNotDisabledKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := &service.APIKey{ID: 5, Key: "video-test-key", Status: service.StatusAPIKeyQuotaExhausted,
		User: &service.User{ID: 2, Status: service.StatusActive, Balance: 0}}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { copy := *key; return &copy, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(apiKeyAuthWithSubscription(svc, nil, cfg))
	router.GET("/v1/videos/:id/content", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/v1/videos/task/content", nil)
	req.Header.Set("x-api-key", key.Key)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	key.Status = service.StatusAPIKeyDisabled
	svc.InvalidateAuthCacheByKey(context.Background(), key.Key)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}
