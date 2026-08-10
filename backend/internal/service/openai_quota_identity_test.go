package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestQueryUsageValidatesUpstreamIdentity(t *testing.T) {
	tests := []struct {
		name            string
		credentials     map[string]any
		response        OpenAIQuotaUsage
		wantHeader      string
		wantErrorCode   string
		wantErrorStatus int
	}{
		{
			name: "matching account and user",
			credentials: map[string]any{
				"chatgpt_account_id": "account-a",
				"chatgpt_user_id":    "user-a",
			},
			response: OpenAIQuotaUsage{
				AccountID: "account-a",
				UserID:    "user-a",
				PlanType:  "pro",
			},
			wantHeader: "account-a",
		},
		{
			name: "account mismatch is rejected",
			credentials: map[string]any{
				"chatgpt_account_id": "account-a",
				"chatgpt_user_id":    "user-a",
			},
			response: OpenAIQuotaUsage{
				AccountID: "account-b",
				UserID:    "user-a",
			},
			wantHeader:      "account-a",
			wantErrorCode:   "OPENAI_QUOTA_IDENTITY_MISMATCH",
			wantErrorStatus: http.StatusBadGateway,
		},
		{
			name: "user mismatch is rejected",
			credentials: map[string]any{
				"chatgpt_account_id": "account-a",
				"chatgpt_user_id":    "user-a",
			},
			response: OpenAIQuotaUsage{
				AccountID: "account-a",
				UserID:    "user-b",
			},
			wantHeader:      "account-a",
			wantErrorCode:   "OPENAI_QUOTA_IDENTITY_MISMATCH",
			wantErrorStatus: http.StatusBadGateway,
		},
		{
			name: "missing response identity remains compatible",
			credentials: map[string]any{
				"chatgpt_account_id": "account-a",
				"chatgpt_user_id":    "user-a",
			},
			response:   OpenAIQuotaUsage{},
			wantHeader: "account-a",
		},
		{
			name:        "legacy organization id is the expected account id",
			credentials: map[string]any{"organization_id": "legacy-org"},
			response: OpenAIQuotaUsage{
				AccountID: "legacy-org",
			},
			wantHeader: "legacy-org",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credentials := shallowCopyMap(tt.credentials)
			credentials["access_token"] = "identity-test-token"
			account := &Account{
				ID:          901,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Credentials: credentials,
			}
			repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: account}}
			tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
				OpenAITokenCacheKey(account): "identity-test-token",
			}}
			tokenProvider := NewOpenAITokenProvider(repo, tokenCache, nil)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, tt.wantHeader, r.Header.Get("chatgpt-account-id"))
				w.Header().Set("content-type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			defer srv.Close()

			svc := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(srv))
			usage, err := svc.QueryUsageLightweight(context.Background(), account.ID)
			if tt.wantErrorCode == "" {
				require.NoError(t, err)
				require.NotNil(t, usage)
				return
			}

			require.Error(t, err)
			require.Equal(t, tt.wantErrorCode, infraerrors.Reason(err))
			require.Equal(t, tt.wantErrorStatus, infraerrors.Code(err))
			require.NotContains(t, err.Error(), "account-b")
			require.NotContains(t, err.Error(), "user-b")
		})
	}
}

// quotaRefreshSnapshotRepo returns detached account snapshots so a refresh
// cannot accidentally mutate the snapshot already held by the quota service.
// The second read must therefore be what supplies the request identity.
type quotaRefreshSnapshotRepo struct {
	AccountRepository
	account *Account
}

func (r *quotaRefreshSnapshotRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account == nil || r.account.ID != id {
		return nil, nil
	}
	copy := *r.account
	copy.Credentials = shallowCopyMap(r.account.Credentials)
	if r.account.ProxyID != nil {
		proxyID := *r.account.ProxyID
		copy.ProxyID = &proxyID
	}
	return &copy, nil
}

