package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	openaipkg "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type mutableOpenAITokenCache struct {
	mu      sync.Mutex
	token   string
	deletes int
}

func (c *mutableOpenAITokenCache) GetAccessToken(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		return "", errors.New("token not found")
	}
	return c.token, nil
}

func (c *mutableOpenAITokenCache) SetAccessToken(_ context.Context, _ string, token string, _ time.Duration) error {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return nil
}

func (c *mutableOpenAITokenCache) DeleteAccessToken(context.Context, string) error {
	c.mu.Lock()
	c.token = ""
	c.deletes++
	c.mu.Unlock()
	return nil
}

func (*mutableOpenAITokenCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func (*mutableOpenAITokenCache) ReleaseRefreshLock(context.Context, string) error { return nil }

func TestAcquireOpenAIAuthenticatedAccountSnapshotDiscardsStaleCachedToken(t *testing.T) {
	requested := newOpenAI5hWakeAccount(904, "workspace-before")
	requested.Credentials["access_token"] = "token-before"
	durable := newOpenAI5hWakeAccount(904, "workspace-after")
	durable.Credentials["access_token"] = "token-after"
	durable.Credentials["chatgpt_user_id"] = "user-after"
	repo := &quotaRefreshSnapshotRepo{account: durable}
	cache := &mutableOpenAITokenCache{token: "token-before"}
	provider := NewOpenAITokenProvider(repo, cache, nil)

	token, snapshot, err := acquireOpenAIAuthenticatedAccountSnapshot(context.Background(), repo, provider, requested)

	require.NoError(t, err)
	require.Equal(t, "token-after", token)
	require.Equal(t, "workspace-after", openAIQuotaAccountID(snapshot))
	require.Equal(t, "user-after", snapshot.GetCredential("chatgpt_user_id"))
	require.Equal(t, 1, cache.deletes)
}

func TestOpenAI5hWakeRequestUsesAuthenticatedDurableAccountSnapshot(t *testing.T) {
	requested := newOpenAI5hWakeAccount(905, "workspace-before")
	requested.Credentials["access_token"] = "token-before"

	proxyID := int64(41)
	durable := newOpenAI5hWakeAccount(905, "workspace-after")
	durable.Credentials["access_token"] = "token-after"
	durable.Credentials["chatgpt_user_id"] = "user-after"
	durable.Credentials["chatgpt_account_is_fedramp"] = true
	durable.Credentials["model_mapping"] = map[string]any{openaipkg.DefaultTestModel: "after-model"}
	durable.ProxyID = &proxyID
	durable.Proxy = &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 19041}
	durable.Concurrency = 7

	repo := &quotaRefreshSnapshotRepo{account: durable}
	cache := &mutableOpenAITokenCache{token: "token-before"}
	provider := NewOpenAITokenProvider(repo, cache, nil)
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{openAI5hWakeHeaders(http.StatusOK)}}
	wake := &OpenAI5hWakeService{accountRepo: repo, tokenProvider: provider, httpUpstream: upstream}

	result := wake.sendMinimumWakeRequest(context.Background(), requested)

	require.NoError(t, result.err)
	require.NotNil(t, result.requestAccount)
	require.Equal(t, "workspace-after", openAIQuotaAccountID(result.requestAccount))
	require.Equal(t, "token-after", result.requestAccount.GetOpenAIAccessToken())
	require.Len(t, upstream.requests, 1)
	captured := upstream.requests[0]
	require.Equal(t, "Bearer token-after", captured.headers.Get("Authorization"))
	require.Equal(t, "workspace-after", captured.headers.Get("chatgpt-account-id"))
	require.Equal(t, "true", captured.headers.Get("x-openai-fedramp"))
	require.Equal(t, "http://127.0.0.1:19041", captured.proxyURL)
	require.Equal(t, 7, captured.concurrency)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(captured.body, &payload))
	require.Equal(t, "after-model", payload["model"])
	require.Equal(t, 1, cache.deletes)
}

