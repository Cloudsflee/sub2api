package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	// CodexTicketProxySyncAdvisoryLockID is shared by every application
	// instance so concurrent admin saves serialize on the database.
	CodexTicketProxySyncAdvisoryLockID int64 = 684318217693421907
	codexTicketProxySyncAdvisoryLockID       = CodexTicketProxySyncAdvisoryLockID
)

var (
	// ErrCodexTicketProxyDisabled is intentionally structured so an operator
	// can distinguish a disabled managed proxy from a malformed URL without
	// exposing proxy credentials in an API error or log line.
	ErrCodexTicketProxyDisabled = infraerrors.Conflict(
		"CODEX_TICKET_PROXY_DISABLED",
		"the matching Codex ticket harvest proxy is disabled",
	)
	ErrCodexTicketProxySyncUnavailable = infraerrors.InternalServer(
		"CODEX_TICKET_PROXY_SYNC_UNAVAILABLE",
		"Codex ticket proxy synchronization is not configured",
	)
	ErrCodexTicketProxyResolveFailed = infraerrors.InternalServer(
		"CODEX_TICKET_PROXY_RESOLVE_FAILED",
		"Codex ticket harvest proxy resolution failed",
	)
	ErrCodexTicketProxyAccountListFailed = infraerrors.InternalServer(
		"CODEX_TICKET_PROXY_ACCOUNT_LIST_FAILED",
		"Codex ticket account reconciliation lookup failed",
	)
	ErrCodexTicketProxyBindFailed = infraerrors.InternalServer(
		"CODEX_TICKET_PROXY_BIND_FAILED",
		"Codex ticket business proxy binding failed",
	)
)

// CodexTicketProxySpec is the normalized identity persisted in the managed
// proxy table. It deliberately contains credentials only in memory and is
// never formatted into logs or audit records.
type CodexTicketProxySpec struct {
	Protocol string
	Host     string
	Port     int
	Username string
	Password string
}

// CodexTicketProxyResolver is implemented by the proxy repository. Keeping it
// separate from ProxyRepository lets existing test doubles continue to satisfy
// the broad proxy contract while the synchronization path gets an atomic
// find-or-create operation.
type CodexTicketProxyResolver interface {
	FindOrCreateCodexTicketProxy(ctx context.Context, spec CodexTicketProxySpec) (*Proxy, error)
}

// CodexTicketBusinessProxyWriter updates several account rows and emits one
// merged scheduler outbox event in the caller's transaction.
type CodexTicketBusinessProxyWriter interface {
	UpdateCodexTicketBusinessProxy(ctx context.Context, proxyID int64, accountIDs []int64) ([]int64, error)
}

type codexTicketAccountStatusLister interface {
	ListByPlatformAllStatuses(ctx context.Context, platform string) ([]Account, error)
}

// CodexTicketProxySyncResult contains operational counters. The result is
// useful to tests and callers that want to expose metrics, but it intentionally
// carries no URL, password, token, or account credential.
type CodexTicketProxySyncResult struct {
	ProxyID       int64
	TargetCount   int
	ChangedCount  int
	SkippedCount  int
	SkippedIDs    int
	SkippedStatus int
	SkippedType   int
	SkippedShadow int
	SkippedBound  int
}

// CodexTicketProxySyncService coordinates settings, proxy identity, account
// bindings, and scheduler invalidation. All durable writes are performed under
// one transaction when an Ent client is available.
type CodexTicketProxySyncService struct {
	entClient               *dbent.Client
	settingRepo             SettingRepository
	accountRepo             AccountRepository
	proxyRepo               ProxyRepository
	proxyResolver           CodexTicketProxyResolver
	accountWriter           CodexTicketBusinessProxyWriter
	schedulerCache          SchedulerCache
	fallbackHarvestProxyURL string
}

// SetFallbackHarvestProxyURL supplies the startup YAML/env proxy used when the
// database setting is omitted (an explicit empty database value still clears
// synchronization for that save).
func (s *CodexTicketProxySyncService) SetFallbackHarvestProxyURL(raw string) {
	if s == nil {
		return
	}
	s.fallbackHarvestProxyURL = strings.TrimSpace(raw)
}

