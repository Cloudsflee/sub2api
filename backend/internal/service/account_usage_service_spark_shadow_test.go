package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// sparkShadowUsageTestRepo is a minimal AccountRepository stub for spark shadow
// usage tests.  GetByID serves both shadow and parent accounts from a map;
// UpdateExtra records the persisted updates for assertion.
type sparkShadowUsageTestRepo struct {
	AccountRepository
	mu            sync.RWMutex
	accounts      map[int64]*Account
	updateExtraCh chan map[string]any
	clearCalls    atomic.Int32
	getByIDCalls  atomic.Int32
	parentID      int64
	parentCalls   atomic.Int32
}

// monotonicSparkUsageTestRepo simulates PostgreSQL matching the identity row
// while its CASE guard keeps an already newer managed snapshot. RowsAffected is
// still one in that situation, which is why the service must reload the row.
type monotonicSparkUsageTestRepo struct {
	*sparkShadowUsageTestRepo
	casCalls atomic.Int32
}

func (r *monotonicSparkUsageTestRepo) UpdateOpenAICodexSnapshot(
	context.Context,
	int64,
	*Account,
	map[string]any,
	map[string]any,
) (bool, error) {
	r.casCalls.Add(1)
	return true, nil
}

func (r *sparkShadowUsageTestRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.getByIDCalls.Add(1)
	if id == r.parentID {
		r.parentCalls.Add(1)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if acc, ok := r.accounts[id]; ok {
		return cloneSparkShadowUsageTestAccount(acc), nil
	}
	return nil, fmt.Errorf("account %d not found", id)
}

func cloneSparkShadowUsageTestAccount(account *Account) *Account {
	if account == nil {
		return nil
	}
	cloned := *account
	cloned.Credentials = shallowCopyMap(account.Credentials)
	cloned.Extra = shallowCopyMap(account.Extra)
	return &cloned
}

func TestGetOpenAIUsage_SparkShadowFreshSnapshotDoesNotLoadParent(t *testing.T) {
	cache := NewUsageCache()
	svc, shadow, repo := newSparkUsageTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("fresh persisted usage must not contact the upstream")
		w.WriteHeader(http.StatusInternalServerError)
	}, cache)
	shadow.Extra = map[string]any{
		"codex_usage_updated_at": time.Now().UTC().Format(time.RFC3339),
		"codex_5h_used_percent":  10.0,
		"codex_5h_reset_at":      time.Now().Add(time.Hour).Format(time.RFC3339),
		"codex_7d_used_percent":  20.0,
		"codex_7d_reset_at":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}
	delete(repo.accounts, *shadow.ParentAccountID)
	repo.parentID = *shadow.ParentAccountID

	usage, err := svc.getOpenAIUsage(context.Background(), shadow, false)

	require.NoError(t, err)
	require.NotNil(t, usage.FiveHour)
	require.NotNil(t, usage.SevenDay)
	require.Zero(t, repo.parentCalls.Load(), "a fresh shadow snapshot must not reload the parent identity")
}

func (r *sparkShadowUsageTestRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	if account := r.accounts[id]; account != nil {
		updated := cloneSparkShadowUsageTestAccount(account)
		if updated.Extra == nil {
			updated.Extra = make(map[string]any)
		}
		for key, value := range updates {
			updated.Extra[key] = value
		}
		r.accounts[id] = updated
	}
	r.mu.Unlock()
	if r.updateExtraCh != nil {
		copied := make(map[string]any, len(updates))
		for k, v := range updates {
			copied[k] = v
		}
		r.updateExtraCh <- copied
	}
	return nil
}

func (r *sparkShadowUsageTestRepo) ClearError(_ context.Context, id int64) error {
	r.clearCalls.Add(1)
	r.mu.Lock()
	if account := r.accounts[id]; account != nil {
		updated := cloneSparkShadowUsageTestAccount(account)
		updated.Status = StatusActive
		updated.ErrorMessage = ""
		r.accounts[id] = updated
	}
	r.mu.Unlock()
	return nil
}

func newSparkUsageTestService(
	t *testing.T,
	handler http.HandlerFunc,
	cache *UsageCache,
) (*AccountUsageService, *Account, *sparkShadowUsageTestRepo) {
	t.Helper()
	parentID := int64(100)
	shadow := &Account{
		ID: 200, ParentAccountID: &parentID, Platform: PlatformOpenAI,
		Type: AccountTypeOAuth, Status: StatusActive, QuotaDimension: QuotaDimensionSpark,
	}
	parent := &Account{
		ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": "org-spark-parent", "access_token": "fake-access-token"},
	}
	repo := &sparkShadowUsageTestRepo{accounts: map[int64]*Account{shadow.ID: shadow, parent.ID: parent}}
	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		OpenAITokenCacheKey(parent): "fake-access-token",
	}}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	quotaService := NewOpenAIQuotaService(
		repo, nil, NewOpenAITokenProvider(repo, tokenCache, nil), newQuotaRedirectingFactory(server),
	)
	return &AccountUsageService{
		accountRepo: repo, openAIQuotaService: quotaService, cache: cache,
	}, shadow, repo
}