func TestOpenAI5hWakeRequestRejectsDurableIdentityChangeBeforeSending(t *testing.T) {
	requested := newOpenAI5hWakeAccount(907, "workspace-before")
	requested.Credentials["access_token"] = "token-before"
	durable := newOpenAI5hWakeAccount(907, "workspace-after")
	durable.Credentials["access_token"] = "token-after"

	repo := &quotaRefreshSnapshotRepo{account: durable}
	cache := &mutableOpenAITokenCache{token: "token-before"}
	provider := NewOpenAITokenProvider(repo, cache, nil)
	upstream := &openAI5hWakeHTTPStub{}
	wake := &OpenAI5hWakeService{accountRepo: repo, tokenProvider: provider, httpUpstream: upstream}

	result := wake.sendMinimumWakeRequest(
		context.Background(),
		requested,
		openAI5hWakeIdentityHash(requested),
	)

	require.ErrorIs(t, result.err, errOpenAI5hWakeIdentityChanged)
	require.Equal(t, "identity_check", result.phase)
	require.Equal(t, "identity_changed", result.errorCode)
	require.NotNil(t, result.requestAccount)
	require.Equal(t, "workspace-after", openAIQuotaAccountID(result.requestAccount))
	require.Empty(t, upstream.requests)
	require.Equal(t, 1, cache.deletes)
}

func TestOpenAI5hWakeRequestReportsDurableEntitlementChangeBeforeSending(t *testing.T) {
	requested := newOpenAI5hWakeAccount(910, "workspace")
	durable := newOpenAI5hWakeAccount(910, "workspace")
	durable.Credentials["plan_type"] = "free"

	repo := &quotaRefreshSnapshotRepo{account: durable}
	provider := NewOpenAITokenProvider(repo, nil, nil)
	upstream := &openAI5hWakeHTTPStub{}
	wake := &OpenAI5hWakeService{accountRepo: repo, tokenProvider: provider, httpUpstream: upstream}

	result := wake.sendMinimumWakeRequest(
		context.Background(),
		requested,
		openAI5hWakeIdentityHash(requested),
	)

	require.Error(t, result.err)
	require.Equal(t, "eligibility_check", result.phase)
	require.Equal(t, "no_5h_entitlement", result.errorCode)
	require.Empty(t, upstream.requests)
}

func TestOpenAIUsageProbePreparesAuthenticatedDurableAccountSnapshot(t *testing.T) {
	requested := newOpenAI5hWakeAccount(906, "workspace-before")
	requested.Credentials["access_token"] = "token-before"
	requested.Credentials["chatgpt_user_id"] = "user-before"

	proxyID := int64(42)
	durable := newOpenAI5hWakeAccount(906, "workspace-after")
	durable.Credentials["access_token"] = "token-after"
	durable.Credentials["chatgpt_user_id"] = "user-after"
	durable.Credentials["chatgpt_account_is_fedramp"] = true
	durable.ProxyID = &proxyID
	durable.Proxy = &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 19042}

	repo := &quotaRefreshSnapshotRepo{account: durable}
	cache := &mutableOpenAITokenCache{token: "token-before"}
	provider := NewOpenAITokenProvider(repo, cache, nil)
	usage := &AccountUsageService{
		accountRepo: repo,
		openAIQuotaService: &OpenAIQuotaService{
			tokenProvider: provider,
		},
	}

	prepared, err := usage.prepareOpenAICodexProbeAccount(context.Background(), requested)

	require.NoError(t, err)
	require.Equal(t, "token-after", prepared.GetOpenAIAccessToken())
	require.Equal(t, "workspace-after", openAIQuotaAccountID(prepared))
	require.Equal(t, "user-after", prepared.GetCredential("chatgpt_user_id"))
	require.True(t, prepared.IsChatGPTAccountFedRAMP())
	require.Equal(t, "http://127.0.0.1:19042", prepared.Proxy.URL())
	require.Equal(t, 1, cache.deletes)
}
