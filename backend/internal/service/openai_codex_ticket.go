package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	openAICodexTicketExtraKeyPrefix    = "codex_turn_ticket:"
	openAICodexAstraMinVersion         = "0.153.4"
	openAICodexTicketStatePrefix       = "gAAAAA"
	openAICodexTicketTargetLength      = 292
	openAICodexTicketDefaultModel      = "gpt-6-astra"
	openAICodexTicketDefaultSolModel   = "gpt-5.6-sol"
	openAICodexTicketMaxHarvestProxies = 256
	openAICodexTicketMinProbeInterval  = 180 * time.Second
)

// OpenAICodexTicketMaxHarvestProxyEntries bounds the persisted pool size.
const OpenAICodexTicketMaxHarvestProxyEntries = openAICodexTicketMaxHarvestProxies

// OpenAICodexTicketMinProbeIntervalSeconds is the enforced global cycle floor.
const OpenAICodexTicketMinProbeIntervalSeconds = int(openAICodexTicketMinProbeInterval / time.Second)

// OpenAICodexTicketTargetLength is the exact accepted state length.
const OpenAICodexTicketTargetLength = openAICodexTicketTargetLength

// OpenAICodexTicketHarvestFailureCategory is deliberately a small, stable
// vocabulary.  It is safe to expose in operations output and keeps proxy
// credentials and transport error strings out of logs.
type OpenAICodexTicketHarvestFailureCategory string

const (
	OpenAICodexTicketHarvestFailureNone       OpenAICodexTicketHarvestFailureCategory = ""
	OpenAICodexTicketHarvestFailureTransport  OpenAICodexTicketHarvestFailureCategory = "transport"
	OpenAICodexTicketHarvestFailureHTTP312    OpenAICodexTicketHarvestFailureCategory = "http_312"
	OpenAICodexTicketHarvestFailureLength312  OpenAICodexTicketHarvestFailureCategory = "length_312"
	OpenAICodexTicketHarvestFailureState312   OpenAICodexTicketHarvestFailureCategory = OpenAICodexTicketHarvestFailureLength312
	OpenAICodexTicketHarvestFailureHTTPStatus OpenAICodexTicketHarvestFailureCategory = "http_status"
	OpenAICodexTicketHarvestFailureIncomplete OpenAICodexTicketHarvestFailureCategory = "incomplete"
	OpenAICodexTicketHarvestFailureState      OpenAICodexTicketHarvestFailureCategory = "invalid_state"
)

// OpenAICodexTicketHarvestProxyStatus is the redacted runtime state of one
// pool entry. URL/userinfo is intentionally absent; EntryID is a stable hash
// suitable for logs and dashboards.
type OpenAICodexTicketHarvestProxyStatus struct {
	Index               int                                     `json:"index"`
	EntryID             string                                  `json:"entry_id"`
	Available           bool                                    `json:"available"`
	CooldownUntil       *time.Time                              `json:"cooldown_until,omitempty"`
	FailureCount        int                                     `json:"failure_count"`
	LastHTTPStatus      int                                     `json:"last_http_status"`
	LastStateLength     int                                     `json:"last_state_length"`
	LastFailureCategory OpenAICodexTicketHarvestFailureCategory `json:"last_failure_category,omitempty"`
	LastResultAt        *time.Time                              `json:"last_result_at,omitempty"`
}

// OpenAICodexTicketHarvestPoolStatus summarizes the currently configured
// harvest-only pool. It is a snapshot and can safely be serialized by an
// operational endpoint without exposing proxy credentials.
type OpenAICodexTicketHarvestPoolStatus struct {
	PoolSize                int                                   `json:"pool_size"`
	AvailableCount          int                                   `json:"available_count"`
	CoolingDownCount        int                                   `json:"cooling_down_count"`
	MinimumProbeIntervalSec int                                   `json:"minimum_probe_interval_seconds"`
	Entries                 []OpenAICodexTicketHarvestProxyStatus `json:"entries"`
}

// OpenAICodexTicketHarvestCompletion describes the stop condition for a
// bounded operator fill run. The resident harvester remains alive so tickets
// can be refreshed before expiry; callers may stop their one-shot monitor when
// Complete becomes true.
type OpenAICodexTicketHarvestCompletion struct {
	EligibleAccountCount int  `json:"eligible_account_count"`
	EligiblePairCount    int  `json:"eligible_pair_count"`
	ReadyPairCount       int  `json:"ready_pair_count"`
	SkippedAccountCount  int  `json:"skipped_account_count"`
	Complete             bool `json:"complete"`
}

// ErrOpenAICodexTicketUnavailable 表示该号该模型没有可用的 292 门票，
// 且 fail_closed 禁止裸打业务请求。
var ErrOpenAICodexTicketUnavailable = errors.New("codex turn-state ticket unavailable")

