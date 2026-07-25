package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"golang.org/x/sync/errgroup"
)

const (
	publicAccountStatusCacheTTL   = 15 * time.Second
	publicAccountStatusMaxWorkers = 6
)

var ErrPublicAccountStatusGroupNotFound = errors.New("public account status group not found")

type PublicAccountStatusRepository interface {
	ListPublicStatusGroups(ctx context.Context) ([]PublicStatusGroupRecord, error)
	ListPublicStatusGroupAccounts(ctx context.Context, groupIDs []int64) ([]PublicStatusGroupAccountRecord, error)
	ListPublicStatusAccounts(ctx context.Context, groupID int64, offset, limit int) ([]*Account, int64, error)
}

type PublicStatusGroupRecord struct {
	ID          int64
	Name        string
	Description string
	Platform    string
	Status      string
}

type PublicStatusGroupAccountRecord struct {
	GroupID int64
	Account *Account
}

type PublicAccountStatusSummary struct {
	Total    int            `json:"total"`
	Statuses map[string]int `json:"statuses"`
}

type PublicAccountStatusGroup struct {
	ID            int64                      `json:"id"`
	Name          string                     `json:"name"`
	Description   string                     `json:"description,omitempty"`
	Platform      string                     `json:"platform"`
	Status        string                     `json:"status"`
	StatusSummary PublicAccountStatusSummary `json:"status_summary"`
}

type PublicAPIKeyQuotaWindow struct {
	Limit   float64    `json:"limit"`
	Used    float64    `json:"used"`
	ResetAt *time.Time `json:"reset_at,omitempty"`
}

type PublicAPIKeyQuota struct {
	Total  *PublicAPIKeyQuotaWindow `json:"total,omitempty"`
	Daily  *PublicAPIKeyQuotaWindow `json:"daily,omitempty"`
	Weekly *PublicAPIKeyQuotaWindow `json:"weekly,omitempty"`
}

type PublicGrokUsage struct {
	RequestQuota      *xai.QuotaWindow    `json:"request_quota,omitempty"`
	TokenQuota        *xai.QuotaWindow    `json:"token_quota,omitempty"`
	RetryAfterSeconds *int                `json:"retry_after_seconds,omitempty"`
	EntitlementStatus string              `json:"entitlement_status,omitempty"`
	SnapshotState     string              `json:"snapshot_state,omitempty"`
	LastQuotaProbeAt  string              `json:"last_quota_probe_at,omitempty"`
	LastHeadersSeenAt string              `json:"last_headers_seen_at,omitempty"`
	FreeTokenLimit    int64               `json:"free_token_limit,omitempty"`
	LocalUsage        *WindowStats        `json:"local_usage,omitempty"`
	LocalUsage24h     *WindowStats        `json:"local_usage_24h,omitempty"`
	LocalUsage7d      *WindowStats        `json:"local_usage_7d,omitempty"`
	LocalUsageMonthly *WindowStats        `json:"local_usage_monthly,omitempty"`
	Billing           *xai.BillingSummary `json:"billing,omitempty"`
}

type PublicAccountUsageSnapshot struct {
	Source             string                            `json:"source,omitempty"`
	UpdatedAt          *time.Time                        `json:"updated_at,omitempty"`
	FiveHour           *UsageProgress                    `json:"five_hour,omitempty"`
	SevenDay           *UsageProgress                    `json:"seven_day,omitempty"`
	SevenDaySonnet     *UsageProgress                    `json:"seven_day_sonnet,omitempty"`
	SevenDayFable      *UsageProgress                    `json:"seven_day_fable,omitempty"`
	GeminiSharedDaily  *UsageProgress                    `json:"gemini_shared_daily,omitempty"`
	GeminiProDaily     *UsageProgress                    `json:"gemini_pro_daily,omitempty"`
	GeminiFlashDaily   *UsageProgress                    `json:"gemini_flash_daily,omitempty"`
	GeminiSharedMinute *UsageProgress                    `json:"gemini_shared_minute,omitempty"`
	GeminiProMinute    *UsageProgress                    `json:"gemini_pro_minute,omitempty"`
	GeminiFlashMinute  *UsageProgress                    `json:"gemini_flash_minute,omitempty"`
	APIKeyQuota        *PublicAPIKeyQuota                `json:"api_key_quota,omitempty"`
	ModelQuotas        map[string]*AntigravityModelQuota `json:"model_quotas,omitempty"`
	SubscriptionTier   string                            `json:"subscription_tier,omitempty"`
	AICredits          []AICredit                        `json:"ai_credits,omitempty"`
	Grok               *PublicGrokUsage                  `json:"grok,omitempty"`
}

