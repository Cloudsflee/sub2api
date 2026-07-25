package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type publicAccountStatusRouteRepo struct{}

func (publicAccountStatusRouteRepo) ListPublicStatusGroups(context.Context) ([]service.PublicStatusGroupRecord, error) {
	return []service.PublicStatusGroupRecord{{
		ID:       1,
		Name:     "Public",
		Platform: service.PlatformOpenAI,
		Status:   service.StatusActive,
	}}, nil
}

func (publicAccountStatusRouteRepo) ListPublicStatusGroupAccounts(context.Context, []int64) ([]service.PublicStatusGroupAccountRecord, error) {
	return []service.PublicStatusGroupAccountRecord{}, nil
}

func (publicAccountStatusRouteRepo) ListPublicStatusAccounts(_ context.Context, groupID int64, _, _ int) ([]*service.Account, int64, error) {
	if groupID != 1 {
		return nil, 0, service.ErrPublicAccountStatusGroupNotFound
	}
	return []*service.Account{}, 0, nil
}

func newPublicAccountStatusRoutesTestRouter(redisClient *redis.Client) *gin.Engine {
	gin.SetMode(gin.TestMode)
	statusService := service.NewPublicAccountStatusService(publicAccountStatusRouteRepo{}, nil, nil)
	statusHandler := handler.NewPublicAccountStatusHandler(statusService)
	router := gin.New()
	RegisterPublicAccountStatusRoutes(
		router.Group("/api/v1"),
		&handler.Handlers{PublicAccountStatus: statusHandler},
		redisClient,
	)
	return router
}

func TestPublicAccountStatusRoutesAreAnonymousAndReadOnly(t *testing.T) {
	router := newPublicAccountStatusRoutesTestRouter(nil)

	for _, path := range []string{
		"/api/v1/public/account-status/groups",
		"/api/v1/public/account-status/groups/1/accounts",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, recorder.Code, "path=%s", path)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, "/api/v1/public/account-status/groups", nil))
		require.Equal(t, http.StatusNotFound, recorder.Code, "method=%s", method)
	}
}

func TestPublicAccountStatusRoutesSharePerIPRateLimit(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	router := newPublicAccountStatusRoutesTestRouter(redisClient)

	for requestNumber := 1; requestNumber <= 121; requestNumber++ {
		path := "/api/v1/public/account-status/groups"
		if requestNumber%2 == 0 {
			path = "/api/v1/public/account-status/groups/1/accounts"
		}
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.RemoteAddr = "198.51.100.40:12345"
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if requestNumber <= 120 {
			require.Equal(t, http.StatusOK, recorder.Code, "request=%d", requestNumber)
			continue
		}
		require.Equal(t, http.StatusTooManyRequests, recorder.Code)
		require.Contains(t, recorder.Body.String(), "rate limit exceeded")
	}
}

func TestPublicAccountStatusRoutesRateLimitFailsOpen(t *testing.T) {
	redisClient := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  20 * time.Millisecond,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
		MaxRetries:   -1,
	})
	t.Cleanup(func() { _ = redisClient.Close() })
	router := newPublicAccountStatusRoutesTestRouter(redisClient)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/public/account-status/groups", nil)
	request.RemoteAddr = "203.0.113.10:12345"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
}
