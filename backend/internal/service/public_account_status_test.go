//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type publicAccountStatusRepoStub struct {
	groups       []PublicStatusGroupRecord
	groupRows    []PublicStatusGroupAccountRecord
	accounts     []*Account
	total        int64
	accountsErr  error
	groupCalls   int
	accountCalls int
}

func (r *publicAccountStatusRepoStub) ListPublicStatusGroups(context.Context) ([]PublicStatusGroupRecord, error) {
	r.groupCalls++
	return r.groups, nil
}

func (r *publicAccountStatusRepoStub) ListPublicStatusGroupAccounts(context.Context, []int64) ([]PublicStatusGroupAccountRecord, error) {
	return r.groupRows, nil
}

func (r *publicAccountStatusRepoStub) ListPublicStatusAccounts(context.Context, int64, int, int) ([]*Account, int64, error) {
	r.accountCalls++
	return r.accounts, r.total, r.accountsErr
}

func TestMaskPublicAccountNameUnicodeAndEmail(t *testing.T) {
	tests := map[string]string{
		"":               "***",
		"A":              "***",
		"AB":             "A***",
		"中文账号":           "中***号",
		"alpha-user":     "al***er",
		"张三@example.com": "张***@example.com",
		"x@example.com":  "***@example.com",
		"ab@example.com": "a***@example.com",
	}
	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, expected, MaskPublicAccountName(input))
		})
	}
}

func TestClassifyPublicAccountStatusPriorityAndRecovery(t *testing.T) {
	now := time.Date(2026, time.July, 25, 4, 0, 0, 0, time.UTC)
	overload := now.Add(time.Minute)
	rateLimit := now.Add(2 * time.Minute)
	temporary := now.Add(3 * time.Minute)
	modelLimit := now.Add(4 * time.Minute)
	expires := now.Add(-time.Minute)
	account := &Account{
		Status:                 StatusError,
		Schedulable:            false,
		AutoPauseOnExpired:     true,
		ExpiresAt:              &expires,
		OverloadUntil:          &overload,
		RateLimitResetAt:       &rateLimit,
		TempUnschedulableUntil: &temporary,
		Extra: map[string]any{modelRateLimitsKey: map[string]any{
			"claude-sonnet": map[string]any{"rate_limit_reset_at": modelLimit.Format(time.RFC3339)},
		}},
	}

	status, recovery := classifyPublicAccountStatus(account, now)
	require.Equal(t, "error", status)
	require.Nil(t, recovery)

	account.Status = "disabled"
	status, _ = classifyPublicAccountStatus(account, now)
	require.Equal(t, "inactive", status)

	account.Status = StatusActive
	status, _ = classifyPublicAccountStatus(account, now)
	require.Equal(t, "expired", status)

	account.AutoPauseOnExpired = false
	status, recovery = classifyPublicAccountStatus(account, now)
	require.Equal(t, "overloaded", status)
	require.Equal(t, overload, *recovery)

	account.OverloadUntil = nil
	status, recovery = classifyPublicAccountStatus(account, now)
	require.Equal(t, "rate_limited", status)
	require.Equal(t, rateLimit, *recovery)

	account.RateLimitResetAt = nil
	status, recovery = classifyPublicAccountStatus(account, now)
	require.Equal(t, "temporarily_unavailable", status)
	require.Equal(t, temporary, *recovery)

	account.TempUnschedulableUntil = nil
	account.Extra["quota_daily_limit"] = 10.0
	account.Extra["quota_daily_used"] = 10.0
	account.Extra["quota_daily_start"] = now.Add(-time.Hour).Format(time.RFC3339)
	quotaReset := now.Add(5 * time.Minute)
	account.Extra["quota_daily_reset_at"] = quotaReset.Format(time.RFC3339)
	status, recovery = classifyPublicAccountStatus(account, now)
	require.Equal(t, "quota_exhausted", status)
	require.Equal(t, quotaReset, *recovery)

	delete(account.Extra, "quota_daily_limit")
	delete(account.Extra, "quota_daily_used")
	delete(account.Extra, "quota_daily_start")
	delete(account.Extra, "quota_daily_reset_at")
	status, _ = classifyPublicAccountStatus(account, now)
	require.Equal(t, "paused", status)

	account.Schedulable = true
	status, recovery = classifyPublicAccountStatus(account, now)
	require.Equal(t, "model_limited", status)
	require.Equal(t, modelLimit, *recovery)

	account.Extra = nil
	status, recovery = classifyPublicAccountStatus(account, now)
	require.Equal(t, "available", status)
	require.Nil(t, recovery)
}