type PublicAccountStatusAccount struct {
	Name               string                      `json:"name"`
	Platform           string                      `json:"platform"`
	Type               string                      `json:"type"`
	Status             string                      `json:"status"`
	RecoveryAt         *time.Time                  `json:"recovery_at,omitempty"`
	CurrentConcurrency int                         `json:"current_concurrency"`
	MaxConcurrency     int                         `json:"max_concurrency"`
	LastUsedAt         *time.Time                  `json:"last_used_at,omitempty"`
	UpdatedAt          time.Time                   `json:"updated_at"`
	ExpiresAt          *time.Time                  `json:"expires_at,omitempty"`
	Usage              *PublicAccountUsageSnapshot `json:"usage,omitempty"`
}

type PublicAccountStatusPage struct {
	Items    []PublicAccountStatusAccount `json:"items"`
	Total    int64                        `json:"total"`
	Page     int                          `json:"page"`
	PageSize int                          `json:"page_size"`
	Pages    int                          `json:"pages"`
}

type publicAccountStatusCacheEntry struct {
	value     any
	etag      string
	expiresAt time.Time
}

type PublicAccountStatusService struct {
	repo               PublicAccountStatusRepository
	usageService       *AccountUsageService
	concurrencyService *ConcurrencyService

	cacheMu sync.Mutex
	cache   map[string]publicAccountStatusCacheEntry
}

func NewPublicAccountStatusService(
	repo PublicAccountStatusRepository,
	usageService *AccountUsageService,
	concurrencyService *ConcurrencyService,
) *PublicAccountStatusService {
	return &PublicAccountStatusService{
		repo:               repo,
		usageService:       usageService,
		concurrencyService: concurrencyService,
		cache:              make(map[string]publicAccountStatusCacheEntry),
	}
}

func (s *PublicAccountStatusService) ListGroups(ctx context.Context) ([]PublicAccountStatusGroup, string, error) {
	const cacheKey = "groups"
	if value, etag, ok := s.cachedGroups(cacheKey); ok {
		return value, etag, nil
	}

	groups, err := s.repo.ListPublicStatusGroups(ctx)
	if err != nil {
		return nil, "", err
	}
	groupIDs := make([]int64, 0, len(groups))
	for i := range groups {
		groupIDs = append(groupIDs, groups[i].ID)
	}
	accountRows, err := s.repo.ListPublicStatusGroupAccounts(ctx, groupIDs)
	if err != nil {
		return nil, "", err
	}

	now := time.Now()
	summaries := make(map[int64]PublicAccountStatusSummary, len(groups))
	for _, row := range accountRows {
		if row.Account == nil {
			continue
		}
		summary := summaries[row.GroupID]
		if summary.Statuses == nil {
			summary.Statuses = emptyPublicAccountStatusCounts()
		}
		status, _ := classifyPublicAccountStatus(row.Account, now)
		summary.Total++
		summary.Statuses[status]++
		summaries[row.GroupID] = summary
	}

	out := make([]PublicAccountStatusGroup, 0, len(groups))
	for _, group := range groups {
		summary, ok := summaries[group.ID]
		if !ok {
			summary = PublicAccountStatusSummary{Statuses: emptyPublicAccountStatusCounts()}
		}
		out = append(out, PublicAccountStatusGroup{
			ID:            group.ID,
			Name:          group.Name,
			Description:   group.Description,
			Platform:      group.Platform,
			Status:        group.Status,
			StatusSummary: summary,
		})
	}
	etag := s.storeCache(cacheKey, out)
	return out, etag, nil
}