type openAICodexTicket struct {
	AccountID  int64     `json:"account_id"`
	Model      string    `json:"model"`
	State      string    `json:"state"`
	Length     int       `json:"length"`
	CapturedAt time.Time `json:"captured_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Attempts   int       `json:"attempts"`
}

func openAICodexTicketKey(accountID int64, model string) string {
	return fmt.Sprintf("%d\x00%s", accountID, strings.TrimSpace(model))
}

func openAICodexTicketExtraKey(model string) string {
	return openAICodexTicketExtraKeyPrefix + strings.TrimSpace(model)
}

func normalizeOpenAICodexTicketModel(model string) string {
	return strings.TrimSpace(model)
}

// SplitOpenAICodexTicketHarvestProxyURLs accepts the legacy single URL and a
// newline/semicolon separated pool. A JSON string array is also accepted so
// deployment config can keep credentials out of shell argument parsing.
// Entries are validated and de-duplicated in input order.
func SplitOpenAICodexTicketHarvestProxyURLs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var entries []string
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &entries); err != nil || entries == nil {
			return nil, errors.New("harvest proxy pool must be a JSON string array")
		}
	} else {
		// Newline is the documented form. Semicolons and commas are accepted
		// for deployment environment variables where literal newlines are
		// inconvenient; proxy URLs cannot contain an unescaped comma in their
		// authority portion, so this remains unambiguous for valid entries.
		entries = strings.FieldsFunc(raw, func(r rune) bool {
			return r == '\n' || r == '\r' || r == ';' || r == ','
		})
	}
	seen := make(map[string]struct{}, len(entries))
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if err := ValidateOpenAICodexTicketHarvestProxyURL(entry); err != nil {
			return nil, fmt.Errorf("harvest proxy %d: %w", len(result)+1, err)
		}
		canonical := canonicalOpenAICodexTicketHarvestProxyURL(entry)
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, entry)
		if len(result) > openAICodexTicketMaxHarvestProxies {
			return nil, fmt.Errorf("harvest proxy pool exceeds %d entries", openAICodexTicketMaxHarvestProxies)
		}
	}
	return result, nil
}

// canonicalOpenAICodexTicketHarvestProxyURL is used only for de-duplication
// and state keys. It never returns a value to logs or API responses.
func canonicalOpenAICodexTicketHarvestProxyURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if host := parsed.Hostname(); host != "" {
		// url.URL.Host preserves userinfo-free authority while normalizing host
		// case. Keep an explicitly supplied port stable for legacy URLs.
		host = strings.ToLower(host)
		port := parsed.Port()
		if port == "" {
			switch parsed.Scheme {
			case "http":
				port = "80"
			case "https":
				port = "443"
			case "socks5", "socks5h":
				port = "1080"
			}
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if port != "" {
			parsed.Host = host + ":" + port
		} else {
			parsed.Host = host
		}
	}
	return parsed.String()
}

// OpenAICodexTicketHarvestProxyID returns a stable, redacted identifier for a
// pool entry. Passwords are excluded so rotating credentials does not change
// the operational identity or leak secrets through a hash input in logs.
func OpenAICodexTicketHarvestProxyID(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	canonical := strings.TrimSpace(raw)
	if err == nil {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		if parsed.User != nil {
			parsed.User = url.User(parsed.User.Username())
		}
		canonical = canonicalOpenAICodexTicketHarvestProxyURL(parsed.String())
	}
	sum := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("p-%x", sum[:8])
}

// FirstOpenAICodexTicketHarvestProxyURL returns the primary entry used for
// legacy business-proxy synchronization. Harvesting itself may use all entries.
func FirstOpenAICodexTicketHarvestProxyURL(raw string) (string, error) {
	entries, err := SplitOpenAICodexTicketHarvestProxyURLs(raw)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	return entries[0], nil
}

// OpenAICodexTicketHarvestProxyPoolSize returns the validated number of
// distinct entries. Invalid input is reported as size zero so callers that
// already perform validation can use it as a cheap mode check.
func OpenAICodexTicketHarvestProxyPoolSize(raw string) int {
	entries, err := SplitOpenAICodexTicketHarvestProxyURLs(raw)
	if err != nil {
		return 0
	}
	return len(entries)
}

// OpenAICodexTicketHarvestOnlyMode is true for a real multi-entry pool. A
// single URL intentionally keeps the historical business-proxy linkage.
func OpenAICodexTicketHarvestOnlyMode(raw string) bool {
	return OpenAICodexTicketHarvestProxyPoolSize(raw) > 1
}

func extractOpenAICodexTicketModel(body []byte) string {
	return normalizeOpenAICodexTicketModel(gjson.GetBytes(body, "model").String())
}

func (s *OpenAIGatewayService) openAICodexTicketConfig() config.OpenAICodexTicketConfig {
	cfg := config.OpenAICodexTicketConfig{}
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	// 292 is an interoperability contract, not a tunable response shape.
	// Keep the legacy field for config-file compatibility but normalize every
	// non-292 value to the only value accepted by the harvester and gate.
	if cfg.TargetLength != openAICodexTicketTargetLength {
		cfg.TargetLength = openAICodexTicketTargetLength
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 3600
	}
	if cfg.RefreshBeforeSeconds <= 0 {
		cfg.RefreshBeforeSeconds = 600
	}
	if time.Duration(cfg.HarvestProbeIntervalSeconds)*time.Second < openAICodexTicketMinProbeInterval {
		cfg.HarvestProbeIntervalSeconds = int(openAICodexTicketMinProbeInterval / time.Second)
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = 25
	}
	if len(cfg.Models) == 0 {
		cfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	return cfg
}

func (s *OpenAIGatewayService) openAICodexTicketGatedModel(model string) bool {
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketEnabled() {
		return false
	}
	for _, item := range s.openAICodexTicketConfig().Models {
		if normalizeOpenAICodexTicketModel(item) == model {
			return true
		}
	}
	return false
}

// OpenAICodexTicketStatus 是给管理端看的门票摘要，不含 state blob。
type OpenAICodexTicketStatus struct {
	Model            string     `json:"model"`
	Length           int        `json:"length,omitempty"`
	Ready            bool       `json:"ready"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	Blocked          bool       `json:"blocked"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

func OpenAICodexTicketStatuses(account *Account, cfg config.OpenAICodexTicketConfig, now time.Time) []OpenAICodexTicketStatus {
	if !cfg.Enabled || !isOpenAICodexTicketAccount(account) {
		return nil
	}
	models, targetLen := OpenAICodexTicketModelsForAccount(account, cfg), cfg.TargetLength
	if OpenAICodexTicketHarvestOnlyMode(cfg.HarvestProxyURL) {
		filtered := make([]string, 0, len(models))
		for _, model := range models {
			if !isOpenAICodexTicketSolModel(model) {
				filtered = append(filtered, model)
			}
		}
		models = filtered
	}
	if len(models) == 0 {
		return nil
	}
	targetLen = openAICodexTicketTargetLength
	out := make([]OpenAICodexTicketStatus, 0, len(models))
	for _, model := range models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" {
			continue
		}
		status := OpenAICodexTicketStatus{Model: model}
		ticket := parseOpenAICodexTicketFromAny(0, model, nil)
		if account != nil && account.Extra != nil {
			ticket = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
		}
		if ticket.valid(now, targetLen) {
			status.Ready = true
			status.Length = ticket.Length
			remaining := int64(ticket.ExpiresAt.Sub(now) / time.Second)
			if remaining < 0 {
				remaining = 0
			}
			status.RemainingSeconds = remaining
			exp := ticket.ExpiresAt
			status.ExpiresAt = &exp
		}
		status.Blocked = cfg.FailClosed && !status.Ready
		out = append(out, status)
	}
	return out
}

func (s *OpenAIGatewayService) openAICodexTicketEnabled() bool {
	return s.openAICodexTicketEnabledContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	fallback := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.Enabled
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketEnabled(ctx, fallback)
	}
	return fallback
}

// OpenAICodexTicketAccountAllowed applies the optional account allowlist.
// An empty allowlist preserves the historical global scope.
func OpenAICodexTicketAccountAllowed(account *Account, accountIDs []int64) bool {
	if account == nil || account.ID <= 0 {
		return false
	}
	if len(accountIDs) == 0 {
		return true
	}
	for _, id := range accountIDs {
		if id == account.ID {
			return true
		}
	}
	return false
}

// OpenAICodexTicketModelsForAccount resolves the global model set through an
// optional account-specific override. An explicit empty override disables all
// ticket handling for that account while leaving its ordinary requests intact.
func OpenAICodexTicketModelsForAccount(account *Account, cfg config.OpenAICodexTicketConfig) []string {
	models := cfg.Models
	if len(models) == 0 {
		models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	if account == nil || account.ID <= 0 {
		return nil
	}
	scoped, exists := cfg.AccountModels[strconv.FormatInt(account.ID, 10)]
	if !exists {
		return append([]string(nil), models...)
	}
	allowed := make(map[string]struct{}, len(scoped))
	for _, model := range scoped {
		if model = normalizeOpenAICodexTicketModel(model); model != "" {
			allowed[model] = struct{}{}
		}
	}
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = normalizeOpenAICodexTicketModel(model)
		if _, ok := allowed[model]; ok {
			result = append(result, model)
		}
	}
	return result
}

func (s *OpenAIGatewayService) openAICodexTicketAccountModelAllowed(ctx context.Context, account *Account, model string) bool {
	if s == nil || account == nil {
		return false
	}
	cfg := s.openAICodexTicketConfig()
	if s.settingService != nil {
		cfg.AccountModels = s.settingService.GetOpenAICodexTicketAccountModels(ctx)
	}
	model = normalizeOpenAICodexTicketModel(model)
	if !s.openAICodexTicketHarvestModelAllowed(ctx, model) {
		return false
	}
	for _, allowed := range OpenAICodexTicketModelsForAccount(account, cfg) {
		if normalizeOpenAICodexTicketModel(allowed) == model {
			return true
		}
	}
	return false
}

// Multi-entry pools are harvest-only. Sol traffic remains on the ordinary
// business path even when an older global model list still mentions Sol.
// Single-entry configurations retain the historical model behavior.
func (s *OpenAIGatewayService) openAICodexTicketHarvestModelAllowed(ctx context.Context, model string) bool {
	if s == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !OpenAICodexTicketHarvestOnlyMode(s.openAICodexTicketHarvestProxyURLContext(ctx)) {
		return true
	}
	return !isOpenAICodexTicketSolModel(model)
}

func isOpenAICodexTicketSolModel(model string) bool {
	normalized := strings.ToLower(normalizeOpenAICodexTicketModel(model))
	return normalized == strings.ToLower(openAICodexTicketDefaultSolModel) || strings.HasSuffix(normalized, "-sol")
}

func (s *OpenAIGatewayService) openAICodexTicketAccountAllowed(ctx context.Context, account *Account) bool {
	if s == nil || account == nil {
		return false
	}
	ids := append([]int64(nil), s.openAICodexTicketConfig().AccountIDs...)
	if s.settingService != nil {
		ids = s.settingService.GetOpenAICodexTicketAccountIDs(ctx)
	}
	return OpenAICodexTicketAccountAllowed(account, ids)
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURL() string {
	return s.openAICodexTicketHarvestProxyURLContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURLContext(ctx context.Context) string {
	if s.settingService != nil {
		if proxy := s.settingService.GetOpenAICodexTicketHarvestProxyURL(ctx); proxy != "" {
			return proxy
		}
	}
	return strings.TrimSpace(s.openAICodexTicketConfig().HarvestProxyURL)
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURLsContext(ctx context.Context) []string {
	raw := s.openAICodexTicketHarvestProxyURLContext(ctx)
	entries, err := SplitOpenAICodexTicketHarvestProxyURLs(raw)
	if err != nil {
		logger.L().Warn("openai_codex_ticket harvest proxy pool rejected", zap.String("reason", "invalid_pool"))
		return nil
	}
	return entries
}

type openAICodexTicketHarvestProxyRuntime struct {
	failureCount        int
	cooldownUntil       time.Time
	lastHTTPStatus      int
	lastStateLength     int
	lastFailureCategory OpenAICodexTicketHarvestFailureCategory
	lastResultAt        time.Time
}

func (s *OpenAIGatewayService) ensureOpenAICodexTicketProxyRuntimeLocked(entries []string) {
	if s.openaiCodexTicketProxyCooldown == nil {
		s.openaiCodexTicketProxyCooldown = make(map[string]time.Time)
	}
	if s.openaiCodexTicketProxyStates == nil {
		s.openaiCodexTicketProxyStates = make(map[string]*openAICodexTicketHarvestProxyRuntime)
	}
	for _, entry := range entries {
		key := canonicalOpenAICodexTicketHarvestProxyURL(entry)
		if s.openaiCodexTicketProxyStates[key] == nil {
			s.openaiCodexTicketProxyStates[key] = &openAICodexTicketHarvestProxyRuntime{}
		}
	}
}

// nextOpenAICodexTicketHarvestProxy reserves one distinct pool entry for the
// next probe. Failed entries cool down so a bad node does not consume every
// account's attempt in the same cycle.
func (s *OpenAIGatewayService) nextOpenAICodexTicketHarvestProxy(ctx context.Context) string {
	entries := s.openAICodexTicketHarvestProxyURLsContext(ctx)
	if len(entries) == 0 {
		return ""
	}
	now := time.Now()
	s.openaiCodexTicketProxyMu.Lock()
	defer s.openaiCodexTicketProxyMu.Unlock()
	s.ensureOpenAICodexTicketProxyRuntimeLocked(entries)
	// Preserve legacy single-endpoint behavior. Pool cooldowns are useful only
	// when another route is available; a lone endpoint must remain retryable.
	if len(entries) == 1 {
		return entries[0]
	}
	for i := 0; i < len(entries); i++ {
		idx := int(s.openaiCodexTicketProxyCursor % uint64(len(entries)))
		s.openaiCodexTicketProxyCursor++
		entry := entries[idx]
		key := canonicalOpenAICodexTicketHarvestProxyURL(entry)
		runtime := s.openaiCodexTicketProxyStates[key]
		legacyUntil := s.openaiCodexTicketProxyCooldown[key]
		if legacyUntil.IsZero() {
			legacyUntil = s.openaiCodexTicketProxyCooldown[entry]
		}
		if (runtime != nil && now.Before(runtime.cooldownUntil)) || (!legacyUntil.IsZero() && now.Before(legacyUntil)) {
			continue
		}
		return entry
	}
	return ""
}

func (s *OpenAIGatewayService) recordOpenAICodexTicketHarvestProxy(proxyURL string, success bool) {
	category := OpenAICodexTicketHarvestFailureTransport
	if success {
		category = OpenAICodexTicketHarvestFailureNone
	}
	s.recordOpenAICodexTicketHarvestProxyResult(proxyURL, category, 0, 0)
}

// recordOpenAICodexTicketHarvestProxyResult updates the per-entry circuit
// state. Only the category/status/length are retained; the underlying error
// and URL are deliberately discarded.
func (s *OpenAIGatewayService) recordOpenAICodexTicketHarvestProxyResult(proxyURL string, category OpenAICodexTicketHarvestFailureCategory, status, stateLength int) {
	if strings.TrimSpace(proxyURL) == "" {
		return
	}
	entries, _ := SplitOpenAICodexTicketHarvestProxyURLs(s.openAICodexTicketHarvestProxyURL())
	if len(entries) == 0 {
		return
	}
	key := canonicalOpenAICodexTicketHarvestProxyURL(proxyURL)
	now := time.Now()
	s.openaiCodexTicketProxyMu.Lock()
	defer s.openaiCodexTicketProxyMu.Unlock()
	s.ensureOpenAICodexTicketProxyRuntimeLocked(entries)
	runtime := s.openaiCodexTicketProxyStates[key]
	if runtime == nil {
		return
	}
	runtime.lastHTTPStatus = status
	runtime.lastStateLength = stateLength
	runtime.lastResultAt = now
	if category == OpenAICodexTicketHarvestFailureNone {
		runtime.failureCount = 0
		runtime.cooldownUntil = time.Time{}
		runtime.lastFailureCategory = OpenAICodexTicketHarvestFailureNone
		delete(s.openaiCodexTicketProxyCooldown, key)
		return
	}
	runtime.failureCount++
	runtime.lastFailureCategory = category
	// A failed entry is never retried more frequently than the global minimum.
	cooldown := time.Duration(s.openAICodexTicketConfig().HarvestProbeIntervalSeconds) * time.Second
	if cooldown < openAICodexTicketMinProbeInterval {
		cooldown = openAICodexTicketMinProbeInterval
	}
	runtime.cooldownUntil = now.Add(cooldown)
	s.openaiCodexTicketProxyCooldown[key] = runtime.cooldownUntil
}

// OpenAICodexTicketHarvestPoolStatus returns a redacted operational snapshot.
func (s *OpenAIGatewayService) OpenAICodexTicketHarvestPoolStatus(ctx context.Context) OpenAICodexTicketHarvestPoolStatus {
	if s == nil {
		return OpenAICodexTicketHarvestPoolStatus{
			MinimumProbeIntervalSec: int(openAICodexTicketMinProbeInterval / time.Second),
			Entries:                 []OpenAICodexTicketHarvestProxyStatus{},
		}
	}
	entries := s.openAICodexTicketHarvestProxyURLsContext(ctx)
	status := OpenAICodexTicketHarvestPoolStatus{
		PoolSize:                len(entries),
		MinimumProbeIntervalSec: int(openAICodexTicketMinProbeInterval / time.Second),
		Entries:                 make([]OpenAICodexTicketHarvestProxyStatus, 0, len(entries)),
	}
	now := time.Now()
	s.openaiCodexTicketProxyMu.Lock()
	defer s.openaiCodexTicketProxyMu.Unlock()
	s.ensureOpenAICodexTicketProxyRuntimeLocked(entries)
	for index, entry := range entries {
		key := canonicalOpenAICodexTicketHarvestProxyURL(entry)
		runtime := s.openaiCodexTicketProxyStates[key]
		item := OpenAICodexTicketHarvestProxyStatus{
			Index:     index,
			EntryID:   OpenAICodexTicketHarvestProxyID(entry),
			Available: true,
		}
		if runtime != nil {
			item.FailureCount = runtime.failureCount
			item.LastHTTPStatus = runtime.lastHTTPStatus
			item.LastStateLength = runtime.lastStateLength
			item.LastFailureCategory = runtime.lastFailureCategory
			if !runtime.lastResultAt.IsZero() {
				value := runtime.lastResultAt
				item.LastResultAt = &value
			}
			cooldownUntil := runtime.cooldownUntil
			if cooldownUntil.IsZero() {
				cooldownUntil = s.openaiCodexTicketProxyCooldown[key]
			}
			if cooldownUntil.IsZero() {
				cooldownUntil = s.openaiCodexTicketProxyCooldown[entry]
			}
			if len(entries) > 1 && now.Before(cooldownUntil) {
				value := cooldownUntil
				item.CooldownUntil = &value
				item.Available = false
			}
		}
		if item.Available {
			status.AvailableCount++
		} else {
			status.CoolingDownCount++
		}
		status.Entries = append(status.Entries, item)
	}
	return status
}

// OpenAICodexTicketHarvestCompletion evaluates the configured account/model
// scope using persisted or in-memory tickets. It intentionally treats only
// active, non-shadow OAuth accounts as eligible, matching the harvester.
func (s *OpenAIGatewayService) OpenAICodexTicketHarvestCompletion(ctx context.Context) OpenAICodexTicketHarvestCompletion {
	result := OpenAICodexTicketHarvestCompletion{}
	if s == nil || s.accountRepo == nil {
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var accounts []Account
	var err error
	if lister, ok := s.accountRepo.(codexTicketAccountStatusLister); ok {
		accounts, err = lister.ListByPlatformAllStatuses(ctx, PlatformOpenAI)
	} else {
		accounts, err = s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	}
	if err != nil {
		return result
	}
	cfg := s.openAICodexTicketConfig()
	accountIDs := append([]int64(nil), cfg.AccountIDs...)
	accountModels := cfg.AccountModels
	if s.settingService != nil {
		accountIDs = s.settingService.GetOpenAICodexTicketAccountIDs(ctx)
		accountModels = s.settingService.GetOpenAICodexTicketAccountModels(ctx)
	}
	cfg.AccountModels = accountModels
	now := time.Now()
	for index := range accounts {
		account := &accounts[index]
		if account.Status != StatusActive || !isOpenAICodexTicketAccount(account) || account.IsShadow() || (account.ExpiresAt != nil && !account.ExpiresAt.After(now)) || !OpenAICodexTicketAccountAllowed(account, accountIDs) {
			result.SkippedAccountCount++
			continue
		}
		result.EligibleAccountCount++
		for _, model := range OpenAICodexTicketModelsForAccount(account, cfg) {
			model = normalizeOpenAICodexTicketModel(model)
			if model == "" || isOpenAICodexTicketSolModel(model) {
				continue
			}
			result.EligiblePairCount++
			if ticket := lookupPersistedOpenAICodexTicket(account, model); ticket.valid(now, openAICodexTicketTargetLength) {
				result.ReadyPairCount++
			}
		}
	}
	result.Complete = result.EligiblePairCount > 0 && result.ReadyPairCount == result.EligiblePairCount
	return result
}

func (t *openAICodexTicket) valid(now time.Time, targetLen int) bool {
	if t == nil {
		return false
	}
	state := strings.TrimSpace(t.State)
	if !openAICodexTicketStateLengthAccepted(state, targetLen) || t.Length != targetLen {
		return false
	}
	if t.ExpiresAt.IsZero() || !now.Before(t.ExpiresAt) {
		return false
	}
	return true
}

// openAICodexTicketStateLengthAccepted enforces the configured exact length.
// The ticket is a signed opaque value; accepting a different-sized token would
// change the contract and allow an account without a valid 292 ticket through.
func openAICodexTicketStateLengthAccepted(state string, targetLen int) bool {
	state = strings.TrimSpace(state)
	if state == "" || !strings.HasPrefix(state, openAICodexTicketStatePrefix) {
		return false
	}
	return targetLen == openAICodexTicketTargetLength && len(state) == openAICodexTicketTargetLength
}

func (t *openAICodexTicket) needsRefresh(now time.Time, refreshBefore time.Duration) bool {
	if t == nil || t.ExpiresAt.IsZero() {
		return true
	}
	return !t.ExpiresAt.After(now.Add(refreshBefore))
}

func (s *OpenAIGatewayService) lookupOpenAICodexTicket(account *Account, model string) *openAICodexTicket {
	if s == nil || account == nil || account.ID <= 0 {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" {
		return nil
	}
	key := openAICodexTicketKey(account.ID, model)
	targetLen := openAICodexTicketTargetLength
	if s != nil {
		targetLen = s.openAICodexTicketConfig().TargetLength
	}
	now := time.Now()
	var mem *openAICodexTicket
	if raw, ok := s.openaiCodexTickets.Load(key); ok {
		mem, _ = raw.(*openAICodexTicket)
	}
	var extra *openAICodexTicket
	if account.Extra != nil {
		extra = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
	}
	if extra.valid(now, targetLen) && (mem == nil || extra.CapturedAt.After(mem.CapturedAt)) {
		s.openaiCodexTickets.Store(key, extra)
		return extra
	}
	if mem.valid(now, targetLen) {
		return mem
	}
	if extra != nil {
		s.openaiCodexTickets.Store(key, extra)
		return extra
	}
	if mem != nil {
		s.openaiCodexTickets.Delete(key)
	}
	return nil
}

func lookupPersistedOpenAICodexTicket(account *Account, model string) *openAICodexTicket {
	if account == nil || account.Extra == nil {
		return nil
	}
	return parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
}

func parseOpenAICodexTicketFromAny(accountID int64, model string, raw any) *openAICodexTicket {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ticket openAICodexTicket
	if err := json.Unmarshal(b, &ticket); err != nil {
		return nil
	}
	ticket.AccountID = accountID
	if strings.TrimSpace(model) != "" {
		ticket.Model = model
	}
	ticket.State = strings.TrimSpace(ticket.State)
	if ticket.Length == 0 {
		ticket.Length = len(ticket.State)
	}
	if ticket.State == "" {
		return nil
	}
	return &ticket
}

func (s *OpenAIGatewayService) storeOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) {
	if err := s.persistOpenAICodexTicket(ctx, account, ticket); err != nil {
		accountID := int64(0)
		model := ""
		if account != nil {
			accountID = account.ID
		}
		if ticket != nil {
			model = ticket.Model
		}
		logger.L().Warn("openai_codex_ticket persist failed",
			zap.Int64("account_id", accountID),
			zap.String("model", model),
			zap.String("reason", "persistence"),
		)
	}
}

func (s *OpenAIGatewayService) persistOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) error {
	if s == nil || account == nil || ticket == nil || account.ID <= 0 {
		return errors.New("invalid ticket persistence input")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	model := normalizeOpenAICodexTicketModel(ticket.Model)
	ticket.Model = model
	ticket.AccountID = account.ID
	s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, model), ticket)
	if s.accountRepo == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		openAICodexTicketExtraKey(model): ticket,
	}); err != nil {
		s.openaiCodexTickets.Delete(openAICodexTicketKey(account.ID, model))
		return err
	}
	return nil
}

// applyOpenAICodexTicket 在出站请求上覆盖 x-codex-turn-state。
// 请求路径只注入已捕获的有效门票，不现场打票；无票则返回
// ErrOpenAICodexTicketUnavailable。打票由后台 harvester 完成。
func (s *OpenAIGatewayService) applyOpenAICodexTicket(ctx context.Context, account *Account, model string, h http.Header) error {
	if s == nil || h == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledContext(ctx) {
		return nil
	}
	if !s.openAICodexTicketAccountAllowed(ctx, account) {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketAccountModelAllowed(ctx, account, model) || !s.openAICodexTicketGatedModel(model) {
		return nil
	}
	cfg := s.openAICodexTicketConfig()
	ticket := s.lookupOpenAICodexTicket(account, model)
	if ticket.valid(time.Now(), cfg.TargetLength) {
		h.Set(openAICodexTurnStateHeader, ticket.State)
		return nil
	}
	if !cfg.FailClosed {
		return nil
	}
	return ErrOpenAICodexTicketUnavailable
}

// openAICodexTicketOutboundModel 预测本请求真正出站的模型名，也就是
// applyOpenAICodexTicket 注入时读到的 body.model。
//
// 调度门控与注入必须按同一个模型名判定门票。普通请求下二者同源：Forward 的
// upstreamModel 与本函数都走 resolveOpenAIAccountUpstreamModelForRequest，且
// Forward 会把 body.model 改写成该值后才注入。但 /responses/compact 例外——
// Forward 会把出站模型进一步改写为 compact 映射或 gateway.openai_compact_model
// （默认非空），此时若门控仍按客户端原始模型判定，就会把「实际出站是非门控
// 模型、根本不需要票」的 compact 请求整片误拦成不可调度。
func (s *OpenAIGatewayService) openAICodexTicketOutboundModel(account *Account, requestedModel string, requireCompact bool) string {
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if !account.IsOpenAI() {
		return canonicalOpenAIAccountSchedulingModel(account, model)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, requireCompact)
	if requireCompact {
		// 与 Forward 同序：compact 兜底模型优先于普通/compact 映射结果。
		if compactModel := strings.TrimSpace(s.resolveOpenAICompactFallbackModel(account, model)); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return model
}

// outboundModel 必须是真正会发给上游的模型名（openAICodexTicketOutboundModel），
// 不是客户端原始模型：注入侧读的是出站 body.model，两侧口径必须一致。
func (s *OpenAIGatewayService) openAICodexTicketBlocksAccount(account *Account, outboundModel string) bool {
	if s == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabled() {
		return false
	}
	if !s.openAICodexTicketAccountAllowed(context.Background(), account) {
		return false
	}
	cfg := s.openAICodexTicketConfig()
	if !cfg.FailClosed {
		return false
	}
	model := normalizeOpenAICodexTicketModel(outboundModel)
	if !s.openAICodexTicketAccountModelAllowed(context.Background(), account, model) || !s.openAICodexTicketGatedModel(model) {
		return false
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	return !ticket.valid(time.Now(), cfg.TargetLength)
}

func (s *OpenAIGatewayService) fireOpenAICodexTicketProbe(ctx context.Context, account *Account, token, model, proxyURL string, attemptTimeout time.Duration) (state string, status int, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()

	body := []byte(`{"model":` + jsonString(model) + `,"store":false,"stream":true,"instructions":"Reply with exactly: pong","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest))
	req.Close = true
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", uuid.NewString())
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(attemptCtx, s.accountRepo, req.Header, account); err != nil {
		return "", 0, err
	}
	applyOpenAICodexTicketHarvestIdentity(req.Header, model)

	// Synthetic probes must use the dedicated no-reuse transport even when the
	// production account is bound to a plugin. This also avoids reading pluginManager
	// while handlers are still wiring it during gateway construction.
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return "", 0, err
	}
	if resp == nil {
		return "", 0, errors.New("nil upstream response")
	}
	// Only the response header is needed; no connection will be reused.
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	return extractOpenAICodexTurnState(resp.Header), resp.StatusCode, nil
}