func TestClassifyPublicAccountStatusCreditsAndMultipleQuotaRecovery(t *testing.T) {
	now := time.Date(2026, time.July, 25, 4, 0, 0, 0, time.UTC)
	dailyReset := now.Add(2 * time.Hour)
	weeklyReset := now.Add(24 * time.Hour)
	creditsReset := now.Add(3 * time.Hour)
	account := &Account{
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"quota_daily_limit":     1.0,
			"quota_daily_used":      1.0,
			"quota_daily_start":     now.Add(-time.Hour).Format(time.RFC3339),
			"quota_daily_reset_at":  dailyReset.Format(time.RFC3339),
			"quota_weekly_limit":    2.0,
			"quota_weekly_used":     2.0,
			"quota_weekly_start":    now.Add(-time.Hour).Format(time.RFC3339),
			"quota_weekly_reset_at": weeklyReset.Format(time.RFC3339),
			modelRateLimitsKey: map[string]any{
				"AICredits": map[string]any{"rate_limit_reset_at": creditsReset.Format(time.RFC3339)},
			},
		},
	}

	status, recovery := classifyPublicAccountStatus(account, now)
	require.Equal(t, "quota_exhausted", status)
	require.Equal(t, weeklyReset, *recovery, "recovery is when every exhausted quota can be used again")
}

func TestPublicAccountStatusProjectionDoesNotLeakSensitiveFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	repo := &publicAccountStatusRepoStub{
		accounts: []*Account{{
			ID:                      991,
			Name:                    "private-account@example.com",
			Notes:                   pointerToPublicStatusTest("secret-note"),
			Platform:                PlatformOpenAI,
			Type:                    AccountTypeAPIKey,
			Credentials:             map[string]any{"api_key": "sk-secret-value"},
			Extra:                   map[string]any{"private_extra": "secret-extra-value"},
			ProxyID:                 pointerToPublicStatusTest(int64(13)),
			Priority:                99,
			Status:                  StatusActive,
			ErrorMessage:            "raw-upstream-error",
			TempUnschedulableReason: "internal-reason",
			Schedulable:             true,
			Concurrency:             4,
			UpdatedAt:               now,
		}},
		total: 1,
	}
	statusService := NewPublicAccountStatusService(repo, nil, nil)
	page, _, err := statusService.ListAccounts(context.Background(), 7, 1, 20)
	require.NoError(t, err)
	require.Equal(t, "p***t@example.com", page.Items[0].Name)

	payload, err := json.Marshal(page)
	require.NoError(t, err)
	jsonText := string(payload)
	for _, forbidden := range []string{
		`"id"`, `"account_id"`, `"credentials"`, `"email"`, `"proxy"`, `"notes"`,
		`"extra"`, `"error_message"`, `"temp_unschedulable_reason"`, `"priority"`, `"score"`,
		"private-account@example.com", "sk-secret-value", "secret-note", "secret-extra-value",
		"raw-upstream-error", "internal-reason",
	} {
		require.NotContains(t, jsonText, forbidden)
	}
}

