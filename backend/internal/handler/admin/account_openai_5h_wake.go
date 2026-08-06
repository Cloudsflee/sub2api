package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) requireOpenAI5hWake(c *gin.Context) *service.OpenAI5hWakeService {
	if h != nil && h.openAI5hWake != nil {
		return h.openAI5hWake
	}
	response.Error(c, http.StatusServiceUnavailable, "OpenAI 5h wake service is unavailable")
	return nil
}

// PreviewOpenAI5hWake returns a database-wide eligibility snapshot.
func (h *AccountHandler) PreviewOpenAI5hWake(c *gin.Context) {
	wake := h.requireOpenAI5hWake(c)
	if wake == nil {
		return
	}
	preview, err := wake.Preview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, preview)
}

// CreateOpenAI5hWakeTask creates a durable task or returns the active one.
func (h *AccountHandler) CreateOpenAI5hWakeTask(c *gin.Context) {
	wake := h.requireOpenAI5hWake(c)
	if wake == nil {
		return
	}
	middleware.SetAuditAction(c, service.AuditActionOpenAI5hWakeStart)
	var requestedBy *int64
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		userID := subject.UserID
		requestedBy = &userID
	}
	task, created, err := wake.CreateTask(c.Request.Context(), requestedBy, c.GetString(middleware.ContextKeyAuthEmail))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{
		"task_id":     task.ID,
		"total_items": task.TotalItems,
		"reused":      !created,
	})
	response.Accepted(c, gin.H{"task": task, "reused": !created})
}

func (h *AccountHandler) GetLatestOpenAI5hWakeTask(c *gin.Context) {
	wake := h.requireOpenAI5hWake(c)
	if wake == nil {
		return
	}
	task, err := wake.GetLatestTask(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"task": task})
}

func (h *AccountHandler) GetOpenAI5hWakeTask(c *gin.Context) {
	wake := h.requireOpenAI5hWake(c)
	if wake == nil {
		return
	}
	taskID, ok := parseOpenAI5hWakeTaskID(c)
	if !ok {
		return
	}
	task, err := wake.GetTask(c.Request.Context(), taskID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, task)
}

func (h *AccountHandler) ListOpenAI5hWakeTaskItems(c *gin.Context) {
	wake := h.requireOpenAI5hWake(c)
	if wake == nil {
		return
	}
	taskID, ok := parseOpenAI5hWakeTaskID(c)
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	if pageSize > 100 {
		pageSize = 100
	}
	items, total, err := wake.ListTaskItems(c.Request.Context(), taskID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *AccountHandler) ListOpenAI5hWakeTaskEvents(c *gin.Context) {
	wake := h.requireOpenAI5hWake(c)
	if wake == nil {
		return
	}
	taskID, ok := parseOpenAI5hWakeTaskID(c)
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	if pageSize > 100 {
		pageSize = 100
	}
	events, total, err := wake.ListTaskEvents(c.Request.Context(), taskID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, events, total, page, pageSize)
}

func (h *AccountHandler) CancelOpenAI5hWakeTask(c *gin.Context) {
	wake := h.requireOpenAI5hWake(c)
	if wake == nil {
		return
	}
	middleware.SetAuditAction(c, service.AuditActionOpenAI5hWakeCancel)
	taskID, ok := parseOpenAI5hWakeTaskID(c)
	if !ok {
		return
	}
	task, err := wake.CancelTask(c.Request.Context(), taskID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"task_id": task.ID})
	response.Success(c, task)
}

func parseOpenAI5hWakeTaskID(c *gin.Context) (int64, bool) {
	taskID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || taskID <= 0 {
		response.BadRequest(c, "Invalid wake task ID")
		return 0, false
	}
	return taskID, true
}
