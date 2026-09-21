package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexSyncSettingRepoStub struct {
	SettingRepository
	writes map[string]string
	err    error
}

func (r *codexSyncSettingRepoStub) GetAll(_ context.Context) (map[string]string, error) {
	values := make(map[string]string, len(r.writes))
	for key, value := range r.writes {
		values[key] = value
	}
	return values, nil
}

func (r *codexSyncSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.writes[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func (r *codexSyncSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	if r.err != nil {
		return r.err
	}
	if r.writes == nil {
		r.writes = make(map[string]string)
	}
	for key, value := range values {
		r.writes[key] = value
	}
	return nil
}

type codexSyncAccountRepoStub struct {
	AccountRepository
	accounts []Account
	changed  []int64
}

func (r *codexSyncAccountRepoStub) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	result := make([]Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			result = append(result, account)
		}
	}
	return result, nil
}

func (r *codexSyncAccountRepoStub) UpdateCodexTicketBusinessProxy(_ context.Context, _ int64, ids []int64) ([]int64, error) {
	r.changed = append([]int64(nil), ids...)
	return append([]int64(nil), ids...), nil
}

type codexSyncProxyRepoStub struct {
	ProxyRepository
	proxy *Proxy
	spec  CodexTicketProxySpec
	err   error
}

func (r *codexSyncProxyRepoStub) FindOrCreateCodexTicketProxy(_ context.Context, spec CodexTicketProxySpec) (*Proxy, error) {
	r.spec = spec
	if r.err != nil {
		return nil, r.err
	}
	return r.proxy, nil
}

func TestNormalizeCodexTicketProxyURL(t *testing.T) {
	spec, err := NormalizeCodexTicketProxyURL("SOCKS5H://user:secret@Proxy.Example.com")
	require.NoError(t, err)
	require.Equal(t, CodexTicketProxySpec{
		Protocol: "socks5h",
		Host:     "proxy.example.com",
		Port:     1080,
		Username: "user",
		Password: "secret",
	}, spec)

	_, err = NormalizeCodexTicketProxyURL("http://user:secret@host:bad")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
}

func TestCodexTicketSyncBusinessProxyDefaultsFalseAndParsesFalse(t *testing.T) {
	repo := &codexSyncSettingRepoStub{writes: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	settings, err := svc.GetAllSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.OpenAICodexTicketSyncBusinessProxy)

	repo.writes[SettingKeyOpenAICodexTicketSyncBusinessProxy] = "false"
	settings, err = svc.GetAllSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.OpenAICodexTicketSyncBusinessProxy)
}

func TestCodexTicketProxySyncFiltersTargetsAndPersistsSettings(t *testing.T) {
	activeOAuth := Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	setupToken := Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Status: StatusActive}
	shadowParent := int64(7)
	shadow := Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ParentAccountID: &shadowParent}
	disabled := Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusDisabled}

	settingsRepo := &codexSyncSettingRepoStub{}
	accounts := &codexSyncAccountRepoStub{accounts: []Account{activeOAuth, setupToken, shadow, disabled}}
	proxies := &codexSyncProxyRepoStub{proxy: &Proxy{ID: 42, Status: StatusActive}}
	svc := NewCodexTicketProxySyncService(nil, settingsRepo, accounts, proxies, nil)

	err := svc.PersistSettings(context.Background(), map[string]string{"some_setting": "value"}, &SystemSettings{
		OpenAICodexTicketSyncBusinessProxy: true,
		OpenAICodexTicketHarvestProxyURL:   "http://user:secret@proxy.example.com:8080",
		OpenAICodexTicketAccountIDs:        []int64{7, 8, 9, 10},
	})
	require.NoError(t, err)
	require.Equal(t, int64(42), proxies.proxy.ID)
	require.Equal(t, []int64{7}, accounts.changed)
	require.Equal(t, "proxy.example.com", proxies.spec.Host)
	require.Equal(t, "value", settingsRepo.writes["some_setting"])
}

func TestCodexTicketProxySyncDisabledPreservesAccounts(t *testing.T) {
	settingsRepo := &codexSyncSettingRepoStub{}
	accounts := &codexSyncAccountRepoStub{accounts: []Account{{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}}}
	proxies := &codexSyncProxyRepoStub{err: errors.New("resolver must not be called")}
	svc := NewCodexTicketProxySyncService(nil, settingsRepo, accounts, proxies, nil)

	err := svc.PersistSettings(context.Background(), map[string]string{"sync": "false"}, &SystemSettings{
		OpenAICodexTicketSyncBusinessProxy: false,
		OpenAICodexTicketHarvestProxyURL:   "http://proxy.example.com:8080",
	})
	require.NoError(t, err)
	require.Empty(t, accounts.changed)
	require.Empty(t, proxies.spec)
	require.Equal(t, "false", settingsRepo.writes["sync"])
}

func TestCodexTicketProxySyncMultiEntryPoolIsHarvestOnly(t *testing.T) {
	settingsRepo := &codexSyncSettingRepoStub{}
	accounts := &codexSyncAccountRepoStub{accounts: []Account{{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}}}
	proxies := &codexSyncProxyRepoStub{err: errors.New("multi-entry pool must not resolve a business proxy")}
	svc := NewCodexTicketProxySyncService(nil, settingsRepo, accounts, proxies, nil)
	settings := &SystemSettings{
		OpenAICodexTicketSyncBusinessProxy: true,
		OpenAICodexTicketHarvestProxyURL:   "http://listener-a.example:17891\nhttp://listener-b.example:17892",
	}
	require.NoError(t, svc.PersistSettings(context.Background(), map[string]string{"unrelated": "1"}, settings))
	require.False(t, settings.OpenAICodexTicketSyncBusinessProxy)
	require.Empty(t, accounts.changed)
	require.Empty(t, proxies.spec)
	require.Equal(t, "false", settingsRepo.writes[SettingKeyOpenAICodexTicketSyncBusinessProxy])
	require.Equal(t, "1", settingsRepo.writes["unrelated"])
}

func TestCodexTicketProxySyncUsesStartupFallbackWhenSettingIsOmitted(t *testing.T) {
	settingsRepo := &codexSyncSettingRepoStub{}
	accounts := &codexSyncAccountRepoStub{accounts: []Account{{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}}}
	proxies := &codexSyncProxyRepoStub{proxy: &Proxy{ID: 42, Status: StatusActive}}
	svc := NewCodexTicketProxySyncService(nil, settingsRepo, accounts, proxies, nil)
	svc.SetFallbackHarvestProxyURL("http://fallback.example.com:8080")

	err := svc.PersistSettings(context.Background(), map[string]string{"unrelated": "1"}, &SystemSettings{
		OpenAICodexTicketSyncBusinessProxy: true,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{7}, accounts.changed)
	require.Equal(t, "fallback.example.com", proxies.spec.Host)

	accounts.changed = nil
	err = svc.PersistSettings(context.Background(), map[string]string{SettingKeyOpenAICodexTicketHarvestProxyURL: ""}, &SystemSettings{
		OpenAICodexTicketSyncBusinessProxy: true,
	})
	require.NoError(t, err)
	require.Empty(t, accounts.changed)
}