func writeSparkUsageResponse(w http.ResponseWriter) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(OpenAIQuotaUsage{
		AdditionalRateLimits: []OpenAIAdditionalRateLimit{{
			MeteredFeature: "codex_bengalfox",
			RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{
					UsedPercent: 25, ResetAfterSeconds: 3600, LimitWindowSeconds: 18000,
				},
				SecondaryWindow: &OpenAIRateLimitWindow{
					UsedPercent: 50, ResetAfterSeconds: 86400, LimitWindowSeconds: 604800,
				},
			},
		}},
	})
}

// TestGetOpenAIUsage_SparkShadow_WritesExtraAndReturnsNonEmptyWindows covers
// two assertions required by Task 3.2:
//
// A) After getOpenAIUsage on a spark shadow account the shadow row's
// Extra["codex_5h_used_percent"] is persisted, and the upstream call carried
// the PARENT account's chatgpt-account-id (not the shadow's empty one).
//
// B) (P1-b regression guard) The UsageInfo RETURNED by the same call has
// non-nil FiveHour AND SevenDay windows — proving that the rebuild happened
// and not just the DB write.
func TestGetOpenAIUsage_SparkShadow_WritesExtraAndReturnsNonEmptyWindows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pid := int64(100)
	shadow := &Account{
		ID:              200,
		ParentAccountID: &pid,
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		Status:          StatusActive,
		QuotaDimension:  QuotaDimensionSpark,
	}
	parent := &Account{
		ID:       100,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"chatgpt_account_id": "org-spark-parent",
			"access_token":       "fake-access-token",
		},
	}

	// Repo shared by both the OpenAIQuotaService (needs shadow+parent for resolve)
	// and the AccountUsageService (needs UpdateExtra for persist).
	updateExtraCh := make(chan map[string]any, 1)
	repo := &sparkShadowUsageTestRepo{
		accounts:      map[int64]*Account{200: shadow, 100: parent},
		updateExtraCh: updateExtraCh,
	}

	// Token cache: return a fake token for the parent account key.
	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		OpenAITokenCacheKey(parent): "fake-access-token",
	}}
	tokenProvider := NewOpenAITokenProvider(repo, tokenCache, nil)

	// httptest server: records the chatgpt-account-id header and returns a
	// synthetic OpenAIQuotaUsage with codex_bengalfox 5h+7d windows.
	var capturedAccountID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAccountID = r.Header.Get("chatgpt-account-id")
		w.Header().Set("content-type", "application/json")
		resp := OpenAIQuotaUsage{
			AdditionalRateLimits: []OpenAIAdditionalRateLimit{
				{
					MeteredFeature: "codex_bengalfox",
					RateLimit: &OpenAIRateLimit{
						// Primary window → 5h (18000 s = 300 min)
						PrimaryWindow: &OpenAIRateLimitWindow{
							UsedPercent:        42.5,
							ResetAfterSeconds:  3600,
							LimitWindowSeconds: 18000,
						},
						// Secondary window → 7d (604800 s = 10080 min)
						SecondaryWindow: &OpenAIRateLimitWindow{
							UsedPercent:        10.0,
							ResetAfterSeconds:  86400,
							LimitWindowSeconds: 604800,
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	quotaService := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(srv))
	svc := &AccountUsageService{
		accountRepo:        repo,
		openAIQuotaService: quotaService,
	}

	usage, err := svc.getOpenAIUsage(ctx, shadow, true /*force*/)
	require.NoError(t, err)

	// Assertion A-1: upstream received the PARENT's chatgpt-account-id.
	require.Equal(t, "org-spark-parent", capturedAccountID,
		"QueryUsage must use parent's chatgpt-account-id for spark shadow accounts")

	// Assertion A-2: shadow Extra was persisted with codex_5h_used_percent.
	select {
	case updates := <-updateExtraCh:
		require.Contains(t, updates, "codex_5h_used_percent",
			"persisted extra must contain codex_5h_used_percent")
		require.InDelta(t, 42.5, updates["codex_5h_used_percent"], 0.01,
			"codex_5h_used_percent must match the upstream value")
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateExtra was not called within timeout — spark shadow persist did not happen")
	}

	// Assertion B (P1-b regression guard): returned UsageInfo must have
	// non-nil windows. This FAILS if the code only writes Extra without
	// rebuilding the returned UsageInfo.
	require.NotNil(t, usage.FiveHour,
		"returned UsageInfo.FiveHour must be non-nil (rebuild from merged Extra must happen)")
	require.NotNil(t, usage.SevenDay,
		"returned UsageInfo.SevenDay must be non-nil (rebuild from merged Extra must happen)")
}

func TestGetOpenAIUsageReturnsAuthoritativeSnapshotWhenLateProbeLosesMonotonicCAS(t *testing.T) {
	parentID := int64(301)
	shadow := &Account{
		ID: 302, ParentAccountID: &parentID, Platform: PlatformOpenAI,
		Type: AccountTypeOAuth, Status: StatusActive, QuotaDimension: QuotaDimensionSpark,
		Extra: map[string]any{
			"codex_usage_updated_at":              time.Now().UTC().Format(time.RFC3339),
			"codex_5h_used_percent":               88.0,
			"codex_5h_reset_at":                   time.Now().Add(4 * time.Hour).UTC().Format(time.RFC3339),
			"codex_7d_used_percent":               77.0,
			"codex_7d_reset_at":                   time.Now().Add(6 * 24 * time.Hour).UTC().Format(time.RFC3339),
			OpenAICodexSnapshotObservedAtExtraKey: "99999999999999999999",
		},
	}
	parent := &Account{
		ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{
			"chatgpt_account_id": "authoritative-workspace",
			"access_token":       "authoritative-token",
		},
	}
	baseRepo := &sparkShadowUsageTestRepo{accounts: map[int64]*Account{shadow.ID: shadow, parent.ID: parent}}
	repo := &monotonicSparkUsageTestRepo{sparkShadowUsageTestRepo: baseRepo}
	cache := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(parent): "authoritative-token"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(OpenAIQuotaUsage{
			AdditionalRateLimits: []OpenAIAdditionalRateLimit{{
				MeteredFeature: "codex_bengalfox",
				RateLimit: &OpenAIRateLimit{
					PrimaryWindow:   &OpenAIRateLimitWindow{UsedPercent: 42, ResetAfterSeconds: 3600, LimitWindowSeconds: 18000},
					SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 24, ResetAfterSeconds: 86400, LimitWindowSeconds: 604800},
				},
			}},
		})
	}))
	defer server.Close()
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, cache, nil), newQuotaRedirectingFactory(server))
	service := &AccountUsageService{accountRepo: repo, openAIQuotaService: quota, cache: NewUsageCache()}

	usage, err := service.getOpenAIUsage(context.Background(), shadow, true)

	require.NoError(t, err)
	require.Equal(t, int32(1), repo.casCalls.Load())
	require.NotNil(t, usage.FiveHour)
	require.NotNil(t, usage.SevenDay)
	require.Equal(t, 88.0, usage.FiveHour.Utilization)
	require.Equal(t, 77.0, usage.SevenDay.Utilization)
	require.Equal(t, 88.0, shadow.Extra["codex_5h_used_percent"])
	require.NotEqual(t, 42.0, usage.FiveHour.Utilization)
}