func jsonString(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func applyOpenAICodexTicketHarvestIdentity(h http.Header, model string) {
	ensureCodexIdentityHeaders(h)
	enforceCodexIdentityHeaders(h)
	version := strings.TrimSpace(h.Get("version"))
	if needsOpenAICodexAstraVersion(model) && (version == "" || CompareVersions(version, openAICodexAstraMinVersion) < 0) {
		h.Set("version", openAICodexAstraMinVersion)
		h.Set("user-agent", buildCodexCLIUserAgent(openAICodexAstraMinVersion))
		h.Set("originator", openai.CodexDefaultOriginator)
	}
}

func needsOpenAICodexAstraVersion(model string) bool {
	m := strings.ToLower(normalizeOpenAICodexTicketModel(model))
	return strings.Contains(m, "gpt-6") || strings.Contains(m, "astra")
}

func (s *OpenAIGatewayService) StartOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || s.openaiCodexTicketDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.openaiCodexTicketCancel = cancel
	s.openaiCodexTicketDone = done
	go func() {
		defer close(done)
		s.openAICodexTicketHarvestLoop(ctx)
	}()
	// Do not synchronously read runtime settings while the harvester is being
	// wired. The loop performs the authoritative read with its cancellable
	// context; a startup DB read here can race that first probe and prolong
	// shutdown during a storage outage.
	enabled := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.Enabled
	configuredProxyCount := OpenAICodexTicketHarvestProxyPoolSize(s.openAICodexTicketConfig().HarvestProxyURL)
	if enabled && configuredProxyCount == 0 {
		logger.L().Warn("openai_codex_ticket enabled without harvest proxy", zap.String("reason", "no_harvest_proxy"))
	}
	logger.L().Info("openai_codex_ticket harvester started",
		zap.Bool("enabled", enabled),
		zap.Int("harvest_proxy_count", configuredProxyCount),
		zap.Int("ttl_seconds", s.openAICodexTicketConfig().TTLSeconds),
		zap.Int("target_length", s.openAICodexTicketConfig().TargetLength),
		zap.Strings("models", s.openAICodexTicketConfig().Models),
	)
}

