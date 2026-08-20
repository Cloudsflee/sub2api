package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPublicUpstreamImportCreatesCanonicalOpenAIAccountWithoutLeakingCredentials(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	service.SetDefaultIdempotencyCoordinator(nil)
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	svc := newCodexImportMemoryAdminService(nil)
	svc.groups = publicUpstreamImportTestGroups()
	h := newPublicUpstreamImportTestHandler(svc, &config.Config{})

	secret := "sk-upstream-secret"
	recorder := performPublicUpstreamImportRequest(t, h, PublicAccountImportUpstreamRequest{
		BaseURL:  "https://API.Example.com:443/v1/?tenant=alpha",
		APIKey:   secret,
		GroupIDs: []int64{5},
	}, "create-one", 42)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), secret)
	require.NotContains(t, recorder.Body.String(), "api_key")
	require.NotContains(t, recorder.Body.String(), "base_url")
	require.Len(t, svc.createdAccounts, 1)
	created := svc.createdAccounts[0]
	require.Equal(t, "api.example.com", created.Name)
	require.Equal(t, service.PlatformOpenAI, created.Platform)
	require.Equal(t, service.AccountTypeAPIKey, created.Type)
	require.Equal(t, "https://api.example.com/v1?tenant=alpha", created.Credentials["base_url"])
	require.Equal(t, secret, created.Credentials["api_key"])
	require.Equal(t, []int64{5, 6}, created.GroupIDs)
	require.Equal(t, publicAccountImportDefaultConcurrency, created.Concurrency)
	require.Equal(t, publicAccountImportDefaultPriority, created.Priority)
	require.True(t, created.SkipDefaultGroupBind)
	require.NotNil(t, created.ProbeEnabled)
	require.True(t, *created.ProbeEnabled)

	var envelope struct {
		Data PublicAccountImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, 1, envelope.Data.Created)
	require.Len(t, envelope.Data.Items, 1)
}

func TestPublicUpstreamImportUpdatesOnlyMissingGroupsThenSkips(t *testing.T) {
	svc := newCodexImportMemoryAdminService([]service.Account{{
		ID:          77,
		Name:        "preserved-name",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Credentials: map[string]any{"base_url": "HTTPS://API.EXAMPLE.COM:443/v1/", "api_key": "same-key"},
		Extra:       map[string]any{"preserve": true},
		Concurrency: 19,
		Priority:    27,
		GroupIDs:    []int64{9, 6},
		AccountGroups: []service.AccountGroup{
			{AccountID: 77, GroupID: 10},
		},
	}})
	svc.groups = publicUpstreamImportTestGroups()
	h := newPublicUpstreamImportTestHandler(svc, &config.Config{})

	input, err := h.normalizePublicUpstreamImport(t.Context(), PublicAccountImportUpstreamRequest{
		Name:     "ignored-new-name",
		BaseURL:  "https://api.example.com/v1",
		APIKey:   "same-key",
		GroupIDs: []int64{5},
	})
	require.NoError(t, err)

	updated, err := h.importPublicUpstreamAccount(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, 1, updated.Updated)
	require.Zero(t, updated.Created)
	require.Len(t, svc.updatedAccounts, 1)
	update := svc.updatedAccounts[0].input
	require.NotNil(t, update.GroupIDs)
	require.Equal(t, []int64{9, 6, 10, 5}, *update.GroupIDs)
	require.Empty(t, update.Name)
	require.Nil(t, update.Credentials)
	require.Nil(t, update.Extra)
	require.Nil(t, update.Concurrency)
	require.Nil(t, update.Priority)
	require.Equal(t, "preserved-name", svc.accounts[0].Name)
	require.Equal(t, 19, svc.accounts[0].Concurrency)
	require.Equal(t, 27, svc.accounts[0].Priority)

	skipped, err := h.importPublicUpstreamAccount(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, 1, skipped.Skipped)
	require.Len(t, svc.updatedAccounts, 1)
}

func TestPublicUpstreamImportRequiresFeatureAndIdempotencyKey(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	svc.groups = publicUpstreamImportTestGroups()
	h := newPublicUpstreamImportTestHandler(svc, &config.Config{})
	payload := PublicAccountImportUpstreamRequest{
		BaseURL: "https://api.example.com", APIKey: "key", GroupIDs: []int64{5},
	}

	t.Setenv(publicAccountImportEnabledEnv, "false")
	disabled := performPublicUpstreamImportRequest(t, h, payload, "key-1", 1)
	require.Equal(t, http.StatusNotFound, disabled.Code)

	t.Setenv(publicAccountImportEnabledEnv, "true")
	missingKey := performPublicUpstreamImportRequest(t, h, payload, "", 1)
	require.Equal(t, http.StatusBadRequest, missingKey.Code)

	invalidKey := performPublicUpstreamImportRequest(t, h, payload, strings.Repeat("x", 129), 1)
	require.Equal(t, http.StatusBadRequest, invalidKey.Code)
	require.Empty(t, svc.createdAccounts)
}

