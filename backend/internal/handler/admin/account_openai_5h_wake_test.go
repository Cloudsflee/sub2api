package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAI5hWakeHandlerAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r *openAI5hWakeHandlerAccountRepo) ListByPlatform(context.Context, string) ([]service.Account, error) {
	return append([]service.Account(nil), r.accounts...), nil
}

type openAI5hWakeHandlerTaskRepo struct {
	service.OpenAI5hWakeTaskRepository
	task         *service.OpenAI5hWakeTask
	items        []*service.OpenAI5hWakeTaskItem
	createParams service.OpenAI5hWakeCreateParams
}

func (r *openAI5hWakeHandlerTaskRepo) CreateOrGetActive(_ context.Context, params service.OpenAI5hWakeCreateParams) (*service.OpenAI5hWakeTask, bool, error) {
	r.createParams = params
	return r.task, true, nil
}

func (r *openAI5hWakeHandlerTaskRepo) GetLatestTask(context.Context) (*service.OpenAI5hWakeTask, error) {
	return r.task, nil
}

func (r *openAI5hWakeHandlerTaskRepo) GetTask(_ context.Context, id int64) (*service.OpenAI5hWakeTask, error) {
	if r.task != nil && r.task.ID == id {
		return r.task, nil
	}
	return nil, service.ErrOpenAI5hWakeTaskNotFound
}

func (r *openAI5hWakeHandlerTaskRepo) ListTaskItems(_ context.Context, taskID int64, _, _ int) ([]*service.OpenAI5hWakeTaskItem, int64, error) {
	if r.task == nil || taskID != r.task.ID {
		return nil, 0, service.ErrOpenAI5hWakeTaskNotFound
	}
	return r.items, int64(len(r.items)), nil
}

func (r *openAI5hWakeHandlerTaskRepo) RequestCancel(_ context.Context, taskID int64, now time.Time) (*service.OpenAI5hWakeTask, error) {
	if r.task == nil || taskID != r.task.ID {
		return nil, service.ErrOpenAI5hWakeTaskNotFound
	}
	copyTask := *r.task
	copyTask.CancelRequestedAt = &now
	return &copyTask, nil
}

func newOpenAI5hWakeHandler(t *testing.T) (*AccountHandler, *openAI5hWakeHandlerTaskRepo) {
	t.Helper()
	now := time.Now().UTC()
	taskRepo := &openAI5hWakeHandlerTaskRepo{
		task: &service.OpenAI5hWakeTask{
			ID: 31, Status: service.OpenAI5hWakeTaskStatusPending, EligibleAccountCount: 1,
			EstimatedRequestCount: 1, TotalItems: 1, CreatedAt: now, UpdatedAt: now,
		},
		items: []*service.OpenAI5hWakeTaskItem{{
			ID: 44, TaskID: 31, IdentityHash: "0123456789abcdef", MemberAccountIDs: []int64{7},
			AttemptedAccountIDs: []int64{}, Status: service.OpenAI5hWakeItemStatusPending,
			CreatedAt: now, UpdatedAt: now,
		}},
	}
	accountRepo := &openAI5hWakeHandlerAccountRepo{accounts: []service.Account{{
		ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"chatgpt_account_id": "pool-7"},
	}}}
	wake := service.NewOpenAI5hWakeService(taskRepo, accountRepo, nil, nil, nil, nil, nil, nil)
	handler := &AccountHandler{}
	handler.SetOpenAI5hWakeService(wake)
	return handler, taskRepo
}

func invokeOpenAI5hWakeHandler(t *testing.T, method, path string, params gin.Params, handler gin.HandlerFunc) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, path, nil)
	ctx.Params = params
	handler(ctx)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	return recorder.Code, payload
}

func requireOpenAI5hWakeMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	require.True(t, ok)
	return result
}

func TestOpenAI5hWakeHandlerContracts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, repo := newOpenAI5hWakeHandler(t)

	status, payload := invokeOpenAI5hWakeHandler(t, http.MethodGet, "/preview", nil, handler.PreviewOpenAI5hWake)
	require.Equal(t, http.StatusOK, status)
	preview := requireOpenAI5hWakeMap(t, payload["data"])
	require.Equal(t, float64(1), preview["eligible_accounts"])
	require.Equal(t, float64(1), preview["unique_quota_pools"])

	status, payload = invokeOpenAI5hWakeHandler(t, http.MethodPost, "/tasks", nil, handler.CreateOpenAI5hWakeTask)
	require.Equal(t, http.StatusAccepted, status)
	created := requireOpenAI5hWakeMap(t, payload["data"])
	require.Equal(t, false, created["reused"])
	require.Equal(t, float64(31), requireOpenAI5hWakeMap(t, created["task"])["id"])
	require.Len(t, repo.createParams.Items, 1)
	require.Len(t, repo.createParams.Items[0].IdentityHash, 64)

	status, payload = invokeOpenAI5hWakeHandler(t, http.MethodGet, "/tasks/latest", nil, handler.GetLatestOpenAI5hWakeTask)
	require.Equal(t, http.StatusOK, status)
	latest := requireOpenAI5hWakeMap(t, payload["data"])
	require.Equal(t, float64(31), requireOpenAI5hWakeMap(t, latest["task"])["id"])

	params := gin.Params{{Key: "id", Value: "31"}}
	status, payload = invokeOpenAI5hWakeHandler(t, http.MethodGet, "/tasks/31", params, handler.GetOpenAI5hWakeTask)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "pending", requireOpenAI5hWakeMap(t, payload["data"])["status"])

	status, payload = invokeOpenAI5hWakeHandler(t, http.MethodGet, "/tasks/31/items?page=1&page_size=10", params, handler.ListOpenAI5hWakeTaskItems)
	require.Equal(t, http.StatusOK, status)
	items := requireOpenAI5hWakeMap(t, payload["data"])
	require.Equal(t, float64(1), items["total"])
	itemList, ok := items["items"].([]any)
	require.True(t, ok)
	require.Len(t, itemList, 1)

	status, payload = invokeOpenAI5hWakeHandler(t, http.MethodPost, "/tasks/31/cancel", params, handler.CancelOpenAI5hWakeTask)
	require.Equal(t, http.StatusOK, status)
	require.NotEmpty(t, requireOpenAI5hWakeMap(t, payload["data"])["cancel_requested_at"])
}

func TestOpenAI5hWakeHandlerRejectsInvalidTaskID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, _ := newOpenAI5hWakeHandler(t)
	status, payload := invokeOpenAI5hWakeHandler(
		t, http.MethodGet, "/tasks/not-a-number", gin.Params{{Key: "id", Value: "not-a-number"}}, handler.GetOpenAI5hWakeTask,
	)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "Invalid wake task ID", payload["message"])
}