func (s *OpenAIGatewayService) StopOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	s.openaiCodexTicketStopped = true
	cancel, done := s.openaiCodexTicketCancel, s.openaiCodexTicketDone
	s.openaiCodexTicketLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.refreshOpenAICodexTickets(ctx)
			timer.Reset(time.Duration(s.openAICodexTicketConfig().HarvestProbeIntervalSeconds) * time.Second)
		}
	}
}

// refreshOpenAICodexTickets probes each account/model with a missing or soon-to-expire
// ticket once. The loop waits for all probes, then waits the configured interval
// before starting the next cycle.
func (s *OpenAIGatewayService) refreshOpenAICodexTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil {
		return
	}
	if !s.openAICodexTicketEnabledContext(ctx) {
		logger.L().Debug("openai_codex_ticket cycle skipped", zap.String("reason", "disabled"))
		return
	}
	proxyURLs := s.openAICodexTicketHarvestProxyURLsContext(ctx)
	if len(proxyURLs) == 0 {
		logger.L().Warn("openai_codex_ticket cycle skipped", zap.String("reason", "no_harvest_proxy"))
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		logger.L().Warn("openai_codex_ticket list accounts failed", zap.String("reason", "account_list"))
		return
	}
	cfg := s.openAICodexTicketConfig()
	harvestOnly := len(proxyURLs) > 1
	ticketAccountIDs := append([]int64(nil), cfg.AccountIDs...)
	if s.settingService != nil {
		ticketAccountIDs = s.settingService.GetOpenAICodexTicketAccountIDs(ctx)
	}
	now := time.Now()
	refreshBefore := time.Duration(cfg.RefreshBeforeSeconds) * time.Second
	var wg sync.WaitGroup
	probed := 0
	eligibleAccounts := 0
	eligibleModels := 0
	for i := range accounts {
		account := accounts[i]
		if account.Status != StatusActive || !isOpenAICodexTicketAccount(&account) || !OpenAICodexTicketAccountAllowed(&account, ticketAccountIDs) {
			continue
		}
		eligibleAccounts++
		accountCfg := cfg
		if s.settingService != nil {
			accountCfg.AccountModels = s.settingService.GetOpenAICodexTicketAccountModels(ctx)
		}
		for _, model := range OpenAICodexTicketModelsForAccount(&account, accountCfg) {
			model := normalizeOpenAICodexTicketModel(model)
			if model == "" || (harvestOnly && isOpenAICodexTicketSolModel(model)) {
				continue
			}
			eligibleModels++
			// 已有一张有效且未临近过期的票 → 本周期不打，省得白刷。
			if t := s.lookupOpenAICodexTicket(&account, model); t.valid(now, cfg.TargetLength) && !t.needsRefresh(now, refreshBefore) {
				continue
			}
			acc := account
			// Token/header helpers may update account metadata; each model owns its maps.
			acc.Extra = maps.Clone(account.Extra)
			acc.Credentials = maps.Clone(account.Credentials)
			probed++
			wg.Add(1)
			go func(acc Account, model string) {
				defer wg.Done()
				s.probeOnceOpenAICodexTicket(ctx, &acc, model)
			}(acc, model)
		}
	}
	wg.Wait()
	if eligibleAccounts == 0 {
		logger.L().Warn("openai_codex_ticket cycle found no eligible accounts",
			zap.Int("listed_accounts", len(accounts)),
			zap.Int("allowlist_count", len(ticketAccountIDs)),
		)
	} else if eligibleModels == 0 {
		logger.L().Warn("openai_codex_ticket cycle found no eligible models",
			zap.Int("eligible_accounts", eligibleAccounts),
			zap.Int("configured_model_count", len(cfg.Models)),
		)
	}
	if ctx.Err() == nil {
		completion := s.OpenAICodexTicketHarvestCompletion(ctx)
		poolStatus := s.OpenAICodexTicketHarvestPoolStatus(ctx)
		logger.L().Info("openai_codex_ticket harvest pool status",
			zap.Int("pool_size", poolStatus.PoolSize),
			zap.Int("available_entries", poolStatus.AvailableCount),
			zap.Int("cooling_entries", poolStatus.CoolingDownCount),
		)
		if completion.Complete {
			logger.L().Info("openai_codex_ticket harvest scope complete",
				zap.Int("eligible_accounts", completion.EligibleAccountCount),
				zap.Int("eligible_pairs", completion.EligiblePairCount),
				zap.Int("ready_pairs", completion.ReadyPairCount),
			)
		}
	}
	if probed > 0 {
		logger.L().Info("openai_codex_ticket probe cycle", zap.Int("probed", probed))
	}
}