func NewCodexTicketProxySyncService(
	entClient *dbent.Client,
	settingRepo SettingRepository,
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	schedulerCache SchedulerCache,
) *CodexTicketProxySyncService {
	svc := &CodexTicketProxySyncService{
		entClient:      entClient,
		settingRepo:    settingRepo,
		accountRepo:    accountRepo,
		proxyRepo:      proxyRepo,
		schedulerCache: schedulerCache,
	}
	if resolver, ok := proxyRepo.(CodexTicketProxyResolver); ok {
		svc.proxyResolver = resolver
	}
	if writer, ok := accountRepo.(CodexTicketBusinessProxyWriter); ok {
		svc.accountWriter = writer
	}
	return svc
}

// PersistSettings implements SettingsWriteCoordinator. The settings map is
// already normalized and has omitted keys removed by SettingService.
func (s *CodexTicketProxySyncService) PersistSettings(ctx context.Context, updates map[string]string, settings *SystemSettings) error {
	if s == nil || s.settingRepo == nil {
		return ErrCodexTicketProxySyncUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	effectiveProxyURL := ""
	if settings != nil {
		effectiveProxyURL = strings.TrimSpace(settings.OpenAICodexTicketHarvestProxyURL)
		if effectiveProxyURL == "" {
			if _, explicitlyWritten := updates[SettingKeyOpenAICodexTicketHarvestProxyURL]; !explicitlyWritten {
				effectiveProxyURL = s.fallbackHarvestProxyURL
			}
		}
	}
	poolEntries, poolErr := SplitOpenAICodexTicketHarvestProxyURLs(effectiveProxyURL)
	if poolErr != nil {
		return infraerrors.BadRequest("INVALID_CODEX_HARVEST_PROXY", poolErr.Error())
	}
	// A pool represents independent harvest exits. Binding one arbitrary entry
	// to business traffic would silently defeat the pool and change the two
	// target accounts' existing proxy IDs. Persist the explicit harvest-only
	// mode while leaving those bindings untouched.
	if len(poolEntries) > 1 {
		if settings != nil {
			settings.OpenAICodexTicketSyncBusinessProxy = false
		}
		poolUpdates := make(map[string]string, len(updates)+1)
		for key, value := range updates {
			poolUpdates[key] = value
		}
		poolUpdates[SettingKeyOpenAICodexTicketSyncBusinessProxy] = "false"
		if s.entClient == nil {
			return s.settingRepo.SetMultiple(ctx, poolUpdates)
		}
		tx, err := s.entClient.Tx(ctx)
		if err != nil {
			return fmt.Errorf("begin Codex harvest-only settings transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		txCtx := dbent.NewTxContext(ctx, tx)
		if _, err := tx.Client().ExecContext(txCtx,
			"SELECT pg_advisory_xact_lock($1)", codexTicketProxySyncAdvisoryLockID); err != nil {
			return fmt.Errorf("acquire Codex harvest-only settings lock: %w", err)
		}
		if err := s.settingRepo.SetMultiple(txCtx, poolUpdates); err != nil {
			return fmt.Errorf("persist harvest-only settings: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit harvest-only settings: %w", err)
		}
		return nil
	}
	// No account reconciliation is possible when the feature is disabled or
	// the harvest proxy is intentionally cleared. A normal repository write is
	// sufficient in that case and avoids taking the cross-instance lock for
	// unrelated settings saves.
	if settings == nil || !settings.OpenAICodexTicketSyncBusinessProxy || effectiveProxyURL == "" {
		return s.settingRepo.SetMultiple(ctx, updates)
	}
	if s.entClient == nil {
		// Lightweight unit-test and migration environments may not have an Ent
		// client. Preserve their repository semantics while still running the
		// reconciliation algorithm when optional capabilities are supplied.
		changed, result, err := s.syncBindings(ctx, settings, effectiveProxyURL)
		if err != nil {
			return err
		}
		if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
			return err
		}
		s.logSyncResultIfReconciled(result)
		s.refreshSchedulerSnapshots(ctx, changed)
		return nil
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin Codex ticket proxy synchronization transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if _, err := tx.Client().ExecContext(txCtx,
		"SELECT pg_advisory_xact_lock($1)", codexTicketProxySyncAdvisoryLockID); err != nil {
		return fmt.Errorf("acquire Codex ticket proxy synchronization lock: %w", err)
	}
	changed, result, err := s.syncBindings(txCtx, settings, effectiveProxyURL)
	if err != nil {
		return err
	}
	if err := s.settingRepo.SetMultiple(txCtx, updates); err != nil {
		return fmt.Errorf("persist settings in Codex ticket proxy transaction: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Codex ticket proxy synchronization transaction: %w", err)
	}
	s.logSyncResultIfReconciled(result)
	s.refreshSchedulerSnapshots(ctx, changed)
	return nil
}

// Sync performs an immediate reconciliation without changing settings. It is
// useful for startup/operator repair flows and keeps the same transaction and
// rollback guarantees as an admin settings save.
func (s *CodexTicketProxySyncService) Sync(ctx context.Context, settings *SystemSettings) error {
	return s.PersistSettings(ctx, map[string]string{}, settings)
}

func (s *CodexTicketProxySyncService) Reconcile(ctx context.Context, settings *SystemSettings) error {
	return s.Sync(ctx, settings)
}

func (s *CodexTicketProxySyncService) syncBindings(ctx context.Context, settings *SystemSettings, effectiveProxyURL string) ([]int64, CodexTicketProxySyncResult, error) {
	if settings == nil || !settings.OpenAICodexTicketSyncBusinessProxy {
		return nil, CodexTicketProxySyncResult{}, nil
	}
	rawProxyURL := strings.TrimSpace(effectiveProxyURL)
	if rawProxyURL == "" {
		// Clearing the harvest proxy is deliberately non-destructive: existing
		// business proxy assignments remain in place.
		return nil, CodexTicketProxySyncResult{}, nil
	}
	if OpenAICodexTicketHarvestOnlyMode(rawProxyURL) {
		return nil, CodexTicketProxySyncResult{}, nil
	}
	primaryProxyURL, err := FirstOpenAICodexTicketHarvestProxyURL(rawProxyURL)
	if err != nil {
		return nil, CodexTicketProxySyncResult{}, infraerrors.BadRequest("INVALID_CODEX_HARVEST_PROXY", err.Error())
	}
	spec, err := NormalizeCodexTicketProxyURL(primaryProxyURL)
	if err != nil {
		return nil, CodexTicketProxySyncResult{}, infraerrors.BadRequest("INVALID_CODEX_HARVEST_PROXY", err.Error())
	}
	if s.proxyResolver == nil || s.accountRepo == nil {
		return nil, CodexTicketProxySyncResult{}, ErrCodexTicketProxySyncUnavailable
	}
	managedProxy, err := s.proxyResolver.FindOrCreateCodexTicketProxy(ctx, spec)
	if err != nil {
		if errors.Is(err, ErrCodexTicketProxyDisabled) {
			return nil, CodexTicketProxySyncResult{}, err
		}
		return nil, CodexTicketProxySyncResult{}, ErrCodexTicketProxyResolveFailed
	}
	if managedProxy == nil || managedProxy.ID <= 0 {
		return nil, CodexTicketProxySyncResult{}, ErrCodexTicketProxySyncUnavailable
	}

	var accounts []Account
	if lister, ok := s.accountRepo.(codexTicketAccountStatusLister); ok {
		accounts, err = lister.ListByPlatformAllStatuses(ctx, PlatformOpenAI)
	} else {
		accounts, err = s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	}
	if err != nil {
		return nil, CodexTicketProxySyncResult{}, ErrCodexTicketProxyAccountListFailed
	}
	allowlist := make(map[int64]struct{}, len(settings.OpenAICodexTicketAccountIDs))
	for _, id := range settings.OpenAICodexTicketAccountIDs {
		if id > 0 {
			allowlist[id] = struct{}{}
		}
	}
	targetIDs := make([]int64, 0, len(accounts))
	result := CodexTicketProxySyncResult{ProxyID: managedProxy.ID}
	seenAccountIDs := make(map[int64]struct{}, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if account == nil || account.ID <= 0 {
			result.SkippedIDs++
			continue
		}
		seenAccountIDs[account.ID] = struct{}{}
		if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
			result.SkippedType++
			continue
		}
		if account.IsShadow() {
			result.SkippedShadow++
			continue
		}
		if !account.IsActive() || (account.ExpiresAt != nil && !account.ExpiresAt.After(time.Now())) {
			result.SkippedStatus++
			continue
		}
		if account.ProxyID != nil && *account.ProxyID == managedProxy.ID {
			result.SkippedBound++
			continue
		}
		if len(allowlist) > 0 {
			if _, ok := allowlist[account.ID]; !ok {
				result.SkippedIDs++
				continue
			}
		}
		targetIDs = append(targetIDs, account.ID)
	}
	for id := range allowlist {
		if _, seen := seenAccountIDs[id]; !seen {
			// Includes deleted or otherwise absent allowlist entries. They are
			// counted for observability but never treated as a hard failure.
			result.SkippedIDs++
		}
	}
	sort.Slice(targetIDs, func(i, j int) bool { return targetIDs[i] < targetIDs[j] })
	result.TargetCount = len(targetIDs)
	result.SkippedCount = result.SkippedIDs + result.SkippedStatus + result.SkippedType + result.SkippedShadow + result.SkippedBound
	if len(targetIDs) == 0 {
		return nil, result, nil
	}

	if s.accountWriter == nil {
		return nil, result, ErrCodexTicketProxySyncUnavailable
	}
	changed, err := s.accountWriter.UpdateCodexTicketBusinessProxy(ctx, managedProxy.ID, targetIDs)
	if err != nil {
		return nil, result, ErrCodexTicketProxyBindFailed
	}
	result.ChangedCount = len(changed)
	return changed, result, nil
}

// refreshSchedulerSnapshots runs after the settings/account transaction has
// committed. The durable outbox event remains the fallback when Redis is down.
func (s *CodexTicketProxySyncService) refreshSchedulerSnapshots(ctx context.Context, changed []int64) {
	if len(changed) > 0 && s.schedulerCache != nil && s.accountRepo != nil {
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		fresh, readErr := s.accountRepo.GetByIDs(refreshCtx, changed)
		if readErr != nil {
			slog.Warn("Codex ticket proxy scheduler snapshot refresh deferred", "account_count", len(changed), "error_category", "account_reload")
			return
		}
		for _, account := range fresh {
			if account == nil {
				continue
			}
			if cacheErr := s.schedulerCache.SetAccount(refreshCtx, account); cacheErr != nil {
				slog.Warn("Codex ticket proxy scheduler snapshot refresh failed", "account_id", account.ID, "error_category", "scheduler_cache")
			}
		}
	}
}

func (s *CodexTicketProxySyncService) logSyncResultIfReconciled(result CodexTicketProxySyncResult) {
	if result.ProxyID <= 0 {
		return
	}
	slog.Info("Codex ticket business proxy reconciliation completed",
		"proxy_id", result.ProxyID,
		"target_count", result.TargetCount,
		"changed_count", result.ChangedCount,
		"skipped_count", result.SkippedCount,
		"skipped_id_count", result.SkippedIDs,
		"skipped_status_count", result.SkippedStatus,
		"skipped_type_count", result.SkippedType,
		"skipped_shadow_count", result.SkippedShadow,
		"skipped_already_bound_count", result.SkippedBound,
	)
}

// NormalizeCodexTicketProxyURL validates and canonicalizes a harvest proxy URL
// without retaining its textual URL form in the synchronization state.
func NormalizeCodexTicketProxyURL(raw string) (CodexTicketProxySpec, error) {
	raw = strings.TrimSpace(raw)
	if err := ValidateOpenAICodexTicketHarvestProxyURL(raw); err != nil {
		return CodexTicketProxySpec{}, err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return CodexTicketProxySpec{}, errors.New("invalid harvest proxy URL")
	}
	protocol := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if protocol == "" || host == "" {
		return CodexTicketProxySpec{}, errors.New("harvest proxy must include a scheme and host")
	}
	port := 0
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return CodexTicketProxySpec{}, errors.New("harvest proxy port must be between 1 and 65535")
		}
	} else {
		switch protocol {
		case "http":
			port = 80
		case "https":
			port = 443
		default:
			port = 1080
		}
	}
	username, password := "", ""
	if parsed.User != nil {
		username = parsed.User.Username()
		if decoded, ok := parsed.User.Password(); ok {
			password = decoded
		}
	}
	return CodexTicketProxySpec{
		Protocol: protocol,
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
	}, nil
}