func (s *PublicAccountStatusService) ListAccounts(ctx context.Context, groupID int64, page, pageSize int) (*PublicAccountStatusPage, string, error) {
	cacheKey := "accounts:" + int64String(groupID) + ":" + intString(page) + ":" + intString(pageSize)
	if value, etag, ok := s.cachedPage(cacheKey); ok {
		return value, etag, nil
	}

	offset := (page - 1) * pageSize
	accounts, total, err := s.repo.ListPublicStatusAccounts(ctx, groupID, offset, pageSize)
	if err != nil {
		return nil, "", err
	}
	if accounts == nil {
		return nil, "", ErrPublicAccountStatusGroupNotFound
	}

	accountIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account != nil {
			accountIDs = append(accountIDs, account.ID)
		}
	}
	currentConcurrency := map[int64]int{}
	if s.concurrencyService != nil && len(accountIDs) > 0 {
		if counts, countErr := s.concurrencyService.GetAccountConcurrencyBatch(ctx, accountIDs); countErr == nil {
			currentConcurrency = counts
		}
	}

	now := time.Now()
	out := make([]PublicAccountStatusAccount, len(accounts))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(publicAccountStatusMaxWorkers)
	for i := range accounts {
		i := i
		account := accounts[i]
		group.Go(func() error {
			if account == nil {
				return nil
			}
			var usage *UsageInfo
			if s.usageService != nil {
				usage, _ = s.usageService.GetReadOnlyUsage(groupCtx, account)
			}
			status, recoveryAt := classifyPublicAccountStatusWithUsage(account, usage, now)
			out[i] = PublicAccountStatusAccount{
				Name:               MaskPublicAccountName(account.Name),
				Platform:           account.Platform,
				Type:               account.Type,
				Status:             status,
				RecoveryAt:         recoveryAt,
				CurrentConcurrency: currentConcurrency[account.ID],
				MaxConcurrency:     account.Concurrency,
				LastUsedAt:         account.LastUsedAt,
				UpdatedAt:          account.UpdatedAt,
				ExpiresAt:          account.ExpiresAt,
				Usage:              projectPublicAccountUsage(account, usage),
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, "", err
	}

	pages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if pages < 1 {
		pages = 1
	}
	result := &PublicAccountStatusPage{
		Items:    out,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Pages:    pages,
	}
	etag := s.storeCache(cacheKey, result)
	return result, etag, nil
}

func (s *PublicAccountStatusService) cachedGroups(key string) ([]PublicAccountStatusGroup, string, bool) {
	entry, ok := s.loadCache(key)
	if !ok {
		return nil, "", false
	}
	value, ok := entry.value.([]PublicAccountStatusGroup)
	return value, entry.etag, ok
}

func (s *PublicAccountStatusService) cachedPage(key string) (*PublicAccountStatusPage, string, bool) {
	entry, ok := s.loadCache(key)
	if !ok {
		return nil, "", false
	}
	value, ok := entry.value.(*PublicAccountStatusPage)
	return value, entry.etag, ok
}

func (s *PublicAccountStatusService) loadCache(key string) (publicAccountStatusCacheEntry, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entry, ok := s.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(s.cache, key)
		return publicAccountStatusCacheEntry{}, false
	}
	return entry, true
}

func (s *PublicAccountStatusService) storeCache(key string, value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	s.cacheMu.Lock()
	s.cache[key] = publicAccountStatusCacheEntry{
		value:     value,
		etag:      etag,
		expiresAt: time.Now().Add(publicAccountStatusCacheTTL),
	}
	s.cacheMu.Unlock()
	return etag
}

func emptyPublicAccountStatusCounts() map[string]int {
	return map[string]int{
		"error":                   0,
		"inactive":                0,
		"expired":                 0,
		"overloaded":              0,
		"rate_limited":            0,
		"temporarily_unavailable": 0,
		"quota_exhausted":         0,
		"paused":                  0,
		"model_limited":           0,
		"available":               0,
	}
}

func classifyPublicAccountStatus(account *Account, now time.Time) (string, *time.Time) {
	return classifyPublicAccountStatusWithUsage(account, nil, now)
}

func classifyPublicAccountStatusWithUsage(account *Account, usage *UsageInfo, now time.Time) (string, *time.Time) {
	if account == nil {
		return "inactive", nil
	}
	if account.Status == StatusError {
		return "error", nil
	}
	if account.Status != StatusActive {
		return "inactive", nil
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		return "expired", nil
	}
	if account.OverloadUntil != nil && now.Before(*account.OverloadUntil) {
		return "overloaded", cloneTimePointer(account.OverloadUntil)
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		return "rate_limited", cloneTimePointer(account.RateLimitResetAt)
	}
	if account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) {
		return "temporarily_unavailable", cloneTimePointer(account.TempUnschedulableUntil)
	}
	if recoveryAt, exhausted := publicQuotaRecoveryAt(account, usage, now); exhausted {
		return "quota_exhausted", recoveryAt
	}
	if !account.Schedulable {
		return "paused", nil
	}
	if recoveryAt := publicModelLimitRecoveryAt(account.Extra, now, false); recoveryAt != nil {
		return "model_limited", recoveryAt
	}
	return "available", nil
}