// probeOnceOpenAICodexTicket 走一个池中代理打一发。命中严格 292（HTTP 200、
// gAAAAA 前缀）就落库；否则记 Info miss，交给下个周期轮换下一个入口。同一 key 并发去重，避免上一发还没
// 回来又叠一发。
func (s *OpenAIGatewayService) probeOnceOpenAICodexTicket(ctx context.Context, account *Account, model string) {
	if s == nil || !isOpenAICodexTicketAccount(account) || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) || !s.openAICodexTicketAccountAllowed(ctx, account) || !s.openAICodexTicketAccountModelAllowed(ctx, account, model) {
		return
	}
	cfg := s.openAICodexTicketConfig()
	poolConfigured := len(s.openAICodexTicketHarvestProxyURLsContext(ctx)) > 0
	proxyURL := s.nextOpenAICodexTicketHarvestProxy(ctx)
	if proxyURL == "" || s.httpUpstream == nil || ctx.Err() != nil {
		reason := "upstream_unavailable"
		if proxyURL == "" {
			reason = "no_harvest_proxy"
			if poolConfigured {
				reason = "harvest_proxy_pool_cooldown"
			}
		}
		logger.L().Warn("openai_codex_ticket probe skipped",
			zap.Int64("account_id", account.ID), zap.String("model", model),
			zap.String("reason", reason),
		)
		return
	}
	key := openAICodexTicketKey(account.ID, model)
	_, _, _ = s.openaiCodexTicketFlight.Do(key, func() (any, error) {
		token, _, err := s.GetAccessToken(ctx, account)
		if err != nil || strings.TrimSpace(token) == "" {
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("reason", "token"),
			)
			return nil, nil
		}
		state, status, perr := s.fireOpenAICodexTicketProbe(ctx, account, token, model, proxyURL, time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second)
		if perr != nil {
			s.recordOpenAICodexTicketHarvestProxyResult(proxyURL, OpenAICodexTicketHarvestFailureTransport, status, len(state))
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("entry_id", OpenAICodexTicketHarvestProxyID(proxyURL)),
				zap.String("reason", string(OpenAICodexTicketHarvestFailureTransport)),
			)
			return nil, nil
		}
		if status != http.StatusOK || !openAICodexTicketStateLengthAccepted(state, cfg.TargetLength) {
			category := classifyOpenAICodexTicketHarvestFailure(status, state)
			s.recordOpenAICodexTicketHarvestProxyResult(proxyURL, category, status, len(state))
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("entry_id", OpenAICodexTicketHarvestProxyID(proxyURL)),
				zap.String("reason", string(category)),
				zap.Int("http", status), zap.Int("state_length", len(state)),
			)
			return nil, nil
		}
		now := time.Now()
		ticket := &openAICodexTicket{
			AccountID:  account.ID,
			Model:      model,
			State:      state,
			Length:     len(state),
			CapturedAt: now,
			ExpiresAt:  now.Add(time.Duration(cfg.TTLSeconds) * time.Second),
			Attempts:   1,
		}
		// The listener produced a valid response, so clear its circuit state
		// before the independent database write. Persistence failures are not
		// proxy failures and must not cool a healthy entry.
		s.recordOpenAICodexTicketHarvestProxyResult(proxyURL, OpenAICodexTicketHarvestFailureNone, status, ticket.Length)
		if err := s.persistOpenAICodexTicket(ctx, account, ticket); err != nil {
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("entry_id", OpenAICodexTicketHarvestProxyID(proxyURL)),
				zap.String("reason", "persistence"),
			)
			return nil, nil
		}
		logger.L().Info("openai_codex_ticket harvested",
			zap.Int64("account_id", account.ID), zap.String("model", model),
			zap.String("entry_id", OpenAICodexTicketHarvestProxyID(proxyURL)),
			zap.Int("length", ticket.Length), zap.String("mode", "continuous"))
		return nil, nil
	})
}

