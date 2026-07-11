package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	publicAccountImportEnabledEnv        = "PUBLIC_ACCOUNT_IMPORT_ENABLED"
	publicAccountImportGroupIDsEnv       = "PUBLIC_ACCOUNT_IMPORT_GROUP_IDS"
	publicAccountImportMaxRequestBytes   = 3 << 20
	publicAccountImportMaxFiles          = 20
	publicAccountImportMaxFileBytes      = 512 << 10
	publicAccountImportMaxContentBytes   = 2 << 20
	publicAccountImportMaxAccounts       = 100
	publicAccountImportMaxSelectedGroups = 20
)

type PublicAccountImportGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type PublicAccountImportGroupsResponse struct {
	Groups []PublicAccountImportGroup `json:"groups"`
}

type PublicAccountImportRequest struct {
	Contents []string `json:"contents"`
	GroupIDs []int64  `json:"group_ids"`
}

type PublicAccountImportItem struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
}

type PublicAccountImportResult struct {
	Total    int                         `json:"total"`
	Created  int                         `json:"created"`
	Skipped  int                         `json:"skipped"`
	Failed   int                         `json:"failed"`
	Items    []PublicAccountImportItem   `json:"items,omitempty"`
	Warnings []CodexSessionImportMessage `json:"warnings,omitempty"`
	Errors   []CodexSessionImportMessage `json:"errors,omitempty"`
}

func (h *AccountHandler) ListPublicAccountImportGroups(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}

	groups, err := h.listPublicAccountImportGroups(c.Request.Context())
	if err != nil {
		response.InternalError(c, "Failed to load import groups")
		return
	}

	response.Success(c, PublicAccountImportGroupsResponse{Groups: groups})
}

func (h *AccountHandler) PublicImportCodexSessions(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, publicAccountImportMaxRequestBytes)
	var req PublicAccountImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			response.Error(c, http.StatusRequestEntityTooLarge, "Import request is too large")
			return
		}
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	groupIDs, err := h.validatePublicAccountImportGroupIDs(c.Request.Context(), req.GroupIDs)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	contents, err := validatePublicAccountImportContents(req.Contents)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updateExisting := false
	skipExisting := true
	skipDefaultGroupBind := true
	confirmMixedChannelRisk := false
	importReq := CodexSessionImportRequest{
		Contents:                contents,
		GroupIDs:                groupIDs,
		UpdateExisting:          &updateExisting,
		SkipExisting:            &skipExisting,
		SkipDefaultGroupBind:    &skipDefaultGroupBind,
		ConfirmMixedChannelRisk: &confirmMixedChannelRisk,
	}
	entries, err := parseCodexSessionImportEntries(importReq)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if len(entries) == 0 {
		response.BadRequest(c, "No importable accounts were found")
		return
	}
	if len(entries) > publicAccountImportMaxAccounts {
		response.BadRequest(c, fmt.Sprintf("A maximum of %d accounts can be imported at once", publicAccountImportMaxAccounts))
		return
	}

	executeAdminIdempotentJSON(c, "public.accounts.import_codex_session", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		result, importErr := h.importCodexSessions(ctx, importReq, entries)
		if importErr != nil {
			return nil, importErr
		}
		return newPublicAccountImportResult(result), nil
	})
}

func publicAccountImportEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(publicAccountImportEnabledEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func publicAccountImportAllowedGroupIDs() (map[int64]struct{}, error) {
	raw := strings.TrimSpace(os.Getenv(publicAccountImportGroupIDsEnv))
	if raw == "" {
		return nil, errors.New("public account import group allowlist is empty")
	}

	allowed := make(map[int64]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid public account import group id: %s", part)
		}
		allowed[id] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, errors.New("public account import group allowlist is empty")
	}
	return allowed, nil
}

func (h *AccountHandler) listPublicAccountImportGroups(ctx context.Context) ([]PublicAccountImportGroup, error) {
	allowed, err := publicAccountImportAllowedGroupIDs()
	if err != nil {
		return nil, err
	}

	groups, _, err := h.adminService.ListGroups(ctx, 1, 1000, service.PlatformOpenAI, service.StatusActive, "", nil, "sort_order", "asc")
	if err != nil {
		return nil, err
	}

	result := make([]PublicAccountImportGroup, 0, len(groups))
	for _, group := range groups {
		if _, ok := allowed[group.ID]; !ok {
			continue
		}
		if group.Status != service.StatusActive || group.Platform != service.PlatformOpenAI {
			continue
		}
		result = append(result, PublicAccountImportGroup{ID: group.ID, Name: group.Name})
	}
	return result, nil
}

func (h *AccountHandler) validatePublicAccountImportGroupIDs(ctx context.Context, groupIDs []int64) ([]int64, error) {
	if len(groupIDs) == 0 {
		return nil, errors.New("at least one group must be selected")
	}
	if len(groupIDs) > publicAccountImportMaxSelectedGroups {
		return nil, fmt.Errorf("a maximum of %d groups can be selected", publicAccountImportMaxSelectedGroups)
	}
	for _, id := range groupIDs {
		if id <= 0 {
			return nil, errors.New("group_ids contains an invalid group id")
		}
	}

	normalized := normalizeInt64IDList(groupIDs)
	groups, err := h.listPublicAccountImportGroups(ctx)
	if err != nil {
		return nil, errors.New("failed to validate selected groups")
	}
	available := make(map[int64]struct{}, len(groups))
	for _, group := range groups {
		available[group.ID] = struct{}{}
	}
	for _, id := range normalized {
		if _, ok := available[id]; !ok {
			return nil, fmt.Errorf("group %d is not available for public import", id)
		}
	}
	return normalized, nil
}

func validatePublicAccountImportContents(contents []string) ([]string, error) {
	if len(contents) == 0 {
		return nil, errors.New("at least one JSON file is required")
	}
	if len(contents) > publicAccountImportMaxFiles {
		return nil, fmt.Errorf("a maximum of %d JSON files can be uploaded at once", publicAccountImportMaxFiles)
	}

	normalized := make([]string, 0, len(contents))
	totalBytes := 0
	for index, content := range contents {
		trimmed := strings.TrimSpace(content)
		if trimmed == "" {
			return nil, fmt.Errorf("JSON file %d is empty", index+1)
		}
		if len(trimmed) > publicAccountImportMaxFileBytes {
			return nil, fmt.Errorf("JSON file %d is too large", index+1)
		}
		if !json.Valid([]byte(trimmed)) {
			return nil, fmt.Errorf("JSON file %d is invalid", index+1)
		}
		totalBytes += len(trimmed)
		if totalBytes > publicAccountImportMaxContentBytes {
			return nil, errors.New("combined JSON content is too large")
		}
		normalized = append(normalized, trimmed)
	}
	return normalized, nil
}

func newPublicAccountImportResult(result CodexSessionImportResult) PublicAccountImportResult {
	items := make([]PublicAccountImportItem, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, PublicAccountImportItem{
			Index:   item.Index,
			Name:    item.Name,
			Action:  item.Action,
			Message: item.Message,
		})
	}
	return PublicAccountImportResult{
		Total:    result.Total,
		Created:  result.Created,
		Skipped:  result.Skipped,
		Failed:   result.Failed,
		Items:    items,
		Warnings: result.Warnings,
		Errors:   result.Errors,
	}
}
