//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type publicStatusHandlerRepoStub struct {
	groups []service.PublicStatusGroupRecord
}

func (r *publicStatusHandlerRepoStub) ListPublicStatusGroups(context.Context) ([]service.PublicStatusGroupRecord, error) {
	return r.groups, nil
}

func (r *publicStatusHandlerRepoStub) ListPublicStatusGroupAccounts(context.Context, []int64) ([]service.PublicStatusGroupAccountRecord, error) {
	return []service.PublicStatusGroupAccountRecord{}, nil
}

func (r *publicStatusHandlerRepoStub) ListPublicStatusAccounts(_ context.Context, groupID int64, _, _ int) ([]*service.Account, int64, error) {
	if groupID != 1 {
		return nil, 0, service.ErrPublicAccountStatusGroupNotFound
	}
	return []*service.Account{}, 0, nil
}

func newPublicStatusHandlerTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	repo := &publicStatusHandlerRepoStub{groups: []service.PublicStatusGroupRecord{{
		ID:       1,
		Name:     "Public",
		Platform: service.PlatformOpenAI,
		Status:   service.StatusActive,
	}}}
	h := NewPublicAccountStatusHandler(service.NewPublicAccountStatusService(repo, nil, nil))
	router := gin.New()
	router.GET("/groups", h.ListGroups)
	router.GET("/groups/:group_id/accounts", h.ListAccounts)
	return router
}

func TestPublicAccountStatusHandlerETag(t *testing.T) {
	router := newPublicStatusHandlerTestRouter()

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/groups", nil))
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)
	require.Equal(t, "public, max-age=15, must-revalidate", first.Header().Get("Cache-Control"))

	secondRequest := httptest.NewRequest(http.MethodGet, "/groups", nil)
	secondRequest.Header.Set("If-None-Match", "W/"+etag)
	second := httptest.NewRecorder()
	router.ServeHTTP(second, secondRequest)
	require.Equal(t, http.StatusNotModified, second.Code)
	require.Empty(t, second.Body.String())
}

func TestPublicAccountStatusHandlerPaginationAndNotFound(t *testing.T) {
	router := newPublicStatusHandlerTestRouter()

	tests := []struct {
		path string
		code int
	}{
		{path: "/groups/1/accounts?page=1&page_size=20", code: http.StatusOK},
		{path: "/groups/1/accounts?page=0&page_size=20", code: http.StatusBadRequest},
		{path: "/groups/1/accounts?page=1&page_size=25", code: http.StatusBadRequest},
		{path: "/groups/1/accounts?page=9223372036854775807&page_size=100", code: http.StatusBadRequest},
		{path: "/groups/not-a-number/accounts", code: http.StatusNotFound},
		{path: "/groups/2/accounts", code: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			require.Equal(t, test.code, recorder.Code)
		})
	}
}