func classifyOpenAICodexTicketHarvestFailure(status int, state string) OpenAICodexTicketHarvestFailureCategory {
	if status <= 0 {
		return OpenAICodexTicketHarvestFailureTransport
	}
	if status != http.StatusOK {
		if status == 312 {
			return OpenAICodexTicketHarvestFailureHTTP312
		}
		return OpenAICodexTicketHarvestFailureHTTPStatus
	}
	if strings.TrimSpace(state) == "" {
		return OpenAICodexTicketHarvestFailureIncomplete
	}
	if len(strings.TrimSpace(state)) == 312 {
		return OpenAICodexTicketHarvestFailureLength312
	}
	return OpenAICodexTicketHarvestFailureState
}

// IsOpenAICodexTicketExtraKey identifies server-managed ticket material.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix)
}

// MergeOpenAICodexTicketExtra preserves only persisted tickets, never summaries or
// blobs supplied by an account edit. The repository repeats this under the row
// lock so a concurrent harvest cannot be overwritten by a stale admin snapshot.
func MergeOpenAICodexTicketExtra(extra, current map[string]any) map[string]any {
	result := maps.Clone(extra)
	for key := range result {
		if IsOpenAICodexTicketExtraKey(key) {
			delete(result, key)
		}
	}
	for key, value := range current {
		if IsOpenAICodexTicketExtraKey(key) {
			if result == nil {
				result = make(map[string]any)
			}
			result[key] = value
		}
	}
	return result
}