func publicQuotaRecoveryAt(account *Account, usage *UsageInfo, now time.Time) (*time.Time, bool) {
	if account == nil {
		return nil, false
	}
	if limit := account.GetQuotaLimit(); limit > 0 && account.GetQuotaUsed() >= limit {
		return nil, true
	}
	var recovery *time.Time
	exhausted := false
	if limit := account.GetQuotaDailyLimit(); limit > 0 && account.GetQuotaDailyUsed() >= limit && !account.IsDailyQuotaPeriodExpired() {
		exhausted = true
		recovery = laterTime(recovery, publicExtraTime(account, "quota_daily_reset_at"))
	}
	if limit := account.GetQuotaWeeklyLimit(); limit > 0 && account.GetQuotaWeeklyUsed() >= limit && !account.IsWeeklyQuotaPeriodExpired() {
		exhausted = true
		recovery = laterTime(recovery, publicExtraTime(account, "quota_weekly_reset_at"))
	}
	if creditsRecovery := publicModelLimitRecoveryAt(account.Extra, now, true); creditsRecovery != nil {
		exhausted = true
		recovery = laterTime(recovery, creditsRecovery)
	}
	if usageRecovery, usageExhausted := publicUsageQuotaRecoveryAt(account, usage, now); usageExhausted {
		exhausted = true
		recovery = laterTime(recovery, usageRecovery)
	}
	return recovery, exhausted
}

func publicUsageQuotaRecoveryAt(account *Account, usage *UsageInfo, now time.Time) (*time.Time, bool) {
	usage = publicStatusUsageForClassification(account, usage, now)
	if account == nil || usage == nil {
		return nil, false
	}

	var windows []*UsageProgress
	switch account.Platform {
	case PlatformOpenAI, PlatformAnthropic:
		windows = []*UsageProgress{usage.FiveHour, usage.SevenDay}
	case PlatformGemini:
		windows = []*UsageProgress{usage.GeminiSharedDaily, usage.GeminiSharedMinute}
	default:
		return nil, false
	}

	var recovery *time.Time
	exhausted := false
	for _, window := range windows {
		if !publicUsageWindowExhausted(window, now) {
			continue
		}
		exhausted = true
		recovery = laterTime(recovery, window.ResetsAt)
	}
	return recovery, exhausted
}