func (r *quotaRefreshSnapshotRepo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	if r.account == nil || r.account.ID != id {
		return nil
	}
	r.account.Credentials = shallowCopyMap(credentials)
	return nil
}

type quotaRefreshExecutor struct{}

func (quotaRefreshExecutor) CanRefresh(*Account) bool { return true }

func (quotaRefreshExecutor) NeedsRefresh(*Account, time.Duration) bool { return true }

func (quotaRefreshExecutor) Refresh(context.Context, *Account) (map[string]any, error) {
	return map[string]any{
		"access_token":       "refreshed-identity-token",
		"refresh_token":      "refreshed-refresh-token",
		"expires_at":         time.Now().Add(time.Hour).Format(time.RFC3339),
		"chatgpt_account_id": "account-after-refresh",
		"chatgpt_user_id":    "user-after-refresh",
	}, nil
}

func (quotaRefreshExecutor) CacheKey(*Account) string { return "quota-refresh-test" }

func TestQueryUsageUsesIdentityFromPostRefreshAccountSnapshot(t *testing.T) {
	account := &Account{
		ID:       902,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":       "stale-token",
			"refresh_token":      "stale-refresh-token",
			"expires_at":         time.Now().Add(time.Minute).Format(time.RFC3339),
			"chatgpt_account_id": "account-before-refresh",
			"chatgpt_user_id":    "user-before-refresh",
		},
	}
	repo := &quotaRefreshSnapshotRepo{account: account}
	refreshAPI := NewOAuthRefreshAPI(repo, nil)
	tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
	tokenProvider.SetRefreshAPI(refreshAPI, quotaRefreshExecutor{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "account-after-refresh", r.Header.Get("chatgpt-account-id"))
		w.Header().Set("content-type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(OpenAIQuotaUsage{
			AccountID: "account-after-refresh",
			UserID:    "user-after-refresh",
		}))
	}))
	defer srv.Close()

	service := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(srv))
	usage, err := service.QueryUsageLightweight(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "account-after-refresh", usage.AccountID)
}

type nilQuotaAccountRepo struct{ AccountRepository }

func (nilQuotaAccountRepo) GetByID(context.Context, int64) (*Account, error) { return nil, nil }

func TestResetCreditHandlesMissingAccountWithoutPanic(t *testing.T) {
	service := NewOpenAIQuotaService(nilQuotaAccountRepo{}, nil, nil, nil)
	_, err := service.ResetCredit(context.Background(), 903)
	require.Error(t, err)
	require.Equal(t, "OPENAI_QUOTA_ACCOUNT_NOT_FOUND", infraerrors.Reason(err))
}

func TestBuildCodexQuotaHeadersKeepsPreparedOAuthMode(t *testing.T) {
	concurrentlyChanged := &Account{
		ID:       908,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":          OpenAIAuthModeAgentIdentity,
			"chatgpt_account_id": "agent-workspace",
		},
	}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{concurrentlyChanged.ID: concurrentlyChanged}}
	service := &OpenAIQuotaService{accountRepo: repo}

	headers, taskID, err := service.buildCodexQuotaHeaders(
		context.Background(), concurrentlyChanged.ID, "prepared-oauth-token", "oauth-workspace", false,
	)

	require.NoError(t, err)
	require.Empty(t, taskID)
	require.Equal(t, "Bearer prepared-oauth-token", headers["authorization"])
	require.Equal(t, "oauth-workspace", headers["chatgpt-account-id"])
}

func TestBuildCodexQuotaHeadersRejectsPreparedAgentModeChange(t *testing.T) {
	concurrentlyChanged := &Account{
		ID:       909,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "new-oauth-token",
			"chatgpt_account_id": "workspace",
		},
	}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{concurrentlyChanged.ID: concurrentlyChanged}}
	service := &OpenAIQuotaService{accountRepo: repo}

	_, _, err := service.buildCodexQuotaHeaders(
		context.Background(), concurrentlyChanged.ID, "", "workspace", false,
	)

	require.ErrorContains(t, err, "authentication mode changed")
}