func TestPublicAccountStatusGroupsIncludeDuplicateMembershipAndCache(t *testing.T) {
	account := &Account{Status: StatusActive, Schedulable: true}
	repo := &publicAccountStatusRepoStub{
		groups: []PublicStatusGroupRecord{
			{ID: 1, Name: "One", Platform: PlatformOpenAI, Status: StatusActive},
			{ID: 2, Name: "Two", Platform: PlatformOpenAI, Status: "disabled"},
		},
		groupRows: []PublicStatusGroupAccountRecord{
			{GroupID: 1, Account: account},
			{GroupID: 2, Account: account},
		},
	}
	statusService := NewPublicAccountStatusService(repo, nil, nil)

	first, firstETag, err := statusService.ListGroups(context.Background())
	require.NoError(t, err)
	second, secondETag, err := statusService.ListGroups(context.Background())
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.NotEmpty(t, firstETag)
	require.Equal(t, firstETag, secondETag)
	require.Equal(t, 1, repo.groupCalls)
	require.Equal(t, 1, first[0].StatusSummary.Total)
	require.Equal(t, 1, first[1].StatusSummary.Total)
	require.Equal(t, "disabled", first[1].Status, "inactive groups remain visible when explicitly public")
}

func TestPublicAccountStatusUnpublishedGroupIsNotFound(t *testing.T) {
	repo := &publicAccountStatusRepoStub{accountsErr: ErrPublicAccountStatusGroupNotFound}
	statusService := NewPublicAccountStatusService(repo, nil, nil)
	_, _, err := statusService.ListAccounts(context.Background(), 404, 1, 20)
	require.True(t, errors.Is(err, ErrPublicAccountStatusGroupNotFound))
}

type publicStatusUsageFetcherSpy struct{ calls int }

func (s *publicStatusUsageFetcherSpy) FetchUsage(context.Context, string, string) (*ClaudeUsageResponse, error) {
	s.calls++
	return nil, errors.New("unexpected upstream call")
}

func (s *publicStatusUsageFetcherSpy) FetchUsageWithOptions(context.Context, *ClaudeUsageFetchOptions) (*ClaudeUsageResponse, error) {
	s.calls++
	return nil, errors.New("unexpected upstream call")
}

func TestGetReadOnlyUsageNeverCallsAnthropicUpstream(t *testing.T) {
	spy := &publicStatusUsageFetcherSpy{}
	usageService := &AccountUsageService{usageFetcher: spy}
	account := &Account{
		ID:                  8,
		Platform:            PlatformAnthropic,
		Type:                AccountTypeSetupToken,
		SessionWindowStatus: "active",
		SessionWindowStart:  pointerToPublicStatusTest(time.Now().Add(-time.Hour)),
		SessionWindowEnd:    pointerToPublicStatusTest(time.Now().Add(4 * time.Hour)),
		Extra:               map[string]any{"passive_usage_7d_utilization": 0.25},
	}

	usage, err := usageService.GetReadOnlyUsage(context.Background(), account)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, "passive", usage.Source)
	require.Zero(t, spy.calls)
}

type publicStatusGeminiUsageRepoStub struct {
	UsageLogRepository
	modelStatsCalls int
}

func (s *publicStatusGeminiUsageRepoStub) GetModelStatsWithFilters(
	context.Context,
	time.Time,
	time.Time,
	int64,
	int64,
	int64,
	int64,
	*int16,
	*bool,
	*int8,
) ([]usagestats.ModelStat, error) {
	s.modelStatsCalls++
	return nil, nil
}

func TestGetReadOnlyUsageUsesGeminiQuotaHintsWithoutCredentials(t *testing.T) {
	usageRepo := &publicStatusGeminiUsageRepoStub{}
	usageService := &AccountUsageService{
		usageLogRepo:       usageRepo,
		geminiQuotaService: NewGeminiQuotaService(&config.Config{}, nil),
	}
	account := &Account{
		ID:                     81,
		Platform:               PlatformGemini,
		Type:                   AccountTypeOAuth,
		GeminiOAuthTypeHint:    "google_one",
		GeminiTierIDHint:       "google_ai_pro",
		GeminiProjectIDPresent: true,
	}

	usage, err := usageService.GetReadOnlyUsage(context.Background(), account)

	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, "local", usage.Source)
	require.NotNil(t, usage.GeminiSharedDaily)
	require.EqualValues(t, 1500, usage.GeminiSharedDaily.LimitRequests)
	require.Nil(t, usage.GeminiProDaily)
	require.Equal(t, 2, usageRepo.modelStatsCalls, "only local daily and minute statistics are read")
	require.Nil(t, account.Credentials)
}

func pointerToPublicStatusTest[T any](value T) *T { return &value }
