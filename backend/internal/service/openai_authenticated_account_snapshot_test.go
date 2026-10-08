package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
	requested := newAuthenticatedOpenAIAccount(904, "workspace-before")
	requested.Credentials["access_token"] = "token-before"
	durable := newAuthenticatedOpenAIAccount(904, "workspace-after")
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

func TestOpenAIUsageProbePreparesAuthenticatedDurableAccountSnapshot(t *testing.T) {
	requested := newAuthenticatedOpenAIAccount(906, "workspace-before")
	requested.Credentials["access_token"] = "token-before"
	requested.Credentials["chatgpt_user_id"] = "user-before"

	proxyID := int64(42)
	durable := newAuthenticatedOpenAIAccount(906, "workspace-after")
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

func newAuthenticatedOpenAIAccount(id int64, identity string) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Credentials: map[string]any{
			"chatgpt_account_id": identity,
			"access_token":       "token",
			"expires_at":         time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
		Extra: map[string]any{},
	}
}