// ValidateOpenAICodexTicketHarvestProxyURL validates only syntax, without making
// a network request or including credentials in validation errors.
func ValidateOpenAICodexTicketHarvestProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("harvest proxy must be an HTTP(S) or SOCKS5(h) URL with a host and no path, query or fragment")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("harvest proxy scheme must be http, https, socks5 or socks5h")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("harvest proxy port must be between 1 and 65535")
		}
	}
	return nil
}

// MaskProxyURL never returns a stored proxy password, even for invalid legacy data.
// A pool is returned one masked URL per line.
func MaskProxyURL(raw string) string {
	entries, err := SplitOpenAICodexTicketHarvestProxyURLs(raw)
	if err != nil || len(entries) == 0 {
		return ""
	}
	masked := make([]string, 0, len(entries))
	for _, entry := range entries {
		parsed, _ := url.Parse(entry)
		if parsed.User != nil {
			if _, ok := parsed.User.Password(); ok {
				parsed.User = url.UserPassword(parsed.User.Username(), "***")
			}
		}
		masked = append(masked, parsed.String())
	}
	return strings.Join(masked, "\n")
}

// IsMaskedProxyURL recognizes the exact password placeholder emitted by the API.
func IsMaskedProxyURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	entries, err := SplitOpenAICodexTicketHarvestProxyURLs(raw)
	if err != nil {
		return false
	}
	sawMasked := false
	for _, entry := range entries {
		parsed, err := url.Parse(entry)
		if err != nil || parsed.User == nil {
			continue
		}
		password, ok := parsed.User.Password()
		if !ok {
			continue
		}
		if password != "***" {
			return false
		}
		sawMasked = true
	}
	return sawMasked
}

// Credential shadows do not own tickets. Keep their existing forwarding policy
// instead of imposing a gate for a key the harvester never populates.
func isOpenAICodexTicketAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike() && !account.IsShadow()
}

// IsOpenAICodexTicketPrivateExtraKey also covers the retired account-level proxy
// override, whose credentials may remain in older account records.
func IsOpenAICodexTicketPrivateExtraKey(key string) bool {
	return IsOpenAICodexTicketExtraKey(key) || key == "codex_harvest_proxy_url"
}

// RedactOpenAICodexTicketExtra strips ephemeral ticket material from exports
// without changing the source account or unrelated backup fields.
func RedactOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}