func TestGetOpenAIUsageForceReportsMissingSparkSnapshot(t *testing.T) {
	cache := NewUsageCache()
	svc, shadow, _ := newSparkUsageTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}, cache)

	usage, err := svc.getOpenAIUsage(context.Background(), shadow, true)

	require.ErrorIs(t, err, errOpenAIUsageProbeNoSnapshot)
	require.NotNil(t, usage)
	var cached bool
	cache.openAIProbeCache.Range(func(_, _ any) bool { cached = true; return false })
	require.False(t, cached, "a response without a quota snapshot must not consume the probe cooldown")
}

func TestGetOpenAIUsageAutomaticFailureKeepsStaleDataAndRetries(t *testing.T) {
	cache := NewUsageCache()
	var requests atomic.Int32
	svc, shadow, _ := newSparkUsageTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}, cache)
	shadow.Extra = map[string]any{
		"codex_usage_updated_at": time.Now().Add(-openAIProbeCacheTTL - time.Minute).Format(time.RFC3339),
		"codex_5h_used_percent":  19.0,
		"codex_5h_reset_at":      time.Now().Add(time.Hour).Format(time.RFC3339),
		"codex_7d_used_percent":  29.0,
		"codex_7d_reset_at":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}

	for range 2 {
		usage, err := svc.getOpenAIUsage(context.Background(), shadow, false)
		require.NoError(t, err)
		require.NotNil(t, usage.FiveHour)
		require.Equal(t, 19.0, usage.FiveHour.Utilization)
	}

	require.Equal(t, int32(2), requests.Load(), "a failed probe must be eligible for the next retry")
	var cached bool
	cache.openAIProbeCache.Range(func(_, _ any) bool { cached = true; return false })
	require.False(t, cached)
}