func TestPublicUpstreamImportIdempotencyReplayDoesNotCreateAgain(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	repo := newMemoryIdempotencyRepoStub()
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	svc := newCodexImportMemoryAdminService(nil)
	svc.groups = publicUpstreamImportTestGroups()
	h := newPublicUpstreamImportTestHandler(svc, &config.Config{})
	payload := PublicAccountImportUpstreamRequest{
		BaseURL: "https://api.example.com/v1", APIKey: "replay-key", GroupIDs: []int64{5},
	}

	first := performPublicUpstreamImportRequest(t, h, payload, "same-operation", 42)
	second := performPublicUpstreamImportRequest(t, h, payload, "same-operation", 42)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, first.Body.String(), second.Body.String())
	require.Len(t, svc.createdAccounts, 1)

	repo.mu.Lock()
	records := make([]*service.IdempotencyRecord, 0, len(repo.data))
	for _, record := range repo.data {
		records = append(records, repo.clone(record))
	}
	repo.mu.Unlock()
	require.Len(t, records, 1)
	stored, err := json.Marshal(records[0])
	require.NoError(t, err)
	require.NotContains(t, string(stored), "replay-key")
	require.NotContains(t, string(stored), "https://api.example.com/v1")
}

func TestNormalizePublicUpstreamImportURLPolicy(t *testing.T) {
	allowlisted := &config.Config{}
	allowlisted.Security.URLAllowlist.Enabled = true
	allowlisted.Security.URLAllowlist.UpstreamHosts = []string{"api.example.com", "127.0.0.1"}

	normalized, _, err := normalizePublicAccountImportUpstreamURLWithConfig(
		"https://API.EXAMPLE.COM:443/prefix/?tenant=one", allowlisted,
	)
	require.NoError(t, err)
	require.Equal(t, "https://api.example.com/prefix?tenant=one", normalized)
	normalized, _, err = normalizePublicAccountImportUpstreamURLWithConfig(
		"https://API.EXAMPLE.COM:443/%2Ftenant/?tenant=one", allowlisted,
	)
	require.NoError(t, err)
	require.Equal(t, "https://api.example.com/%2Ftenant?tenant=one", normalized)

	_, _, err = normalizePublicAccountImportUpstreamURLWithConfig("https://other.example.com", allowlisted)
	require.ErrorContains(t, err, "host is not allowed")
	_, _, err = normalizePublicAccountImportUpstreamURLWithConfig("https://127.0.0.1/v1", allowlisted)
	require.ErrorContains(t, err, "host is not allowed")

	allowlisted.Security.URLAllowlist.AllowPrivateHosts = true
	_, _, err = normalizePublicAccountImportUpstreamURLWithConfig("https://127.0.0.1/v1", allowlisted)
	require.NoError(t, err)

	insecure := &config.Config{}
	normalized, _, err = normalizePublicAccountImportUpstreamURLWithConfig("https://127.0.0.1/v1", insecure)
	require.NoError(t, err, "allowlist-disabled imports must follow the gateway's format-only private-host policy")
	require.Equal(t, "https://127.0.0.1/v1", normalized)
	_, _, err = normalizePublicAccountImportUpstreamURLWithConfig("http://example.com/v1", insecure)
	require.Error(t, err)

	insecure.Security.URLAllowlist.AllowInsecureHTTP = true
	normalized, _, err = normalizePublicAccountImportUpstreamURLWithConfig("http://example.com/v1", insecure)
	require.NoError(t, err)
	require.Equal(t, "http://example.com/v1", normalized)

	for _, invalid := range []string{
		"https://user:password@example.com/v1",
		"https://example.com/v1#fragment",
		"ftp://example.com/v1",
	} {
		_, _, err = normalizePublicAccountImportUpstreamURLWithConfig(invalid, insecure)
		require.Error(t, err, invalid)
	}
}

func TestPublicUpstreamImportFieldLimits(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	svc.groups = publicUpstreamImportTestGroups()
	h := newPublicUpstreamImportTestHandler(svc, &config.Config{})

	tests := []struct {
		name string
		req  PublicAccountImportUpstreamRequest
	}{
		{name: "name", req: PublicAccountImportUpstreamRequest{Name: strings.Repeat("界", 101), BaseURL: "https://example.com", APIKey: "key", GroupIDs: []int64{5}}},
		{name: "url", req: PublicAccountImportUpstreamRequest{BaseURL: "https://example.com/" + strings.Repeat("a", 2048), APIKey: "key", GroupIDs: []int64{5}}},
		{name: "key", req: PublicAccountImportUpstreamRequest{BaseURL: "https://example.com", APIKey: strings.Repeat("k", publicUpstreamImportMaxKeyBytes+1), GroupIDs: []int64{5}}},
		{name: "key control character", req: PublicAccountImportUpstreamRequest{BaseURL: "https://example.com", APIKey: "key\tvalue", GroupIDs: []int64{5}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := h.normalizePublicUpstreamImport(t.Context(), test.req)
			require.Error(t, err)
		})
	}
}

func newPublicUpstreamImportTestHandler(svc service.AdminService, cfg *config.Config) *AccountHandler {
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetURLPolicyConfig(cfg)
	return h
}

func publicUpstreamImportTestGroups() []service.Group {
	return []service.Group{
		{ID: 5, Name: "K12", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
		{ID: 6, Name: "ALL", Platform: service.PlatformOpenAI, Status: service.StatusActive},
		{ID: 9, Name: "PLUS", Platform: service.PlatformOpenAI, Status: service.StatusActive, PublicStatusEnabled: true},
	}
}

func performPublicUpstreamImportRequest(
	t *testing.T,
	h *AccountHandler,
	payload PublicAccountImportUpstreamRequest,
	idempotencyKey string,
	userID int64,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
		c.Next()
	})
	router.POST("/api/v1/user/account-import/upstream", h.ImportPublicUpstreamAccount)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/user/account-import/upstream", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}
