package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type PublicAccountStatusHandler struct {
	service *service.PublicAccountStatusService
}

func NewPublicAccountStatusHandler(statusService *service.PublicAccountStatusService) *PublicAccountStatusHandler {
	return &PublicAccountStatusHandler{service: statusService}
}

func (h *PublicAccountStatusHandler) ListGroups(c *gin.Context) {
	groups, etag, err := h.service.ListGroups(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if writePublicAccountStatusCacheHeaders(c, etag) {
		return
	}
	response.Success(c, groups)
}

func (h *PublicAccountStatusHandler) ListAccounts(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.NotFound(c, "Group not found")
		return
	}
	page, err := parsePositivePublicStatusInt(c.DefaultQuery("page", "1"))
	if err != nil {
		response.BadRequest(c, "Invalid page")
		return
	}
	pageSize, err := parsePositivePublicStatusInt(c.DefaultQuery("page_size", "20"))
	if err != nil || (pageSize != 20 && pageSize != 50 && pageSize != 100) {
		response.BadRequest(c, "page_size must be one of 20, 50, or 100")
		return
	}
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/pageSize {
		response.BadRequest(c, "Invalid page")
		return
	}

	result, etag, err := h.service.ListAccounts(c.Request.Context(), groupID, page, pageSize)
	if errors.Is(err, service.ErrPublicAccountStatusGroupNotFound) {
		response.NotFound(c, "Group not found")
		return
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if writePublicAccountStatusCacheHeaders(c, etag) {
		return
	}
	response.Success(c, result)
}

func parsePositivePublicStatusInt(raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, errors.New("invalid positive integer")
	}
	return value, nil
}

func writePublicAccountStatusCacheHeaders(c *gin.Context, etag string) bool {
	c.Header("Cache-Control", "public, max-age=15, must-revalidate")
	if etag == "" {
		return false
	}
	c.Header("ETag", etag)
	c.Header("Vary", "If-None-Match")
	if publicAccountStatusETagMatches(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return true
	}
	return false
}

func publicAccountStatusETagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