func publicStatusUsageForClassification(account *Account, usage *UsageInfo, now time.Time) *UsageInfo {
	var snapshot UsageInfo
	if usage != nil {
		snapshot = *usage
	}
	if account == nil {
		return usage
	}

	switch account.Platform {
	case PlatformOpenAI:
		if snapshot.FiveHour == nil {
			snapshot.FiveHour = buildCodexUsageProgressFromExtra(account.Extra, "5h", now)
		}
		if snapshot.SevenDay == nil {
			snapshot.SevenDay = buildCodexUsageProgressFromExtra(account.Extra, "7d", now)
		}
	case PlatformAnthropic:
		if snapshot.FiveHour == nil {
			snapshot.FiveHour = publicAnthropicFiveHourUsage(account, now)
		}
		if snapshot.SevenDay == nil {
			snapshot.SevenDay = buildPassiveUsageWindow(account.Extra, "passive_usage_7d_utilization", "passive_usage_7d_reset")
		}
	}

	if usage == nil && snapshot.FiveHour == nil && snapshot.SevenDay == nil &&
		snapshot.GeminiSharedDaily == nil && snapshot.GeminiSharedMinute == nil {
		return nil
	}
	return &snapshot
}

func publicAnthropicFiveHourUsage(account *Account, now time.Time) *UsageProgress {
	if account == nil || account.SessionWindowEnd == nil || !now.Before(*account.SessionWindowEnd) {
		return nil
	}
	utilization, found := resolveAccountExtraNumber(account.Extra, "session_window_utilization")
	utilization *= 100
	if !found {
		switch account.SessionWindowStatus {
		case "rejected":
			utilization = 100
		case "allowed_warning":
			utilization = 80
		}
	}
	return &UsageProgress{
		Utilization:      utilization,
		ResetsAt:         cloneTimePointer(account.SessionWindowEnd),
		RemainingSeconds: max(0, int(account.SessionWindowEnd.Sub(now).Seconds())),
	}
}

func publicUsageWindowExhausted(window *UsageProgress, now time.Time) bool {
	if window == nil || window.Utilization < 100 {
		return false
	}
	return window.ResetsAt == nil || now.Before(*window.ResetsAt)
}

func publicModelLimitRecoveryAt(extra map[string]any, now time.Time, creditsOnly bool) *time.Time {
	if extra == nil {
		return nil
	}
	rawLimits, ok := extra[modelRateLimitsKey].(map[string]any)
	if !ok {
		return nil
	}
	var recovery *time.Time
	keys := make([]string, 0, len(rawLimits))
	for key := range rawLimits {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		isCredits := strings.EqualFold(strings.TrimSpace(key), "AICredits")
		if creditsOnly != isCredits {
			continue
		}
		entry, ok := rawLimits[key].(map[string]any)
		if !ok {
			continue
		}
		resetRaw, _ := entry["rate_limit_reset_at"].(string)
		resetAt, err := time.Parse(time.RFC3339, strings.TrimSpace(resetRaw))
		if err == nil && now.Before(resetAt) {
			recovery = laterTime(recovery, &resetAt)
		}
	}
	return recovery
}

