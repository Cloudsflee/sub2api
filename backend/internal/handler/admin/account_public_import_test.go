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

const retiredPublicAccountImportGroupIDsEnv = "PUBLIC_ACCOUNT_IMPORT_GROUP_IDS"

func TestListPublicAccountImportGroupsUsesPublicStatusFlagAndIgnoresLegacyAllowlist(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(retiredPublicAccountImportGroupIDsEnv, "5")

	svc := newStubAdminService()
	svc.groups = []service.Group{
		{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 9, Name: "BUGTEAM", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 12, Name: "inactive", Platform: service.PlatformOpenAI, Status: service.StatusDisabled, PublicStatusEnabled: true},
		{ID: 13, Name: "private", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 14, Name: "wrong-platform", Platform: service.PlatformAnthropic, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 15, Name: "ALL", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
	}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

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

func TestListPublicAccountImportGroupsAutoSyncsActiveOpenAIGroups(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(retiredPublicAccountImportGroupIDsEnv, "not-an-id")

	svc := newStubAdminService()
	svc.groups = []service.Group{
		{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 6, Name: "ALL", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 9, Name: "BUGTEAM", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 12, Name: "inactive", Platform: service.PlatformOpenAI, Status: service.StatusDisabled, PublicStatusEnabled: true},
		{ID: 13, Name: "private", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 14, Name: "wrong-platform", Platform: service.PlatformAnthropic, Status: service.StatusActive, PublicStatusEnabled: true},
	}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	groups, err := h.listPublicAccountImportGroups(t.Context())
	require.NoError(t, err)
	require.Equal(t, []PublicAccountImportGroup{{ID: 5, Name: "K12"}, {ID: 9, Name: "BUGTEAM"}}, groups)
}

func TestPublicImportCodexSessionsBindsMultipleAllowedGroups(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(retiredPublicAccountImportGroupIDsEnv, "5,9")

	svc := newStubAdminService()
	svc.accounts = nil
	svc.groups = []service.Group{
		{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 6, Name: "ALL", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 9, Name: "BUGTEAM", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
	}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

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
	require.Equal(t, []int64{5, 9, 6}, svc.createdAccounts[0].GroupIDs)
	require.Equal(t, publicAccountImportDefaultConcurrency, svc.createdAccounts[0].Concurrency)
	require.Equal(t, publicAccountImportDefaultPriority, svc.createdAccounts[0].Priority)
	require.True(t, svc.createdAccounts[0].SkipDefaultGroupBind)
}

func TestPublicImportCodexSessionsMergesGroupsWithoutOverwritingExistingAccount(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(retiredPublicAccountImportGroupIDsEnv, "5,9")

	value := buildCodexAccessOnlyImportValue(t, "workspace-existing-public", "user-existing-public")
	item, err := normalizeCodexImportEntry(codexImportEntry{Index: 1, Value: value})
	require.NoError(t, err)
	existingCredentials := cloneCodexImportTestMap(item.Credentials)
	existingCredentials["refresh_token"] = "stored-refresh-token"
	existingExtra := map[string]any{"stored_setting": "keep-me"}
	proxyID := int64(88)
	svc := newCodexImportMemoryAdminService([]service.Account{{
		ID:          77,
		Name:        "existing-public-account",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: existingCredentials,
		Extra:       existingExtra,
		ProxyID:     &proxyID,
		Concurrency: 11,
		Priority:    23,
		GroupIDs:    []int64{9, 6},
	}})
	svc.groups = []service.Group{
		{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 6, Name: "ALL", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 9, Name: "BUGTEAM", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
	}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	accountJSON, err := json.Marshal(value)
	require.NoError(t, err)
	body, err := json.Marshal(PublicAccountImportRequest{
		Contents: []string{string(accountJSON)},
		GroupIDs: []int64{5},
	})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/public/account-import", h.PublicImportCodexSessions)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/public/account-import", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "public-import-merge-existing")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var payload struct {
		Code int                       `json:"code"`
		Data PublicAccountImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Zero(t, payload.Code)
	require.Equal(t, 1, payload.Data.Updated)
	require.Zero(t, payload.Data.Created)
	require.Zero(t, payload.Data.Skipped)
	require.Zero(t, payload.Data.Failed)
	require.Len(t, payload.Data.Items, 1)
	require.Equal(t, "updated", payload.Data.Items[0].Action)
	require.Contains(t, payload.Data.Items[0].Message, "追加绑定")

	require.Empty(t, svc.createdAccounts)
	require.Len(t, svc.updatedAccounts, 1)
	update := svc.updatedAccounts[0].input
	require.NotNil(t, update.GroupIDs)
	require.Equal(t, []int64{9, 6, 5}, *update.GroupIDs)
	require.Nil(t, update.Credentials)
	require.Nil(t, update.Extra)
	require.Nil(t, update.ProxyID)
	require.Nil(t, update.Concurrency)
	require.Nil(t, update.Priority)
	require.Equal(t, "stored-refresh-token", svc.accounts[0].Credentials["refresh_token"])
	require.Equal(t, "keep-me", svc.accounts[0].Extra["stored_setting"])
	require.Equal(t, proxyID, *svc.accounts[0].ProxyID)
	require.Equal(t, 11, svc.accounts[0].Concurrency)
	require.Equal(t, 23, svc.accounts[0].Priority)
}

func TestPublicImportCodexSessionsSkipsExistingAccountWithAllGroupsBound(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(retiredPublicAccountImportGroupIDsEnv, "5")

	value := buildCodexAccessOnlyImportValue(t, "workspace-bound-public", "user-bound-public")
	item, err := normalizeCodexImportEntry(codexImportEntry{Index: 1, Value: value})
	require.NoError(t, err)
	svc := newCodexImportMemoryAdminService([]service.Account{{
		ID:          78,
		Name:        "already-bound-public-account",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: item.Credentials,
		GroupIDs:    []int64{5, 6},
	}})
	svc.groups = []service.Group{
		{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 6, Name: "ALL", Platform: service.PlatformOpenAI, Status: service.StatusActive},
	}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	accountJSON, err := json.Marshal(value)
	require.NoError(t, err)
	body, err := json.Marshal(PublicAccountImportRequest{
		Contents: []string{string(accountJSON)},
		GroupIDs: []int64{5},
	})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/public/account-import", h.PublicImportCodexSessions)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/public/account-import", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "public-import-already-bound")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var payload struct {
		Code int                       `json:"code"`
		Data PublicAccountImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Zero(t, payload.Code)
	require.Equal(t, 1, payload.Data.Skipped)
	require.Zero(t, payload.Data.Created)
	require.Zero(t, payload.Data.Updated)
	require.Zero(t, payload.Data.Failed)
	require.Len(t, payload.Data.Items, 1)
	require.Equal(t, "skipped", payload.Data.Items[0].Action)
	require.Contains(t, payload.Data.Items[0].Message, "已绑定所选分组")
	require.Empty(t, svc.createdAccounts)
	require.Empty(t, svc.updatedAccounts)
}

func TestImportCodexSessionsMergeExistingGroupsRequiresMatchingCredentials(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	storedValue := map[string]any{
		"access_token":  buildCodexAccessTokenWithJTI(t, "workspace-protected", "user-protected", "stored", expiresAt),
		"refresh_token": "stored-refresh-token",
	}
	incomingValue := map[string]any{
		"access_token":  buildCodexAccessTokenWithJTI(t, "workspace-protected", "user-protected", "incoming", expiresAt),
		"refresh_token": "different-refresh-token",
	}
	storedItem, err := normalizeCodexImportEntry(codexImportEntry{Index: 1, Value: storedValue})
	require.NoError(t, err)

	svc := newCodexImportMemoryAdminService([]service.Account{{
		ID:          79,
		Name:        "protected-public-account",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: storedItem.Credentials,
		GroupIDs:    []int64{6},
	}})
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	updateExisting := false
	skipExisting := true
	result, err := h.importCodexSessions(t.Context(), CodexSessionImportRequest{
		GroupIDs:                []int64{5, 6},
		UpdateExisting:          &updateExisting,
		SkipExisting:            &skipExisting,
		mergeExistingGroupsOnly: true,
	}, []codexImportEntry{{Index: 1, Value: incomingValue}})

	require.NoError(t, err)
	require.Equal(t, 1, result.Skipped)
	require.Zero(t, result.Created)
	require.Zero(t, result.Updated)
	require.Zero(t, result.Failed)
	require.Len(t, result.Items, 1)
	require.Contains(t, result.Items[0].Message, "凭据与已有记录不一致")
	require.Empty(t, svc.createdAccounts)
	require.Empty(t, svc.updatedAccounts)
}

func TestResolvePublicAccountImportGroupsAddsAllAndSetsPriority(t *testing.T) {
	t.Setenv(retiredPublicAccountImportGroupIDsEnv, "ignored")

	svc := newStubAdminService()
	svc.groups = []service.Group{
		{ID: 2, Name: "PLUS", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 4, Name: "FREE", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 6, Name: "ALL", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 9, Name: "BUGTEAM", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 10, Name: "k12-ourselves", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 11, Name: "OTHER", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
	}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	tests := []struct {
		name         string
		selected     []int64
		wantGroupIDs []int64
		wantPriority int
	}{
		{name: "OTHER is priority 1", selected: []int64{11}, wantGroupIDs: []int64{11, 6}, wantPriority: 1},
		{name: "FREE is priority 3", selected: []int64{4}, wantGroupIDs: []int64{4, 6}, wantPriority: 3},
		{name: "PLUS is priority 4", selected: []int64{2}, wantGroupIDs: []int64{2, 6}, wantPriority: 4},
		{name: "K12 is priority 2", selected: []int64{5}, wantGroupIDs: []int64{5, 6}, wantPriority: 2},
		{name: "other groups are priority 2", selected: []int64{9}, wantGroupIDs: []int64{9, 6}, wantPriority: 2},
		{name: "similar names remain priority 2", selected: []int64{10}, wantGroupIDs: []int64{10, 6}, wantPriority: 2},
		{name: "multiple groups use the smallest priority", selected: []int64{9, 4, 11}, wantGroupIDs: []int64{4, 9, 11, 6}, wantPriority: 1},
		{name: "FREE wins over PLUS when both are selected", selected: []int64{4, 2}, wantGroupIDs: []int64{2, 4, 6}, wantPriority: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groupIDs, priority, err := h.resolvePublicAccountImportGroups(t.Context(), tt.selected)
			require.NoError(t, err)
			require.Equal(t, tt.wantGroupIDs, groupIDs)
			require.Equal(t, tt.wantPriority, priority)
		})
	}
}

func TestPublicImportCodexSessionsRejectsGroupWithoutPublicStatus(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(retiredPublicAccountImportGroupIDsEnv, "9")

	svc := newStubAdminService()
	svc.groups = []service.Group{
		{ID: 6, Name: "ALL", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 9, Name: "private", Platform: service.PlatformOpenAI, Status: service.StatusActive},
	}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

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
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
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

func TestValidatePublicAccountImportContentsMatchesAdminParser(t *testing.T) {
	content := "\uFEFF  " + `{"access_token":"first"}` + "\n" + `{"access_token":"second","expires_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`
	valid, err := validatePublicAccountImportContents([]string{content})
	require.NoError(t, err)
	require.Len(t, valid, 1)
	require.NotContains(t, valid[0], "\uFEFF")

	entries, err := parseCodexSessionImportEntries(CodexSessionImportRequest{Contents: valid})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	rawToken, err := validatePublicAccountImportContents([]string{"raw-access-token"})
	require.NoError(t, err)
	require.Equal(t, []string{"raw-access-token"}, rawToken)

	_, err = validatePublicAccountImportContents([]string{"\uFEFF  "})
	require.ErrorContains(t, err, "empty")

	manyFiles := make([]string, 50)
	for index := range manyFiles {
		manyFiles[index] = `{"access_token":"test"}`
	}
	validated, err := validatePublicAccountImportContents(manyFiles)
	require.NoError(t, err)
	require.Len(t, validated, len(manyFiles))
}
