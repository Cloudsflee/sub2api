package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestListPublicAccountImportGroupsFiltersAllowlist(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(publicAccountImportGroupIDsEnv, "5,9,12")

	svc := newStubAdminService()
	svc.groups = []service.Group{
		{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 9, Name: "BUGTEAM", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 12, Name: "inactive", Platform: service.PlatformOpenAI, Status: service.StatusDisabled},
		{ID: 13, Name: "not-allowed", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 14, Name: "wrong-platform", Platform: service.PlatformAnthropic, Status: service.StatusActive},
	}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/public/account-import/groups", h.ListPublicAccountImportGroups)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/public/account-import/groups", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Code int                               `json:"code"`
		Data PublicAccountImportGroupsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Zero(t, payload.Code)
	require.Equal(t, []PublicAccountImportGroup{{ID: 5, Name: "K12"}, {ID: 9, Name: "BUGTEAM"}}, payload.Data.Groups)
}

func TestPublicImportCodexSessionsBindsMultipleAllowedGroups(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(publicAccountImportGroupIDsEnv, "5,9")

	svc := newStubAdminService()
	svc.accounts = nil
	svc.groups = []service.Group{
		{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 9, Name: "BUGTEAM", Platform: service.PlatformOpenAI, Status: service.StatusActive},
	}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	accountJSON, err := json.Marshal(buildCodexAccessOnlyImportValue(t, "workspace-public", "user-public"))
	require.NoError(t, err)
	body, err := json.Marshal(PublicAccountImportRequest{
		Contents: []string{string(accountJSON)},
		GroupIDs: []int64{9, 5, 9},
	})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/public/account-import", h.PublicImportCodexSessions)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/public/account-import", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "public-import-test-1")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var payload struct {
		Code int                       `json:"code"`
		Data PublicAccountImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Zero(t, payload.Code)
	require.Equal(t, 1, payload.Data.Created)
	require.Zero(t, payload.Data.Failed)
	require.Len(t, svc.createdAccounts, 1)
	require.Equal(t, []int64{5, 9}, svc.createdAccounts[0].GroupIDs)
	require.True(t, svc.createdAccounts[0].SkipDefaultGroupBind)
}

func TestPublicImportCodexSessionsRejectsGroupOutsideAllowlist(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(publicAccountImportGroupIDsEnv, "5")

	svc := newStubAdminService()
	svc.groups = []service.Group{{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive}}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	body, err := json.Marshal(PublicAccountImportRequest{Contents: []string{"{}"}, GroupIDs: []int64{9}})
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/public/account-import", h.PublicImportCodexSessions)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/public/account-import", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Empty(t, svc.createdAccounts)
}

func TestImportCodexSessionsSkipExistingDoesNotOverwrite(t *testing.T) {
	value := buildCodexAccessOnlyImportValue(t, "workspace-existing", "user-existing")
	item, err := normalizeCodexImportEntry(codexImportEntry{Index: 1, Value: value})
	require.NoError(t, err)

	svc := newCodexImportMemoryAdminService([]service.Account{{
		ID:          77,
		Name:        "existing",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: item.Credentials,
	}})
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	updateExisting := false
	skipExisting := true
	result, err := h.importCodexSessions(t.Context(), CodexSessionImportRequest{
		UpdateExisting: &updateExisting,
		SkipExisting:   &skipExisting,
	}, []codexImportEntry{{Index: 1, Value: value}})

	require.NoError(t, err)
	require.Equal(t, 1, result.Skipped)
	require.Zero(t, result.Created)
	require.Zero(t, result.Updated)
	require.Empty(t, svc.createdAccounts)
	require.Empty(t, svc.updatedAccounts)
}

func TestValidatePublicAccountImportContentsRequiresJSON(t *testing.T) {
	_, err := validatePublicAccountImportContents([]string{"not-json"})
	require.ErrorContains(t, err, "invalid")

	valid, err := validatePublicAccountImportContents([]string{`{"access_token":"test","expires_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`})
	require.NoError(t, err)
	require.Len(t, valid, 1)
}