func TestRefreshOpenAIUsageSingleflightCachesOnlySuccessfulSnapshot(t *testing.T) {
	cache := NewUsageCache()
	var requests atomic.Int32
	var usageRequests atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	svc, shadow, _ := newSparkUsageTestService(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.Contains(r.URL.Path, "/wham/usage") {
			usageRequests.Add(1)
		}
		enteredOnce.Do(func() { close(entered) })
		<-release
		writeSparkUsageResponse(w)
	}, cache)

	type result struct {
		updates map[string]any
		err     error
	}
	results := make(chan result, 2)
	go func() {
		updates, err := svc.refreshOpenAICodexSnapshot(context.Background(), shadow)
		results <- result{updates: updates, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first usage probe did not reach the upstream")
	}
	go func() {
		updates, err := svc.refreshOpenAICodexSnapshot(context.Background(), shadow)
		results <- result{updates: updates, err: err}
	}()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, int32(1), requests.Load(), "concurrent probes for one account must share one upstream call")
	close(release)

	for range 2 {
		select {
		case got := <-results:
			require.NoError(t, got.err)
			require.Contains(t, got.updates, "codex_5h_used_percent")
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent usage probe did not complete")
		}
	}
	require.Equal(t, int32(1), usageRequests.Load(), "duplicate callers must share the one quota snapshot request")
	require.Equal(t, int32(2), requests.Load(), "the shared QueryUsage may separately inspect reset-credit details")
	var cached bool
	cache.openAIProbeCache.Range(func(_, _ any) bool { cached = true; return false })
	require.True(t, cached, "a verified and persisted snapshot must start the cooldown")
}

func TestRefreshOpenAIUsageSingleflightSurvivesLeaderCancellation(t *testing.T) {
	cache := NewUsageCache()
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	svc, shadow, _ := newSparkUsageTestService(t, func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		writeSparkUsageResponse(w)
	}, cache)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, err := svc.refreshOpenAICodexSnapshot(leaderCtx, shadow)
		leaderResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("leader usage probe did not reach the upstream")
	}
	cancelLeader()

	secondResult := make(chan error, 1)
	go func() {
		_, err := svc.refreshOpenAICodexSnapshot(context.Background(), shadow)
		secondResult <- err
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)

	select {
	case err := <-secondResult:
		require.NoError(t, err, "a cancelled leader must not cancel the shared probe")
	case <-time.After(2 * time.Second):
		t.Fatal("second usage probe did not complete")
	}
	select {
	case <-leaderResult:
	case <-time.After(2 * time.Second):
		t.Fatal("leader usage probe did not return")
	}
}

func TestGetOpenAIUsageDoesNotClearRecoverableErrorFromPersistedSnapshotAlone(t *testing.T) {
	cache := NewUsageCache()
	svc, shadow, repo := newSparkUsageTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("fresh persisted usage must not contact the upstream")
		w.WriteHeader(http.StatusInternalServerError)
	}, cache)
	shadow.Status = StatusError
	shadow.ErrorMessage = "token refresh failed: stale account state"
	shadow.Extra = map[string]any{
		"codex_usage_updated_at": time.Now().Format(time.RFC3339),
		"codex_5h_used_percent":  10.0,
		"codex_5h_reset_at":      time.Now().Add(time.Hour).Format(time.RFC3339),
		"codex_7d_used_percent":  20.0,
		"codex_7d_reset_at":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}

	usage, err := svc.GetUsage(context.Background(), shadow.ID)

	require.NoError(t, err)
	require.NotNil(t, usage.FiveHour)
	require.Zero(t, repo.clearCalls.Load(), "stale/local data is not proof that account authentication recovered")
	stored, err := repo.GetByID(context.Background(), shadow.ID)
	require.NoError(t, err)
	require.Equal(t, StatusError, stored.Status)
}

func TestGetOpenAIUsageClearsRecoverableErrorAfterVerifiedProbe(t *testing.T) {
	cache := NewUsageCache()
	svc, shadow, repo := newSparkUsageTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		writeSparkUsageResponse(w)
	}, cache)
	shadow.Status = StatusError
	shadow.ErrorMessage = "token refresh failed: transient"

	usage, err := svc.GetUsage(context.Background(), shadow.ID, true)

	require.NoError(t, err)
	require.NotNil(t, usage.FiveHour)
	require.Equal(t, int32(1), repo.clearCalls.Load())
	stored, err := repo.GetByID(context.Background(), shadow.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, stored.Status)
	require.Empty(t, stored.ErrorMessage)
}