func projectPublicAccountUsage(account *Account, usage *UsageInfo) *PublicAccountUsageSnapshot {
	projection := &PublicAccountUsageSnapshot{APIKeyQuota: projectPublicAPIKeyQuota(account)}
	if usage != nil {
		projection.Source = usage.Source
		projection.UpdatedAt = usage.UpdatedAt
		projection.FiveHour = usage.FiveHour
		projection.SevenDay = usage.SevenDay
		projection.SevenDaySonnet = usage.SevenDaySonnet
		projection.SevenDayFable = usage.SevenDayFable
		projection.GeminiSharedDaily = usage.GeminiSharedDaily
		projection.GeminiProDaily = usage.GeminiProDaily
		projection.GeminiFlashDaily = usage.GeminiFlashDaily
		projection.GeminiSharedMinute = usage.GeminiSharedMinute
		projection.GeminiProMinute = usage.GeminiProMinute
		projection.GeminiFlashMinute = usage.GeminiFlashMinute
		projection.ModelQuotas = usage.AntigravityQuota
		projection.SubscriptionTier = usage.SubscriptionTier
		projection.AICredits = usage.AICredits
		if usage.GrokRequestQuota != nil || usage.GrokTokenQuota != nil || usage.GrokBilling != nil ||
			usage.GrokLocalUsage != nil || usage.GrokLocalUsage24h != nil || usage.GrokLocalUsage7d != nil ||
			usage.GrokLocalUsageMonthly != nil || usage.GrokQuotaSnapshotState != "" {
			projection.Grok = &PublicGrokUsage{
				RequestQuota:      usage.GrokRequestQuota,
				TokenQuota:        usage.GrokTokenQuota,
				RetryAfterSeconds: usage.GrokRetryAfterSeconds,
				EntitlementStatus: usage.GrokEntitlementStatus,
				SnapshotState:     usage.GrokQuotaSnapshotState,
				LastQuotaProbeAt:  usage.GrokLastQuotaProbeAt,
				LastHeadersSeenAt: usage.GrokLastHeadersSeenAt,
				FreeTokenLimit:    usage.GrokFreeTokenLimit,
				LocalUsage:        usage.GrokLocalUsage,
				LocalUsage24h:     usage.GrokLocalUsage24h,
				LocalUsage7d:      usage.GrokLocalUsage7d,
				LocalUsageMonthly: usage.GrokLocalUsageMonthly,
				Billing:           usage.GrokBilling,
			}
		}
	}
	if projection.APIKeyQuota == nil && usage == nil {
		return nil
	}
	return projection
}

func projectPublicAPIKeyQuota(account *Account) *PublicAPIKeyQuota {
	if account == nil || !account.IsAPIKeyOrBedrock() || !account.HasAnyQuotaLimit() {
		return nil
	}
	quota := &PublicAPIKeyQuota{}
	if limit := account.GetQuotaLimit(); limit > 0 {
		quota.Total = &PublicAPIKeyQuotaWindow{Limit: limit, Used: account.GetQuotaUsed()}
	}
	if limit := account.GetQuotaDailyLimit(); limit > 0 {
		used := account.GetQuotaDailyUsed()
		if account.IsDailyQuotaPeriodExpired() {
			used = 0
		}
		quota.Daily = &PublicAPIKeyQuotaWindow{Limit: limit, Used: used, ResetAt: publicExtraTime(account, "quota_daily_reset_at")}
	}
	if limit := account.GetQuotaWeeklyLimit(); limit > 0 {
		used := account.GetQuotaWeeklyUsed()
		if account.IsWeeklyQuotaPeriodExpired() {
			used = 0
		}
		quota.Weekly = &PublicAPIKeyQuotaWindow{Limit: limit, Used: used, ResetAt: publicExtraTime(account, "quota_weekly_reset_at")}
	}
	return quota
}

func MaskPublicAccountName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "***"
	}
	if at := strings.LastIndex(name, "@"); at > 0 && at < len(name)-1 {
		local := []rune(name[:at])
		domain := name[at:]
		switch len(local) {
		case 0, 1:
			return "***" + domain
		case 2:
			return string(local[0]) + "***" + domain
		default:
			return string(local[0]) + "***" + string(local[len(local)-1]) + domain
		}
	}
	runes := []rune(name)
	switch len(runes) {
	case 0, 1:
		return "***"
	case 2:
		return string(runes[0]) + "***"
	case 3, 4:
		return string(runes[0]) + "***" + string(runes[len(runes)-1])
	default:
		return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
	}
}

func laterTime(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.After(*current) {
		return cloneTimePointer(candidate)
	}
	return current
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func publicExtraTime(account *Account, key string) *time.Time {
	if account == nil {
		return nil
	}
	value := account.getExtraTime(key)
	if value.IsZero() {
		return nil
	}
	return &value
}

func intString(value int) string {
	return int64String(int64(value))
}

func int64String(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
