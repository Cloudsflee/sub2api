package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	openaipkg "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/google/uuid"
)

const (
	openAI5hWakeConcurrency = 8
	// A single quota-pool attempt can include a 20s usage probe, the wake
	// request, and a second usage probe. The item budget is derived from the
	// number of candidate accounts below so fallback members are not starved by
	// the first slow proxy.
	openAI5hWakeAccountAttemptTimeout = 75 * time.Second
	openAI5hWakeItemTimeout           = 10 * time.Minute
	openAI5hWakeRequestTimeout        = 30 * time.Second
	openAI5hWakeClaimTimeout          = 10 * time.Second
	openAI5hWakeLeaseDuration         = 60 * time.Second
	openAI5hWakeHeartbeat             = 20 * time.Second
	openAI5hWakeCancelPoll            = time.Second
	openAI5hWakeClaimPoll             = 2 * time.Second
	openAI5hWakeRetention             = 30 * 24 * time.Hour
	openAI5hWakeCleanupInterval       = 24 * time.Hour
	openAI5hWakeEventTimeout          = 3 * time.Second
	openAI5hWakeEventMessageMax       = 2000
	openAI5hWakeDiagnosticMax         = 600
	openAI5hWakeMaxItemAttempts       = 3
	openAI5hWakeResponseBodyMax       = 1 << 20
	openAI5hWakeConfirmationTimeout   = 8 * time.Second
	openAI5hWakeConfirmationDelay     = 250 * time.Millisecond
	openAI5hWakeConfirmationMaxDelay  = 2 * time.Second
	openAI5hWakeInstructions          = "Reply with OK."
	openAI5hWakeInput                 = "hi"
	openAI5hWakeAuditPath             = "/api/v1/admin/accounts/openai-5h-wake/tasks/:id"
	// OpenAI5hWakeSnapshotIdentityExtraKey is managed by the wake worker. It is
	// intentionally exported so repository credential persistence can invalidate
	// the marker when a quota identity changes without importing wake internals.
	OpenAI5hWakeSnapshotIdentityExtraKey = "codex_5h_wake_identity_hash"
	// This marker prevents a legacy or externally-written future reset timestamp
	// from being treated as proof that this wake worker verified the account.
	openAI5hWakeSnapshotIdentityKey = OpenAI5hWakeSnapshotIdentityExtraKey
)

type openAI5hWakeMonitorTimings struct {
	heartbeatInterval  time.Duration
	cancelPollInterval time.Duration
	leaseDuration      time.Duration
	heartbeatTimeout   time.Duration
	cancelPollTimeout  time.Duration
}

type openAI5hWakeConfirmationTimings struct {
	timeout  time.Duration
	delay    time.Duration
	maxDelay time.Duration
}

var defaultOpenAI5hWakeMonitorTimings = openAI5hWakeMonitorTimings{
	heartbeatInterval:  openAI5hWakeHeartbeat,
	cancelPollInterval: openAI5hWakeCancelPoll,
	leaseDuration:      openAI5hWakeLeaseDuration,
	heartbeatTimeout:   5 * time.Second,
	cancelPollTimeout:  3 * time.Second,
}

var defaultOpenAI5hWakeConfirmationTimings = openAI5hWakeConfirmationTimings{
	timeout:  openAI5hWakeConfirmationTimeout,
	delay:    openAI5hWakeConfirmationDelay,
	maxDelay: openAI5hWakeConfirmationMaxDelay,
}

var errOpenAI5hWakeSnapshotPersist = errors.New("openai 5h wake snapshot persistence failed")
var errOpenAI5hWakeIdentityChanged = errOpenAICodexSnapshotIdentityChanged
var errOpenAI5hWakeNoEntitlement = errors.New("openai 5h wake account has no 5h entitlement")
var errOpenAI5hWakeRepositoryContract = errors.New("openai 5h wake repository returned a nil task")

type openAI5hWakeQuotaGroup struct {
	identityHash string
	accounts     []*Account
}

type openAI5hWakePlan struct {
	preview OpenAI5hWakePreview
	groups  []openAI5hWakeQuotaGroup
}

// openAI5hWakeIdentityFingerprint captures every account attribute that can
// change the meaning of a persisted wake snapshot. Access-token rotation does
// not change this fingerprint, while switching the account type, quota
// dimension, parent/shadow status, or any typed upstream identity does.
type openAI5hWakeIdentityFingerprint struct {
	platform       string
	accountType    string
	quotaDimension string
	shadow         bool
	identityHash   string
}

func openAI5hWakeIdentityFingerprintFor(account *Account) openAI5hWakeIdentityFingerprint {
	if account == nil {
		return openAI5hWakeIdentityFingerprint{}
	}
	return openAI5hWakeIdentityFingerprint{
		platform:       account.Platform,
		accountType:    account.Type,
		quotaDimension: account.QuotaDimensionOrDefault(),
		shadow:         account.IsShadow(),
		identityHash:   openAI5hWakeIdentityHash(account),
	}
}

// OpenAI5hWakeService owns durable task creation and the lease-based worker.
type OpenAI5hWakeService struct {
	repo            OpenAI5hWakeTaskRepository
	accountRepo     AccountRepository
	quotaService    *OpenAIQuotaService
	tokenProvider   *OpenAITokenProvider
	httpUpstream    HTTPUpstream
	tlsProfiles     *TLSFingerprintProfileService
	auditService    *AuditLogService
	agentIdentityWS agentIdentityWSConnectionInvalidator
	agentIdentityMu sync.Mutex

	owner     string
	startOnce sync.Once
	notify    chan struct{}
	runningMu sync.Mutex
	running   map[int64]context.CancelFunc
}

func NewOpenAI5hWakeService(
	repo OpenAI5hWakeTaskRepository,
	accountRepo AccountRepository,
	quotaService *OpenAIQuotaService,
	tokenProvider *OpenAITokenProvider,
	httpUpstream HTTPUpstream,
	tlsProfiles *TLSFingerprintProfileService,
	auditService *AuditLogService,
	openAIGateway *OpenAIGatewayService,
) *OpenAI5hWakeService {
	service := &OpenAI5hWakeService{
		repo:          repo,
		accountRepo:   accountRepo,
		quotaService:  quotaService,
		tokenProvider: tokenProvider,
		httpUpstream:  httpUpstream,
		tlsProfiles:   tlsProfiles,
		auditService:  auditService,
		owner:         uuid.NewString(),
		notify:        make(chan struct{}, 1),
		running:       make(map[int64]context.CancelFunc),
	}
	if openAIGateway != nil {
		service.agentIdentityWS = openAIGateway
	}
	return service
}

func ProvideOpenAI5hWakeService(
	repo OpenAI5hWakeTaskRepository,
	accountRepo AccountRepository,
	quotaService *OpenAIQuotaService,
	tokenProvider *OpenAITokenProvider,
	httpUpstream HTTPUpstream,
	tlsProfiles *TLSFingerprintProfileService,
	auditService *AuditLogService,
	openAIGateway *OpenAIGatewayService,
) *OpenAI5hWakeService {
	service := NewOpenAI5hWakeService(repo, accountRepo, quotaService, tokenProvider, httpUpstream, tlsProfiles, auditService, openAIGateway)
	service.Start()
	return service
}

func (s *OpenAI5hWakeService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.startOnce.Do(func() { go s.runWorker() })
}

func (s *OpenAI5hWakeService) Preview(ctx context.Context) (*OpenAI5hWakePreview, error) {
	plan, err := s.buildPlan(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return &plan.preview, nil
}

func (s *OpenAI5hWakeService) CreateTask(ctx context.Context, requestedByUserID *int64, requestedByEmail string) (*OpenAI5hWakeTask, bool, error) {
	plan, err := s.buildPlan(ctx, time.Now().UTC())
	if err != nil {
		return nil, false, err
	}
	seeds := make([]OpenAI5hWakeTaskItemSeed, 0, len(plan.groups))
	for _, group := range plan.groups {
		ids := make([]int64, 0, len(group.accounts))
		for _, account := range group.accounts {
			ids = append(ids, account.ID)
		}
		seeds = append(seeds, OpenAI5hWakeTaskItemSeed{
			IdentityHash:     group.identityHash,
			MemberAccountIDs: ids,
		})
	}
	task, created, err := s.repo.CreateOrGetActive(ctx, OpenAI5hWakeCreateParams{
		EligibleAccountCount:  plan.preview.EligibleAccounts,
		ActiveWindowCount:     plan.preview.ActiveWindows,
		EstimatedRequestCount: plan.preview.EstimatedRequests,
		RequestedByUserID:     requestedByUserID,
		RequestedByEmail:      strings.TrimSpace(requestedByEmail),
		Items:                 seeds,
	})
	if err != nil {
		return nil, false, err
	}
	// The SQL repository rejects an empty plan before INSERT, but keep this
	// invariant at the service boundary as well. Implementations used by tests,
	// migrations, or alternate storage backends may still return a newly-created
	// zero-item task; such a task can never make progress and must not be
	// reported as accepted. An already-active task is intentionally allowed so
	// concurrent callers retain the idempotent "return active task" behavior.
	if created && len(seeds) == 0 {
		return nil, false, ErrOpenAI5hWakeNoEligiblePools
	}
	if task == nil {
		return nil, false, errOpenAI5hWakeRepositoryContract
	}
	if created {
		slog.Info("openai_5h_wake_started", "task_id", task.ID, "accounts", task.EligibleAccountCount, "pools", task.TotalItems, "estimated_requests", task.EstimatedRequestCount)
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelInfo, "task_created", fmt.Sprintf(
			"accounts=%d pools=%d active_windows=%d estimated_requests=%d",
			task.EligibleAccountCount, task.TotalItems, task.ActiveWindowCount, task.EstimatedRequestCount,
		))
	}
	s.signalWorker()
	return task, created, nil
}

func (s *OpenAI5hWakeService) GetTask(ctx context.Context, id int64) (*OpenAI5hWakeTask, error) {
	task, err := s.repo.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrOpenAI5hWakeTaskNotFound
	}
	return s.populateRunningItemCount(ctx, task)
}

func (s *OpenAI5hWakeService) GetLatestTask(ctx context.Context) (*OpenAI5hWakeTask, error) {
	task, err := s.repo.GetLatestTask(ctx)
	if err != nil || task == nil {
		return task, err
	}
	return s.populateRunningItemCount(ctx, task)
}

func (s *OpenAI5hWakeService) populateRunningItemCount(ctx context.Context, task *OpenAI5hWakeTask) (*OpenAI5hWakeTask, error) {
	if task == nil {
		return nil, ErrOpenAI5hWakeTaskNotFound
	}
	count, err := s.repo.CountRunningTaskItems(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	task.RunningItemCount = count
	return task, nil
}

func (s *OpenAI5hWakeService) ListTaskItems(ctx context.Context, taskID int64, page, pageSize int) ([]*OpenAI5hWakeTaskItem, int64, error) {
	return s.repo.ListTaskItems(ctx, taskID, page, pageSize)
}

func (s *OpenAI5hWakeService) ListTaskEvents(ctx context.Context, taskID int64, page, pageSize int) ([]*OpenAI5hWakeTaskEvent, int64, error) {
	return s.repo.ListTaskEvents(ctx, taskID, page, pageSize)
}

func (s *OpenAI5hWakeService) CancelTask(ctx context.Context, taskID int64) (*OpenAI5hWakeTask, error) {
	task, requested, err := s.repo.RequestCancel(ctx, taskID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrOpenAI5hWakeTaskNotFound
	}
	if !requested {
		return task, nil
	}
	s.runningMu.Lock()
	cancel := s.running[taskID]
	s.runningMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.recordTaskEvent(taskID, nil, OpenAI5hWakeEventLevelWarn, "cancel_requested", "")
	s.signalWorker()
	slog.Info("openai_5h_wake_cancel_requested", "task_id", taskID)
	return task, nil
}

func (s *OpenAI5hWakeService) signalWorker() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *OpenAI5hWakeService) buildPlan(ctx context.Context, now time.Time) (*openAI5hWakePlan, error) {
	accounts, err := listOpenAI5hWakeAccounts(ctx, s.accountRepo)
	if err != nil {
		return nil, err
	}
	plan := &openAI5hWakePlan{}
	plan.preview.TotalOpenAIAccounts = len(accounts)
	eligible := make([]*Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		reason := classifyOpenAI5hWakeExclusion(account, now)
		if reason == "" && len(openAI5hWakeIdentityParts(account)) == 0 {
			reason = "missing_identity"
		}
		if reason != "" {
			incrementOpenAI5hWakeExclusion(&plan.preview.Excluded, reason)
			continue
		}
		eligible = append(eligible, account)
	}
	plan.preview.EligibleAccounts = len(eligible)
	plan.groups = buildOpenAI5hWakeQuotaGroups(eligible)
	plan.preview.UniqueQuotaPools = len(plan.groups)
	for _, group := range plan.groups {
		if _, _, ok := activeWakeSnapshot(group.accounts, now); ok {
			plan.preview.ActiveWindows++
		}
	}
	plan.preview.EstimatedRequests = plan.preview.UniqueQuotaPools - plan.preview.ActiveWindows
	return plan, nil
}

// openAI5hWakeAllStatusRepository is deliberately optional.  Most account
// repository consumers rely on ListByPlatform's active-only behavior, while a
// wake preview must see disabled accounts so its exclusion totals are honest.
// Keeping this capability out of AccountRepository avoids widening every test
// double and every alternate repository implementation.
type openAI5hWakeAllStatusRepository interface {
	ListByPlatformAllStatuses(context.Context, string) ([]Account, error)
}

func listOpenAI5hWakeAccounts(ctx context.Context, repository AccountRepository) ([]Account, error) {
	if allStatuses, ok := repository.(openAI5hWakeAllStatusRepository); ok {
		return allStatuses.ListByPlatformAllStatuses(ctx, PlatformOpenAI)
	}
	return repository.ListByPlatform(ctx, PlatformOpenAI)
}

func classifyOpenAI5hWakeExclusion(account *Account, now time.Time) string {
	if account == nil {
		return "disabled"
	}
	if account.Type == AccountTypeAPIKey {
		return "api_key"
	}
	if account.Type != AccountTypeOAuth {
		return "non_oauth"
	}
	if account.IsShadow() {
		return "spark_shadow"
	}
	if account.QuotaDimensionOrDefault() != QuotaDimensionGlobal {
		return "non_global"
	}
	if account.Status != StatusActive {
		return "disabled"
	}
	if openAI5hWakeHasKnownNoEntitlement(account) {
		return "no_5h_entitlement"
	}
	if !account.Schedulable {
		return "unschedulable"
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		return "expired"
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		return "rate_limited"
	}
	if (account.OverloadUntil != nil && now.Before(*account.OverloadUntil)) ||
		(account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil)) {
		return "cooling_down"
	}
	return ""
}

func openAI5hWakeHasKnownNoEntitlement(account *Account) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type"))) {
	case "free", "abnormal":
		return true
	}
	windowMinutes, observed := openAI5hWakeWindowMinutes(account.Extra)
	return observed && windowMinutes == 0
}

func openAI5hWakeWindowMinutes(extra map[string]any) (int64, bool) {
	if extra == nil {
		return 0, false
	}
	value, exists := extra["codex_5h_window_minutes"]
	if !exists || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), float64(int64(typed)) == typed
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func incrementOpenAI5hWakeExclusion(excluded *OpenAI5hWakeExclusions, reason string) {
	switch reason {
	case "api_key":
		excluded.APIKey++
	case "non_oauth":
		excluded.NonOAuth++
	case "spark_shadow":
		excluded.SparkShadow++
	case "non_global":
		excluded.NonGlobal++
	case "no_5h_entitlement":
		excluded.No5hEntitlement++
	case "disabled":
		excluded.Disabled++
	case "unschedulable":
		excluded.Unschedulable++
	case "expired":
		excluded.Expired++
	case "rate_limited":
		excluded.RateLimited++
	case "cooling_down":
		excluded.CoolingDown++
	case "missing_identity":
		excluded.MissingIdentity++
	}
}

func openAI5hWakeIdentityParts(account *Account) []string {
	if account == nil {
		return nil
	}
	chatGPTAccountID := strings.TrimSpace(account.GetCredential("chatgpt_account_id"))
	organizationID := strings.TrimSpace(account.GetCredential("organization_id"))
	if chatGPTAccountID == "" && organizationID == "" {
		return nil
	}
	// Only exact typed tuples share a wake item. Workspace and user identifiers
	// are not interchangeable aliases and must never create transitive groups.
	values := []struct {
		name  string
		value string
	}{
		{name: "chatgpt_account_id", value: chatGPTAccountID},
		{name: "organization_id", value: organizationID},
		{name: "chatgpt_user_id", value: strings.TrimSpace(account.GetCredential("chatgpt_user_id"))},
	}
	parts := make([]string, 0, len(values))
	for _, identity := range values {
		if identity.value == "" {
			continue
		}
		parts = append(parts, identity.name+":"+identity.value)
	}
	return parts
}

func buildOpenAI5hWakeQuotaGroups(accounts []*Account) []openAI5hWakeQuotaGroup {
	if len(accounts) == 0 {
		return nil
	}
	type groupBuilder struct {
		accounts []*Account
	}
	builders := make(map[string]*groupBuilder)
	for _, account := range accounts {
		identityParts := openAI5hWakeIdentityParts(account)
		if len(identityParts) == 0 {
			continue
		}
		identity := strings.Join(identityParts, "\x00")
		builder := builders[identity]
		if builder == nil {
			builder = &groupBuilder{}
			builders[identity] = builder
		}
		builder.accounts = append(builder.accounts, account)
	}
	groups := make([]openAI5hWakeQuotaGroup, 0, len(builders))
	for _, builder := range builders {
		sort.Slice(builder.accounts, func(i, j int) bool { return builder.accounts[i].ID < builder.accounts[j].ID })
		identityHash := openAI5hWakeIdentityHash(builder.accounts[0])
		groups = append(groups, openAI5hWakeQuotaGroup{
			identityHash: identityHash,
			accounts:     builder.accounts,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].accounts[0].ID < groups[j].accounts[0].ID
	})
	return groups
}

func (s *OpenAI5hWakeService) runWorker() {
	claimTicker := time.NewTicker(openAI5hWakeClaimPoll)
	cleanupTicker := time.NewTicker(openAI5hWakeCleanupInterval)
	defer claimTicker.Stop()
	defer cleanupTicker.Stop()
	s.cleanupTasks()
	for {
		claimStarted := time.Now()
		now := claimStarted.UTC()
		claimCtx, claimCancel := context.WithTimeout(context.Background(), openAI5hWakeClaimTimeout)
		task, err := s.repo.ClaimTask(claimCtx, s.owner, now, now.Add(openAI5hWakeLeaseDuration))
		claimCancel()
		if err != nil {
			slog.Error("openai_5h_wake_claim_failed", "error", err)
		} else if task != nil {
			// ClaimTask signs the lease with database NOW() so host clock skew
			// cannot steal another worker's lease. The local monitor must therefore
			// use an elapsed-time deadline from the claim attempt, not compare the
			// database's absolute timestamp with this process's wall clock. Starting
			// at claimStarted is conservative by the query round-trip duration.
			setOpenAI5hWakeLocalLeaseDeadline(task, claimStarted)
			s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelInfo, "task_claimed", fmt.Sprintf(
				"processed=%d total=%d", task.ProcessedItems, task.TotalItems,
			))
			s.processTask(task)
			continue
		}
		select {
		case <-s.notify:
		case <-claimTicker.C:
		case <-cleanupTicker.C:
			s.cleanupTasks()
		}
	}
}

func setOpenAI5hWakeLocalLeaseDeadline(task *OpenAI5hWakeTask, claimStarted time.Time) {
	if task == nil {
		return
	}
	localLeaseDeadline := claimStarted.Add(openAI5hWakeLeaseDuration)
	task.LeaseExpiresAt = &localLeaseDeadline
}

func (s *OpenAI5hWakeService) cleanupTasks() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	deleted, err := s.repo.DeleteTerminalBefore(ctx, time.Now().UTC().Add(-openAI5hWakeRetention))
	if err != nil {
		slog.Warn("openai_5h_wake_cleanup_failed", "error", err)
	} else if deleted > 0 {
		slog.Info("openai_5h_wake_cleanup_completed", "deleted", deleted)
	}
}

func (s *OpenAI5hWakeService) processTask(task *OpenAI5hWakeTask) {
	if task == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.runningMu.Lock()
	// Tests and alternate wiring may construct the service as a struct literal
	// instead of going through NewOpenAI5hWakeService. Keep task cancellation
	// safe for that valid zero-value-adjacent construction as well.
	if s.running == nil {
		s.running = make(map[int64]context.CancelFunc)
	}
	s.running[task.ID] = cancel
	s.runningMu.Unlock()
	defer func() {
		cancel()
		s.runningMu.Lock()
		delete(s.running, task.ID)
		s.runningMu.Unlock()
	}()

	var lostLease atomic.Bool
	monitorDone := make(chan struct{})
	monitorStopped := make(chan struct{})
	leaseExpiresAt := time.Now().UTC().Add(openAI5hWakeLeaseDuration)
	if task.LeaseExpiresAt != nil && !task.LeaseExpiresAt.IsZero() {
		leaseExpiresAt = task.LeaseExpiresAt.UTC()
	}
	go func() {
		defer close(monitorStopped)
		s.monitorTask(ctx, cancel, task.ID, leaseExpiresAt, &lostLease, monitorDone)
	}()
	var stopMonitorOnce sync.Once
	stopMonitor := func() {
		stopMonitorOnce.Do(func() {
			close(monitorDone)
			<-monitorStopped
		})
	}
	defer stopMonitor()

	cancelRequested := false
	exhaustedItems, err := s.repo.RecoverTaskItems(ctx, task.ID, s.owner, openAI5hWakeMaxItemAttempts)
	if err != nil {
		if lostLease.Load() {
			slog.Warn("openai_5h_wake_lease_lost", "task_id", task.ID)
			return
		}
		// A local cancellation deliberately cancels the recovery context. Confirm
		// the durable request with a detached context so the task can be finalized
		// immediately instead of waiting for the lease to expire and be reclaimed.
		checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
		var cancelCheckErr error
		cancelRequested, cancelCheckErr = s.repo.IsCancelRequested(checkCtx, task.ID)
		checkCancel()
		if cancelCheckErr != nil {
			s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "cancel_check_failed", "recovery: "+wakeEventErrorMessage(cancelCheckErr))
			slog.Error("openai_5h_wake_recovery_cancel_check_failed", "task_id", task.ID, "error", cancelCheckErr)
			return
		}
		if !cancelRequested {
			s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "item_recovery_failed", wakeEventErrorMessage(err))
			slog.Error("openai_5h_wake_item_recovery_failed", "task_id", task.ID, "error", err)
			return
		}
	} else if exhaustedItems > 0 {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "items_retry_exhausted", fmt.Sprintf(
			"count=%d max_attempts=%d", exhaustedItems, openAI5hWakeMaxItemAttempts,
		))
	}

	if !cancelRequested {
		checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
		cancelRequested, err = s.repo.IsCancelRequested(checkCtx, task.ID)
		checkCancel()
		if err != nil {
			s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "cancel_check_failed", wakeEventErrorMessage(err))
			slog.Error("openai_5h_wake_cancel_check_failed", "task_id", task.ID, "error", err)
			return
		}
		if cancelRequested {
			cancel()
			s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelWarn, "cancel_observed", "")
		}
	}
	if !cancelRequested && task.TotalItems == 0 {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "empty_task", "task contains no quota-pool items and cannot be reported as successful")
	}

	var fatalMu sync.Mutex
	var fatalErr error
	setFatal := func(err error) bool {
		fatalMu.Lock()
		defer fatalMu.Unlock()
		if fatalErr == nil {
			fatalErr = err
			cancel()
			return true
		}
		return false
	}
	var workers sync.WaitGroup
	if !cancelRequested {
		for i := 0; i < openAI5hWakeConcurrency; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for ctx.Err() == nil {
					item, claimErr := s.repo.ClaimNextItem(ctx, task.ID, s.owner)
					if claimErr != nil {
						if ctx.Err() == nil {
							if setFatal(claimErr) {
								s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "item_claim_failed", wakeEventErrorMessage(claimErr))
							}
						}
						return
					}
					if item == nil {
						return
					}
					s.recordTaskEvent(task.ID, &item.ID, OpenAI5hWakeEventLevelInfo, "item_started", fmt.Sprintf(
						"attempt=%d pool=%s members=%d", item.AttemptCount, shortWakeIdentityHash(item.IdentityHash), len(item.MemberAccountIDs),
					))
					itemCtx, itemCancel := context.WithTimeout(ctx, openAI5hWakeItemBudget(item))
					result := s.processItem(itemCtx, item)
					itemCancel()
					// Cancellation can arrive immediately after the upstream has
					// confirmed a new window. Preserve that durable success so the
					// side effect is not reported as an unaccounted cancellation.
					if ctx.Err() != nil && !wakeItemResultIsDurable(result) {
						return
					}
					completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
					completed, completeErr := s.repo.CompleteItem(completeCtx, task.ID, s.owner, result)
					completeCancel()
					if completeErr != nil || !completed {
						if completeErr == nil {
							completeErr = fmt.Errorf("item %d completion lost task lease", item.ID)
						}
						if setFatal(completeErr) {
							s.recordTaskEvent(task.ID, &item.ID, OpenAI5hWakeEventLevelError, "item_complete_failed", wakeEventErrorMessage(completeErr))
						}
						return
					}
					s.recordTaskEvent(task.ID, &item.ID, wakeItemEventLevel(result.Status), wakeItemEventCode(result.Status), wakeItemEventMessage(result))
				}
			}()
		}
	}
	workers.Wait()

	if lostLease.Load() {
		slog.Warn("openai_5h_wake_lease_lost", "task_id", task.ID)
		return
	}
	fatalMu.Lock()
	processErr := fatalErr
	fatalMu.Unlock()
	if processErr != nil {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "task_processing_failed", wakeEventErrorMessage(processErr))
		slog.Error("openai_5h_wake_processing_failed", "task_id", task.ID, "error", processErr)
		return
	}
	finalCheckCtx, finalCheckCancel := context.WithTimeout(context.Background(), 5*time.Second)
	cancelRequested, err = s.repo.IsCancelRequested(finalCheckCtx, task.ID)
	finalCheckCancel()
	if err != nil {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "final_cancel_check_failed", wakeEventErrorMessage(err))
		slog.Error("openai_5h_wake_final_cancel_check_failed", "task_id", task.ID, "error", err)
		return
	}

	// Stop concurrent heartbeats before finalization, then establish a fresh
	// lease generation synchronously. This avoids both an expiry window during
	// finalization and a heartbeat racing the task's transition to terminal.
	stopMonitor()
	if lostLease.Load() {
		slog.Warn("openai_5h_wake_lease_lost", "task_id", task.ID)
		return
	}
	leaseNow := time.Now().UTC()
	leaseRefreshCtx, leaseRefreshCancel := context.WithTimeout(context.Background(), 5*time.Second)
	owned, leaseErr := s.repo.HeartbeatTask(leaseRefreshCtx, task.ID, s.owner, leaseNow, leaseNow.Add(openAI5hWakeLeaseDuration))
	leaseRefreshCancel()
	if leaseErr != nil {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "heartbeat_failed", "finalization: "+wakeEventErrorMessage(leaseErr))
		slog.Error("openai_5h_wake_final_lease_refresh_failed", "task_id", task.ID, "error", leaseErr)
		return
	}
	if !owned {
		lostLease.Store(true)
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "lease_lost", "task lease is no longer owned before finalization")
		slog.Warn("openai_5h_wake_lease_lost", "task_id", task.ID)
		return
	}

	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finalizeCancel()
	finalTask, err := s.repo.FinalizeTask(finalizeCtx, task.ID, s.owner, cancelRequested, time.Now().UTC())
	if err != nil {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "task_finalize_failed", wakeEventErrorMessage(err))
		slog.Error("openai_5h_wake_finalize_failed", "task_id", task.ID, "error", err)
		return
	}
	if finalTask == nil {
		err := errOpenAI5hWakeRepositoryContract
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "task_finalize_failed", err.Error())
		slog.Error("openai_5h_wake_finalize_failed", "task_id", task.ID, "error", err)
		return
	}
	s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelInfo, "task_finished", fmt.Sprintf(
		"status=%s woken=%d skipped_active=%d failed=%d cancelled=%d",
		finalTask.Status, finalTask.WokenCount, finalTask.SkippedActiveCount, finalTask.FailedCount, finalTask.CancelledCount,
	))
	s.recordFinalAudit(finalTask)
	slog.Info("openai_5h_wake_finished",
		"task_id", finalTask.ID,
		"status", finalTask.Status,
		"woken", finalTask.WokenCount,
		"skipped_active", finalTask.SkippedActiveCount,
		"failed", finalTask.FailedCount,
		"cancelled", finalTask.CancelledCount,
		"alignment_span_seconds", finalTask.AlignmentSpanSeconds,
	)
}

func (s *OpenAI5hWakeService) monitorTask(
	ctx context.Context,
	cancel context.CancelFunc,
	taskID int64,
	leaseExpiresAt time.Time,
	lostLease *atomic.Bool,
	done <-chan struct{},
) {
	s.monitorTaskWithTimings(ctx, cancel, taskID, leaseExpiresAt, lostLease, done, defaultOpenAI5hWakeMonitorTimings)
}

func (s *OpenAI5hWakeService) monitorTaskWithTimings(
	ctx context.Context,
	cancel context.CancelFunc,
	taskID int64,
	leaseExpiresAt time.Time,
	lostLease *atomic.Bool,
	done <-chan struct{},
	timings openAI5hWakeMonitorTimings,
) {
	heartbeatTicker := time.NewTicker(timings.heartbeatInterval)
	cancelTicker := time.NewTicker(timings.cancelPollInterval)
	defer heartbeatTicker.Stop()
	defer cancelTicker.Stop()
	leaseDeadline := leaseExpiresAt.UTC()
	if leaseDeadline.IsZero() {
		leaseDeadline = time.Now().UTC().Add(timings.leaseDuration)
	}
	leaseTimer := time.NewTimer(durationUntil(leaseDeadline))
	defer leaseTimer.Stop()
	heartbeatFailureLogged := false
	cancelCheckFailureLogged := false
	loseLease := func(message string) {
		lostLease.Store(true)
		cancel()
		s.recordTaskEvent(taskID, nil, OpenAI5hWakeEventLevelError, "lease_lost", message)
		slog.Error("openai_5h_wake_lease_lost", "task_id", taskID, "reason", message, "lease_expires_at", leaseDeadline)
	}
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-leaseTimer.C:
			loseLease("last confirmed task lease expired")
			return
		case <-heartbeatTicker.C:
			now := time.Now().UTC()
			if !now.Before(leaseDeadline) {
				loseLease("last confirmed task lease expired before heartbeat")
				return
			}
			heartbeatCtx, heartbeatCancel := context.WithTimeout(context.Background(), timeoutBefore(leaseDeadline, timings.heartbeatTimeout))
			leaseUntil := now.Add(timings.leaseDuration)
			owned, err := s.repo.HeartbeatTask(heartbeatCtx, taskID, s.owner, now, leaseUntil)
			heartbeatCancel()
			if err != nil {
				if !heartbeatFailureLogged {
					s.recordTaskEventBefore(leaseDeadline, taskID, nil, OpenAI5hWakeEventLevelWarn, "heartbeat_failed", wakeEventErrorMessage(err))
					heartbeatFailureLogged = true
				}
				slog.Warn("openai_5h_wake_heartbeat_failed", "task_id", taskID, "error", err)
				if !time.Now().UTC().Before(leaseDeadline) {
					loseLease("last confirmed task lease expired after heartbeat failure")
					return
				}
				continue
			}
			heartbeatFailureLogged = false
			if !owned {
				loseLease("task lease is no longer owned")
				return
			}
			// HeartbeatTask extends the lease only if the database still considers
			// the previous lease valid. Its successful CAS is therefore stronger
			// evidence than observing that the response crossed the old local
			// deadline. Reject only a confirmation that arrives after the newly
			// granted lease has itself expired.
			if !time.Now().UTC().Before(leaseUntil) {
				loseLease("heartbeat confirmation arrived after the renewed lease expired")
				return
			}
			leaseDeadline = leaseUntil
			resetTimer(leaseTimer, durationUntil(leaseDeadline))
		case <-cancelTicker.C:
			if !time.Now().UTC().Before(leaseDeadline) {
				loseLease("last confirmed task lease expired before cancellation poll")
				return
			}
			checkCtx, checkCancel := context.WithTimeout(context.Background(), timeoutBefore(leaseDeadline, timings.cancelPollTimeout))
			requested, err := s.repo.IsCancelRequested(checkCtx, taskID)
			checkCancel()
			if err != nil {
				if !cancelCheckFailureLogged {
					s.recordTaskEventBefore(leaseDeadline, taskID, nil, OpenAI5hWakeEventLevelWarn, "cancel_poll_failed", wakeEventErrorMessage(err))
					cancelCheckFailureLogged = true
				}
				slog.Warn("openai_5h_wake_cancel_poll_failed", "task_id", taskID, "error", err)
				if !time.Now().UTC().Before(leaseDeadline) {
					loseLease("last confirmed task lease expired after cancellation poll failure")
					return
				}
				continue
			}
			cancelCheckFailureLogged = false
			if requested {
				cancel()
				s.recordTaskEvent(taskID, nil, OpenAI5hWakeEventLevelWarn, "cancel_observed", "")
				return
			}
		}
	}
}

func durationUntil(deadline time.Time) time.Duration {
	duration := time.Until(deadline)
	if duration <= 0 {
		return time.Nanosecond
	}
	return duration
}

func timeoutBefore(deadline time.Time, maximum time.Duration) time.Duration {
	remaining := durationUntil(deadline)
	if maximum <= 0 || remaining < maximum {
		return remaining
	}
	return maximum
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (s *OpenAI5hWakeService) recordTaskEvent(taskID int64, itemID *int64, level, code, message string) {
	s.recordTaskEventWithTimeout(openAI5hWakeEventTimeout, taskID, itemID, level, code, message)
}

func (s *OpenAI5hWakeService) recordTaskEventBefore(deadline time.Time, taskID int64, itemID *int64, level, code, message string) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return
	}
	timeout := openAI5hWakeEventTimeout
	if remaining < timeout {
		timeout = remaining
	}
	s.recordTaskEventWithTimeout(timeout, taskID, itemID, level, code, message)
}

func (s *OpenAI5hWakeService) recordTaskEventWithTimeout(timeout time.Duration, taskID int64, itemID *int64, level, code, message string) {
	if s == nil || s.repo == nil || taskID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := s.repo.AppendTaskEvent(ctx, OpenAI5hWakeTaskEventParams{
		TaskID:  taskID,
		ItemID:  itemID,
		Level:   level,
		Code:    code,
		Message: truncateWakeEventMessage(message),
	})
	if err != nil {
		slog.Warn("openai_5h_wake_event_write_failed", "task_id", taskID, "event_code", code, "error", err)
	}
}

func truncateWakeEventMessage(message string) string {
	message = strings.TrimSpace(message)
	runes := []rune(message)
	if len(runes) <= openAI5hWakeEventMessageMax {
		return message
	}
	return string(runes[:openAI5hWakeEventMessageMax])
}

func wakeEventErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return truncateWakeEventMessage(logredact.RedactText(err.Error()))
}

func shortWakeIdentityHash(identityHash string) string {
	identityHash = strings.TrimSpace(identityHash)
	if len(identityHash) <= 12 {
		return identityHash
	}
	return identityHash[:12]
}

func wakeItemEventCode(status string) string {
	switch status {
	case OpenAI5hWakeItemStatusWoken:
		return "item_woken"
	case OpenAI5hWakeItemStatusSkippedActive:
		return "item_skipped_active"
	case OpenAI5hWakeItemStatusCancelled:
		return "item_cancelled"
	default:
		return "item_failed"
	}
}

func wakeItemEventLevel(status string) string {
	if status == OpenAI5hWakeItemStatusFailed {
		return OpenAI5hWakeEventLevelError
	}
	if status == OpenAI5hWakeItemStatusCancelled {
		return OpenAI5hWakeEventLevelWarn
	}
	return OpenAI5hWakeEventLevelInfo
}

func wakeItemEventMessage(result OpenAI5hWakeCompleteItemParams) string {
	parts := []string{
		fmt.Sprintf("status=%s", result.Status),
		fmt.Sprintf("attempted_accounts=%d", len(result.AttemptedAccountIDs)),
	}
	if result.SuccessfulAccountID != nil {
		parts = append(parts, fmt.Sprintf("successful_account_id=%d", *result.SuccessfulAccountID))
	}
	if result.ResetAt != nil {
		parts = append(parts, "reset_at="+result.ResetAt.UTC().Format(time.RFC3339))
	}
	if result.ErrorCode != "" {
		parts = append(parts, "error_code="+result.ErrorCode)
	}
	return strings.Join(parts, " ")
}

func openAI5hWakeAttemptMessage(accountID int64, candidate, total int, phase string, status int, contentType, errorCode, diagnostic string) string {
	parts := []string{
		fmt.Sprintf("account_id=%d", accountID),
		fmt.Sprintf("candidate=%d/%d", candidate, total),
		"phase=" + strings.TrimSpace(phase),
	}
	if status > 0 {
		parts = append(parts, fmt.Sprintf("status=%d", status))
	}
	if contentType = truncateWakeDiagnostic(logredact.RedactText(contentType)); contentType != "" {
		parts = append(parts, fmt.Sprintf("content_type=%q", contentType))
	}
	if errorCode = strings.TrimSpace(errorCode); errorCode != "" {
		parts = append(parts, "error_code="+errorCode)
	}
	if diagnostic = truncateWakeDiagnostic(logredact.RedactText(diagnostic)); diagnostic != "" {
		parts = append(parts, fmt.Sprintf("diagnostic=%q", diagnostic))
	}
	return strings.Join(parts, " ")
}

func (s *OpenAI5hWakeService) recordWakeAttemptFailure(
	taskID, itemID, accountID int64,
	candidate, total int,
	eventCode, phase string,
	status int,
	contentType, errorCode, diagnostic string,
) {
	message := openAI5hWakeAttemptMessage(accountID, candidate, total, phase, status, contentType, errorCode, diagnostic)
	eventLevel := OpenAI5hWakeEventLevelError
	if eventCode == "usage_check_failed" {
		eventLevel = OpenAI5hWakeEventLevelWarn
	}
	s.recordTaskEvent(taskID, &itemID, eventLevel, eventCode, message)
	slog.Warn("openai_5h_wake_account_attempt_failed",
		"task_id", taskID,
		"item_id", itemID,
		"account_id", accountID,
		"candidate", candidate,
		"candidate_total", total,
		"phase", phase,
		"status", status,
		"content_type", truncateWakeDiagnostic(logredact.RedactText(contentType)),
		"error_code", errorCode,
		"diagnostic", truncateWakeDiagnostic(logredact.RedactText(diagnostic)),
	)
}

func (s *OpenAI5hWakeService) recordWakeRequestAccepted(
	taskID, itemID, accountID int64,
	candidate, total, status int,
	contentType string,
) {
	message := openAI5hWakeAttemptMessage(accountID, candidate, total, "upstream_request", status, contentType, "", "")
	s.recordTaskEvent(taskID, &itemID, OpenAI5hWakeEventLevelInfo, "wake_request_accepted", message)
	slog.Info("openai_5h_wake_request_accepted",
		"task_id", taskID,
		"item_id", itemID,
		"account_id", accountID,
		"candidate", candidate,
		"candidate_total", total,
		"status", status,
		"content_type", truncateWakeDiagnostic(logredact.RedactText(contentType)),
	)
}

func (s *OpenAI5hWakeService) recordWakeRequestSucceeded(
	taskID, itemID, accountID int64,
	candidate, total int,
	resetAt time.Time,
) {
	message := openAI5hWakeAttemptMessage(accountID, candidate, total, "window_confirmation", 0, "", "", "")
	message += " reset_at=" + resetAt.UTC().Format(time.RFC3339)
	s.recordTaskEvent(taskID, &itemID, OpenAI5hWakeEventLevelInfo, "wake_request_succeeded", message)
	slog.Info("openai_5h_wake_request_succeeded",
		"task_id", taskID,
		"item_id", itemID,
		"account_id", accountID,
		"candidate", candidate,
		"candidate_total", total,
		"reset_at", resetAt.UTC().Format(time.RFC3339),
	)
}

func (s *OpenAI5hWakeService) recordFinalAudit(task *OpenAI5hWakeTask) {
	if s.auditService == nil || task == nil {
		return
	}
	statusCode := http.StatusOK
	if task.Status == OpenAI5hWakeTaskStatusFailed {
		statusCode = http.StatusBadGateway
	}
	s.auditService.Record(&AuditLog{
		CreatedAt:   time.Now().UTC(),
		ActorUserID: task.RequestedByUserID,
		ActorEmail:  task.RequestedByEmail,
		ActorRole:   "admin",
		Action:      AuditActionOpenAI5hWakeComplete,
		Method:      "BACKGROUND",
		Path:        openAI5hWakeAuditPath,
		StatusCode:  statusCode,
		Extra: map[string]any{
			"task_id":                task.ID,
			"status":                 task.Status,
			"woken_count":            task.WokenCount,
			"skipped_active_count":   task.SkippedActiveCount,
			"failed_count":           task.FailedCount,
			"cancelled_count":        task.CancelledCount,
			"alignment_span_seconds": task.AlignmentSpanSeconds,
		},
	})
}

func (s *OpenAI5hWakeService) processItem(ctx context.Context, item *OpenAI5hWakeTaskItem) OpenAI5hWakeCompleteItemParams {
	if item == nil {
		return OpenAI5hWakeCompleteItemParams{
			Status:    OpenAI5hWakeItemStatusFailed,
			ErrorCode: "invalid_item",
		}
	}
	result := OpenAI5hWakeCompleteItemParams{ItemID: item.ID, Status: OpenAI5hWakeItemStatusFailed}
	if s == nil || s.accountRepo == nil {
		result.ErrorCode = wakeErrorCode(ctx, "account_reload_failed")
		return result
	}
	accounts, err := s.accountRepo.GetByIDs(ctx, item.MemberAccountIDs)
	if err != nil {
		result.ErrorCode = wakeErrorCode(ctx, "account_reload_failed")
		return result
	}
	allGroups := buildOpenAI5hWakeQuotaGroups(accounts)
	var currentGroup *openAI5hWakeQuotaGroup
	for i := range allGroups {
		if allGroups[i].identityHash == item.IdentityHash {
			currentGroup = &allGroups[i]
			break
		}
	}
	if currentGroup == nil {
		result.ErrorCode = "identity_changed"
		return result
	}
	now := time.Now().UTC()
	candidates := make([]*Account, 0, len(currentGroup.accounts))
	for _, account := range currentGroup.accounts {
		if classifyOpenAI5hWakeExclusion(account, now) == "" {
			candidates = append(candidates, account)
		}
	}
	if len(candidates) == 0 {
		result.ErrorCode = "no_eligible_account"
		return result
	}
	if source, resetAt, ok := activeWakeSnapshot(candidates, now); ok {
		result.Status = OpenAI5hWakeItemStatusSkippedActive
		result.ResetAt = &resetAt
		successID := source.ID
		result.SuccessfulAccountID = &successID
		return result
	}

	type acceptedWakeRequest struct {
		accountID int64
		candidate int
		total     int
	}
	var acceptedRequest *acceptedWakeRequest
	recordConfirmedRequest := func(resetAt *time.Time) {
		if acceptedRequest == nil || resetAt == nil {
			return
		}
		s.recordWakeRequestSucceeded(
			item.TaskID, item.ID, acceptedRequest.accountID,
			acceptedRequest.candidate, acceptedRequest.total, *resetAt,
		)
	}
	lastErrorCode := "window_unconfirmed"
	for candidateIndex, account := range candidates {
		if ctx.Err() != nil {
			result.ErrorCode = wakeErrorCode(ctx, "cancelled")
			return result
		}
		candidateNumber := candidateIndex + 1
		recordAttemptFailureCode := func(eventCode, phase string, status int, errorCode, diagnostic string, contentTypes ...string) {
			contentType := ""
			if len(contentTypes) > 0 {
				contentType = contentTypes[0]
			}
			s.recordWakeAttemptFailure(
				item.TaskID, item.ID, account.ID,
				candidateNumber, len(candidates),
				eventCode, phase, status, contentType, errorCode, diagnostic,
			)
		}
		recordAttemptFailure := func(phase string, status int, errorCode, diagnostic string, contentTypes ...string) {
			recordAttemptFailureCode("account_attempt_failed", phase, status, errorCode, diagnostic, contentTypes...)
		}
		result.AttemptedAccountIDs = append(result.AttemptedAccountIDs, account.ID)
		s.recordTaskEvent(item.TaskID, &item.ID, OpenAI5hWakeEventLevelInfo, "account_attempt_started", fmt.Sprintf(
			"account_id=%d candidate=%d/%d phase=usage_check", account.ID, candidateNumber, len(candidates),
		))
		attemptCtx, attemptCancel := context.WithTimeout(ctx, openAI5hWakeAccountAttemptTimeout)
		usageUpdates, resetAt, queryErr := s.queryAndPersistGlobalUsage(attemptCtx, account)
		if errors.Is(queryErr, errOpenAI5hWakeIdentityChanged) {
			lastErrorCode = "identity_changed"
			recordAttemptFailure("usage_check", 0, lastErrorCode, wakeEventErrorMessage(queryErr))
			attemptCancel()
			continue
		}
		if errors.Is(queryErr, errOpenAI5hWakeSnapshotPersist) {
			lastErrorCode = "snapshot_persist_failed"
			recordAttemptFailure("usage_check", 0, lastErrorCode, wakeEventErrorMessage(queryErr))
			attemptCancel()
			continue
		}
		if errors.Is(queryErr, errOpenAI5hWakeNoEntitlement) {
			lastErrorCode = "no_5h_entitlement"
			recordAttemptFailureCode(
				"no_5h_entitlement", "usage_check", 0, lastErrorCode,
				"upstream usage response reports no 5h entitlement",
			)
			attemptCancel()
			continue
		}
		if queryErr != nil {
			// A probe failure does not prove that the wake request would be
			// rejected, so retain the existing fallback behavior and still send
			// the request. Persist the probe phase explicitly so an operator can
			// distinguish it from an upstream wake failure.
			lastErrorCode = "usage_check_failed"
			recordAttemptFailureCode(
				"usage_check_failed", "usage_check", 0, lastErrorCode,
				wakeEventErrorMessage(queryErr),
			)
		}
		usedPercent, hasUsedPercent := openAIWakeUsedPercent(usageUpdates)
		if queryErr == nil && resetAt != nil && resetAt.After(time.Now()) && hasUsedPercent && usedPercent > 0 {
			attemptCancel()
			result.Status = OpenAI5hWakeItemStatusSkippedActive
			successID := account.ID
			if acceptedRequest != nil {
				result.Status = OpenAI5hWakeItemStatusWoken
				successID = acceptedRequest.accountID
				recordConfirmedRequest(resetAt)
			}
			result.ResetAt = resetAt
			result.SuccessfulAccountID = &successID
			return result
		}

		s.recordTaskEvent(item.TaskID, &item.ID, OpenAI5hWakeEventLevelInfo, "wake_request_started", fmt.Sprintf(
			"account_id=%d candidate=%d/%d phase=upstream_request", account.ID, candidateNumber, len(candidates),
		))
		wakeCtx, wakeCancel := context.WithTimeout(attemptCtx, openAI5hWakeRequestTimeout)
		wakeResult := s.sendMinimumWakeRequest(wakeCtx, account, item.IdentityHash)
		wakeCancel()
		requestAccount := account
		if wakeResult.requestAccount != nil {
			requestAccount = wakeResult.requestAccount
		}
		wakePhase := strings.TrimSpace(wakeResult.phase)
		if wakePhase == "" {
			wakePhase = "upstream_request"
		}
		wakeError := strings.TrimSpace(wakeResult.errorCode)
		if wakeError == "" && (wakeResult.statusCode < http.StatusOK || wakeResult.statusCode >= http.StatusMultipleChoices) {
			wakeError = wakeHTTPErrorCode(wakeResult.statusCode)
		}
		if wakeResult.statusCode >= http.StatusOK && wakeResult.statusCode < http.StatusMultipleChoices {
			acceptedRequest = &acceptedWakeRequest{accountID: account.ID, candidate: candidateNumber, total: len(candidates)}
			s.recordWakeRequestAccepted(
				item.TaskID, item.ID, account.ID,
				candidateNumber, len(candidates), wakeResult.statusCode, wakeResult.contentType,
			)
		}
		recordRequestFailure := func() {
			s.recordWakeAttemptFailure(
				item.TaskID, item.ID, account.ID,
				candidateNumber, len(candidates),
				"wake_request_failed", wakePhase, wakeResult.statusCode, wakeResult.contentType, wakeError, wakeResult.diagnostic,
			)
		}
		if wakeResult.err != nil {
			// A 2xx response can still carry a failed/incomplete SSE stream. The
			// request may nevertheless have activated an already-shared window, so
			// perform a fresh usage check before treating it as a hard failure.
			if wakeResult.statusCode >= http.StatusOK && wakeResult.statusCode < http.StatusMultipleChoices {
				confirmedReset, confirmErr := s.confirmWakeWindow(
					attemptCtx, item.TaskID, item.ID, requestAccount, candidateNumber, len(candidates),
				)
				if confirmErr == nil && confirmedReset != nil && confirmedReset.After(time.Now().UTC()) {
					// The pre-request probe found no active window and the upstream
					// accepted this request with a 2xx status. An incomplete stream
					// does not undo the request side effect, so report this pool as
					// woken once the follow-up usage check confirms the new window.
					recordConfirmedRequest(confirmedReset)
					result.Status = OpenAI5hWakeItemStatusWoken
					result.ResetAt = confirmedReset
					successID := account.ID
					result.SuccessfulAccountID = &successID
					attemptCancel()
					return result
				}
				if errors.Is(confirmErr, errOpenAI5hWakeIdentityChanged) {
					lastErrorCode = "identity_changed"
					recordRequestFailure()
					recordAttemptFailure("post_usage_check", wakeResult.statusCode, lastErrorCode, wakeEventErrorMessage(confirmErr), wakeResult.contentType)
					attemptCancel()
					continue
				}
				if errors.Is(confirmErr, errOpenAI5hWakeSnapshotPersist) {
					lastErrorCode = "snapshot_persist_failed"
					recordRequestFailure()
					recordAttemptFailure("post_usage_check", wakeResult.statusCode, lastErrorCode, wakeEventErrorMessage(confirmErr), wakeResult.contentType)
					attemptCancel()
					continue
				}
				if errors.Is(confirmErr, errOpenAI5hWakeNoEntitlement) {
					lastErrorCode = "no_5h_entitlement"
					recordRequestFailure()
					recordAttemptFailure("post_usage_check", wakeResult.statusCode, lastErrorCode, lastErrorCode, wakeResult.contentType)
					attemptCancel()
					continue
				}
			}
			if wakeError == "" {
				wakeError = wakeErrorCode(attemptCtx, "request_failed")
			}
			lastErrorCode = wakeError
			recordRequestFailure()
			recordAttemptFailure(wakePhase, wakeResult.statusCode, lastErrorCode, wakeResult.diagnostic, wakeResult.contentType)
			attemptCancel()
			continue
		}
		if wakeResult.statusCode == http.StatusUnauthorized {
			lastErrorCode = "unauthorized"
			wakeError = lastErrorCode
			recordRequestFailure()
			recordAttemptFailure(wakePhase, wakeResult.statusCode, lastErrorCode, wakeResult.diagnostic, wakeResult.contentType)
			attemptCancel()
			continue
		}
		if wakeResult.statusCode == http.StatusForbidden {
			lastErrorCode = "forbidden"
			wakeError = lastErrorCode
			recordRequestFailure()
			recordAttemptFailure(wakePhase, wakeResult.statusCode, lastErrorCode, wakeResult.diagnostic, wakeResult.contentType)
			attemptCancel()
			continue
		}
		if wakeResult.statusCode < 200 || wakeResult.statusCode >= 300 {
			if wakeResult.statusCode == http.StatusTooManyRequests && len(wakeResult.updates) > 0 {
				resetAt, persistErr := s.persistWakeSnapshotAndResolve(attemptCtx, requestAccount, wakeResult.updates)
				if persistErr != nil {
					lastErrorCode = "snapshot_persist_failed"
					if errors.Is(persistErr, errOpenAI5hWakeIdentityChanged) {
						lastErrorCode = "identity_changed"
					}
					recordAttemptFailure("snapshot_persist", wakeResult.statusCode, lastErrorCode, wakeEventErrorMessage(persistErr), wakeResult.contentType)
					attemptCancel()
					continue
				}
				if resetAt != nil && resetAt.After(time.Now().UTC()) {
					result.Status = OpenAI5hWakeItemStatusSkippedActive
					result.ResetAt = resetAt
					successID := account.ID
					if acceptedRequest != nil {
						result.Status = OpenAI5hWakeItemStatusWoken
						successID = acceptedRequest.accountID
						recordConfirmedRequest(resetAt)
					}
					result.SuccessfulAccountID = &successID
					attemptCancel()
					return result
				}
			}
			lastErrorCode = wakeHTTPErrorCode(wakeResult.statusCode)
			wakeError = lastErrorCode
			recordRequestFailure()
			recordAttemptFailure(wakePhase, wakeResult.statusCode, lastErrorCode, wakeResult.diagnostic, wakeResult.contentType)
			attemptCancel()
			continue
		}
		if len(wakeResult.updates) > 0 {
			resetAt, persistErr := s.persistWakeSnapshotAndResolve(attemptCtx, requestAccount, wakeResult.updates)
			if persistErr != nil {
				lastErrorCode = "snapshot_persist_failed"
				if errors.Is(persistErr, errOpenAI5hWakeIdentityChanged) {
					lastErrorCode = "identity_changed"
				}
				recordAttemptFailure("snapshot_persist", wakeResult.statusCode, lastErrorCode, wakeEventErrorMessage(persistErr), wakeResult.contentType)
				attemptCancel()
				continue
			}
			if resetAt != nil && resetAt.After(time.Now().UTC()) {
				recordConfirmedRequest(resetAt)
				result.Status = OpenAI5hWakeItemStatusWoken
				result.ResetAt = resetAt
				successID := account.ID
				result.SuccessfulAccountID = &successID
				attemptCancel()
				return result
			}
		}
		resetAt, queryErr = s.confirmWakeWindow(
			attemptCtx, item.TaskID, item.ID, requestAccount, candidateNumber, len(candidates),
		)
		if queryErr != nil {
			lastErrorCode = "post_usage_check_failed"
			if errors.Is(queryErr, errOpenAI5hWakeIdentityChanged) {
				lastErrorCode = "identity_changed"
			} else if errors.Is(queryErr, errOpenAI5hWakeSnapshotPersist) {
				lastErrorCode = "snapshot_persist_failed"
			} else if errors.Is(queryErr, errOpenAI5hWakeNoEntitlement) {
				lastErrorCode = "no_5h_entitlement"
			}
			recordAttemptFailure("post_usage_check", wakeResult.statusCode, lastErrorCode, wakeEventErrorMessage(queryErr), wakeResult.contentType)
			attemptCancel()
			continue
		}
		if resetAt == nil || !resetAt.After(time.Now()) {
			lastErrorCode = "window_unconfirmed"
			recordAttemptFailure("post_usage_check", wakeResult.statusCode, lastErrorCode, "wake request returned success but no active 5h window was observed", wakeResult.contentType)
			attemptCancel()
			continue
		}
		result.Status = OpenAI5hWakeItemStatusWoken
		result.ResetAt = resetAt
		recordConfirmedRequest(resetAt)
		successID := account.ID
		result.SuccessfulAccountID = &successID
		attemptCancel()
		return result
	}
	result.ErrorCode = wakeErrorCode(ctx, lastErrorCode)
	return result
}

func openAI5hWakeItemBudget(item *OpenAI5hWakeTaskItem) time.Duration {
	if item == nil || len(item.MemberAccountIDs) == 0 {
		return openAI5hWakeAccountAttemptTimeout
	}
	budget := time.Duration(len(item.MemberAccountIDs)) * openAI5hWakeAccountAttemptTimeout
	if budget > openAI5hWakeItemTimeout {
		return openAI5hWakeItemTimeout
	}
	return budget
}

func wakeItemResultIsDurable(result OpenAI5hWakeCompleteItemParams) bool {
	return result.Status == OpenAI5hWakeItemStatusWoken || result.Status == OpenAI5hWakeItemStatusSkippedActive
}

func wakeErrorCode(ctx context.Context, fallback string) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	return fallback
}

func (s *OpenAI5hWakeService) queryAndPersistGlobalUsage(ctx context.Context, account *Account) (map[string]any, *time.Time, error) {
	if s.quotaService == nil {
		return nil, nil, fmt.Errorf("quota service unavailable")
	}
	usage, err := s.quotaService.QueryUsageLightweight(ctx, account.ID)
	if err != nil {
		return nil, nil, err
	}
	if openAI5hWakeUsageHasNoEntitlement(usage) {
		// Do not persist a possibly misleading window from a plan that cannot
		// start the requested 5h bucket. The caller will try another member of
		// the shared pool and will never send a wake request for this account.
		return nil, nil, errOpenAI5hWakeNoEntitlement
	}
	// FetchedAt is intentionally exposed as Unix seconds for the API, but it is
	// too coarse to order concurrent probe responses. Use the local receipt time
	// for the persisted snapshot sequence and reset countdown instead.
	now := time.Now().UTC()
	updates := buildCodexGlobalWindowExtraUpdates(usage, now)
	if len(updates) == 0 {
		return nil, nil, nil
	}
	resetAt, err := s.persistWakeSnapshotAndResolve(ctx, account, updates)
	if err != nil {
		// Preserve both the persistence category and a CAS identity conflict so
		// processItem can safely try another member of the same quota pool.
		return updates, resetAt, fmt.Errorf("%w: %w", errOpenAI5hWakeSnapshotPersist, err)
	}
	return updates, resetAt, nil
}

func openAI5hWakeUsageHasNoEntitlement(usage *OpenAIQuotaUsage) bool {
	if usage == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(usage.PlanType)) {
	case "free", "abnormal":
		return true
	default:
		return false
	}
}

func (s *OpenAI5hWakeService) confirmWakeWindow(
	ctx context.Context,
	taskID, itemID int64,
	account *Account,
	candidate, total int,
) (*time.Time, error) {
	return s.confirmWakeWindowWithTimings(
		ctx, taskID, itemID, account, candidate, total,
		defaultOpenAI5hWakeConfirmationTimings,
	)
}

func (s *OpenAI5hWakeService) confirmWakeWindowWithTimings(
	ctx context.Context,
	taskID, itemID int64,
	account *Account,
	candidate, total int,
	timings openAI5hWakeConfirmationTimings,
) (*time.Time, error) {
	if timings.timeout <= 0 {
		return nil, nil
	}
	if timings.delay <= 0 {
		timings.delay = time.Millisecond
	}
	if timings.maxDelay < timings.delay {
		timings.maxDelay = timings.delay
	}
	confirmCtx, cancel := context.WithTimeout(ctx, timings.timeout)
	defer cancel()

	delay := timings.delay
	for attempt := 1; ; attempt++ {
		_, resetAt, err := s.queryAndPersistGlobalUsage(confirmCtx, account)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if confirmCtx.Err() != nil {
				return nil, nil
			}
			return nil, err
		}
		if resetAt != nil && resetAt.After(time.Now().UTC()) {
			return resetAt, nil
		}

		deadline, ok := confirmCtx.Deadline()
		if !ok {
			return nil, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil
		}
		wait := delay
		if wait > remaining {
			wait = remaining
		}
		s.recordTaskEvent(taskID, &itemID, OpenAI5hWakeEventLevelInfo, "wake_confirmation_pending", fmt.Sprintf(
			"account_id=%d candidate=%d/%d confirmation_attempt=%d retry_in_milliseconds=%d",
			account.ID, candidate, total, attempt, wait.Milliseconds(),
		))

		timer := time.NewTimer(wait)
		select {
		case <-confirmCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, nil
		case <-timer.C:
		}
		if delay < timings.maxDelay {
			delay *= 2
			if delay > timings.maxDelay {
				delay = timings.maxDelay
			}
		}
	}
}

type openAI5hWakeHTTPResult struct {
	statusCode  int
	contentType string
	phase       string
	diagnostic  string
	updates     map[string]any
	resetAt     *time.Time
	// requestAccount is the durable account snapshot paired with the exact
	// authentication token and request headers sent upstream. Any response
	// snapshot must use this identity for its repository CAS.
	requestAccount *Account
	errorCode      string
	err            error
}

func buildOpenAI5hWakePayload(account *Account) ([]byte, error) {
	model := openaipkg.DefaultTestModel
	if account != nil {
		if mapped := strings.TrimSpace(account.GetMappedModel(model)); mapped != "" {
			model = normalizeOpenAIModelForUpstream(account, mapped)
		}
	}
	payload := map[string]any{
		"model":        model,
		"instructions": openAI5hWakeInstructions,
		"input": []map[string]any{{
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": openAI5hWakeInput,
			}},
		}},
		"stream": true,
		"store":  false,
	}
	return json.Marshal(payload)
}

func (s *OpenAI5hWakeService) sendMinimumWakeRequest(ctx context.Context, account *Account, expectedIdentityHash ...string) openAI5hWakeHTTPResult {
	if s.httpUpstream == nil {
		return openAI5hWakeHTTPResult{
			phase:      "setup",
			errorCode:  "upstream_unavailable",
			diagnostic: "HTTP upstream unavailable",
			err:        fmt.Errorf("HTTP upstream unavailable"),
		}
	}
	if account == nil {
		err := fmt.Errorf("account unavailable")
		return openAI5hWakeHTTPResult{
			phase:      "setup",
			errorCode:  "account_unavailable",
			diagnostic: wakeSafeErrorMessage(err),
			err:        err,
		}
	}

	requestAccount := account
	authToken := ""
	var err error
	if !account.IsOpenAIAgentIdentity() {
		authToken, requestAccount, err = acquireOpenAIAuthenticatedAccountSnapshot(ctx, s.accountRepo, s.tokenProvider, account)
		if err != nil {
			return openAI5hWakeHTTPResult{
				phase:      "token",
				errorCode:  "token_unavailable",
				diagnostic: wakeSafeErrorMessage(err),
				err:        err,
			}
		}
	}
	if len(expectedIdentityHash) > 0 && strings.TrimSpace(expectedIdentityHash[0]) != "" {
		expected := strings.TrimSpace(expectedIdentityHash[0])
		if openAI5hWakeIdentityHash(requestAccount) != expected {
			err = errOpenAI5hWakeIdentityChanged
			return openAI5hWakeHTTPResult{
				phase:          "identity_check",
				errorCode:      "identity_changed",
				diagnostic:     wakeSafeErrorMessage(err),
				requestAccount: snapshotOAuthRefreshAccount(requestAccount),
				err:            err,
			}
		}
		if reason := classifyOpenAI5hWakeExclusion(requestAccount, time.Now().UTC()); reason != "" {
			errorCode := "account_ineligible"
			if reason == "no_5h_entitlement" {
				errorCode = reason
			}
			err = fmt.Errorf("account became ineligible before wake request: %s", reason)
			return openAI5hWakeHTTPResult{
				phase:          "eligibility_check",
				errorCode:      errorCode,
				diagnostic:     wakeSafeErrorMessage(err),
				requestAccount: snapshotOAuthRefreshAccount(requestAccount),
				err:            err,
			}
		}
	}

	payload, err := buildOpenAI5hWakePayload(requestAccount)
	if err != nil {
		return openAI5hWakeHTTPResult{
			phase:      "request_build",
			errorCode:  "payload_build_failed",
			diagnostic: wakeSafeErrorMessage(err),
			err:        err,
		}
	}
	for recovered := false; ; {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexAPIURL, bytes.NewReader(payload))
		if requestErr != nil {
			return openAI5hWakeHTTPResult{
				phase:      "request_build",
				errorCode:  "request_build_failed",
				diagnostic: wakeSafeErrorMessage(requestErr),
				err:        requestErr,
			}
		}
		req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
		req.Host = "chatgpt.com"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("Originator", "codex_cli_rs")
		if customUA := strings.TrimSpace(requestAccount.GetOpenAIUserAgent()); customUA != "" {
			req.Header.Set("User-Agent", customUA)
		} else {
			req.Header.Set("User-Agent", codexCLIUserAgent)
		}
		if requestAccount.IsOpenAIAgentIdentity() {
			authHeaders, authErr := buildAgentIdentityAuthenticationHeaders(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityMu, requestAccount)
			if authErr != nil {
				return openAI5hWakeHTTPResult{
					phase:      "auth_recovery",
					errorCode:  "auth_build_failed",
					diagnostic: wakeSafeErrorMessage(authErr),
					err:        authErr,
				}
			}
			for key, values := range authHeaders {
				for _, value := range values {
					req.Header.Add(key, value)
				}
			}
		} else {
			req.Header.Set("Authorization", "Bearer "+authToken)
		}
		setOpenAIQuotaAccountHeaders(req.Header, requestAccount)
		enforceCodexIdentityHeaders(req.Header)
		requestAccount.ApplyHeaderOverrides(req.Header)

		proxyURL := ""
		if requestAccount.ProxyID != nil && requestAccount.Proxy != nil {
			proxyURL = requestAccount.Proxy.URL()
		}
		var resp *http.Response
		var sendErr error
		if s.tlsProfiles != nil {
			resp, sendErr = s.httpUpstream.DoWithTLS(req, proxyURL, requestAccount.ID, requestAccount.Concurrency, s.tlsProfiles.ResolveTLSProfile(requestAccount))
		} else {
			resp, sendErr = s.httpUpstream.DoWithTLS(req, proxyURL, requestAccount.ID, requestAccount.Concurrency, nil)
		}
		if sendErr != nil {
			return openAI5hWakeHTTPResult{
				phase:      "upstream_request",
				errorCode:  wakeErrorCode(ctx, "request_failed"),
				diagnostic: wakeSafeErrorMessage(sendErr),
				err:        sendErr,
			}
		}
		if resp == nil {
			err = fmt.Errorf("upstream returned no response")
			return openAI5hWakeHTTPResult{
				phase:      "upstream_request",
				errorCode:  "empty_response",
				diagnostic: wakeSafeErrorMessage(err),
				err:        err,
			}
		}
		result := openAI5hWakeHTTPResult{
			statusCode:     resp.StatusCode,
			contentType:    strings.TrimSpace(resp.Header.Get("Content-Type")),
			phase:          "upstream_request",
			requestAccount: snapshotOAuthRefreshAccount(requestAccount),
		}
		if updates, updateErr := extractOpenAICodexProbeUpdates(resp); updateErr == nil {
			result.updates = updates
			if resetAt, ok := openAIWakeResetAt(updates); ok {
				result.resetAt = &resetAt
			}
		}
		body, bodyErr := readOpenAI5hWakeResponseBody(resp)
		if bodyErr != nil {
			result.phase = "response_body"
			result.errorCode = "response_body_read_failed"
			result.diagnostic = wakeSafeErrorMessage(bodyErr)
			result.err = bodyErr
			return result
		}
		if requestAccount.IsOpenAIAgentIdentity() && !recovered && resp.StatusCode >= 400 && result.resetAt == nil {
			if isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, body) {
				recovered = true
				expectedTaskID := requestAccount.GetCredential("task_id")
				if recoverErr := ensureAgentIdentityTaskForAccount(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityMu, requestAccount, expectedTaskID); recoverErr != nil {
					result.phase = "auth_recovery"
					result.errorCode = "auth_recovery_failed"
					result.diagnostic = wakeSafeErrorMessage(recoverErr)
					result.err = recoverErr
					return result
				}
				continue
			}
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			if streamErr := validateOpenAI5hWakeResponseBody(body); streamErr != nil {
				result.phase = "response_stream"
				result.errorCode = "response_stream_incomplete"
				result.diagnostic = wakeDiagnosticFromBody(body)
				if result.diagnostic == "" {
					result.diagnostic = wakeSafeErrorMessage(streamErr)
				}
				result.err = streamErr
				return result
			}
			return result
		}
		result.errorCode = wakeHTTPErrorCode(resp.StatusCode)
		result.diagnostic = wakeDiagnosticFromBody(body)
		if result.diagnostic == "" {
			result.diagnostic = http.StatusText(resp.StatusCode)
		}
		return result
	}
}

func readOpenAI5hWakeResponseBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, openAI5hWakeResponseBodyMax+1))
	if err != nil {
		return nil, err
	}
	if len(body) > openAI5hWakeResponseBodyMax {
		return nil, fmt.Errorf("response body exceeds %d bytes", openAI5hWakeResponseBodyMax)
	}
	return body, nil
}

func wakeSafeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return truncateWakeDiagnostic(logredact.RedactText(err.Error()))
}

// wakeDiagnosticFromBody extracts only a small, allow-listed set of fields
// from an upstream JSON/SSE error. The complete response is intentionally
// never persisted because it can contain credentials, request metadata, or
// proxy-generated HTML.
func wakeDiagnosticFromBody(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	seen := make(map[string]struct{})
	add := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		value = truncateWakeDiagnostic(logredact.RedactText(value))
		key := label + "=" + value
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		parts = append(parts, key)
	}
	var visit func(any, int)
	visit = func(value any, depth int) {
		if depth > 4 {
			return
		}
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		for _, field := range []struct {
			key   string
			label string
		}{
			{key: "code", label: "upstream_error_code"},
			{key: "type", label: "upstream_error_type"},
			{key: "message", label: "upstream_error_message"},
			{key: "detail", label: "upstream_error_detail"},
		} {
			if raw, exists := object[field.key]; exists {
				if text, ok := raw.(string); ok {
					add(field.label, text)
				}
			}
		}
		for _, key := range []string{"error", "response", "data"} {
			if nested, exists := object[key]; exists {
				visit(nested, depth+1)
			}
		}
	}

	var direct any
	if json.Unmarshal(bytes.TrimSpace(body), &direct) == nil {
		visit(direct, 0)
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event any
		if json.Unmarshal([]byte(payload), &event) == nil {
			visit(event, 0)
		}
	}
	return truncateWakeDiagnostic(strings.Join(parts, " "))
}

func truncateWakeDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= openAI5hWakeDiagnosticMax {
		return value
	}
	return string(runes[:openAI5hWakeDiagnosticMax])
}

func validateOpenAI5hWakeResponseBody(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return errors.New("empty response stream")
	}

	// Accept a buffered JSON response as well as the normal SSE form. Some
	// upstream compatibility layers honor stream=true but buffer one object.
	var direct map[string]any
	if json.Unmarshal(trimmed, &direct) == nil {
		if openAI5hWakeResponseEventSucceeded(direct) {
			return nil
		}
		if openAI5hWakeResponseEventFailed(direct) {
			return errors.New("upstream response reported failure")
		}
	}

	completed := false
	eventName := ""
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			eventName = ""
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(payload), &event) != nil {
			eventName = ""
			continue
		}
		if _, ok := event["type"]; !ok && eventName != "" {
			event["type"] = eventName
		}
		if openAI5hWakeResponseEventFailed(event) {
			return errors.New("upstream response reported failure")
		}
		if openAI5hWakeResponseEventSucceeded(event) {
			completed = true
		}
		eventName = ""
	}
	if completed {
		return nil
	}
	return errors.New("response.completed event not observed")
}

func openAI5hWakeResponseEventSucceeded(event map[string]any) bool {
	if event == nil {
		return false
	}
	typeName, _ := event["type"].(string)
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	if typeName != "response.completed" && typeName != "response.done" {
		// Compatibility layers sometimes buffer a terminal response object and
		// omit the SSE event type.  A positive terminal status is sufficient in
		// that shape; an absent status remains unconfirmed.
		if typeName != "" {
			return false
		}
		switch openAI5hWakeResponseStatus(event) {
		case "completed", "done", "success":
			return true
		default:
			return false
		}
	}
	switch openAI5hWakeResponseStatus(event) {
	case "failed", "incomplete", "cancelled", "canceled", "error":
		return false
	}
	return true
}

func openAI5hWakeResponseEventFailed(event map[string]any) bool {
	if event == nil {
		return false
	}
	typeName, _ := event["type"].(string)
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	switch typeName {
	case "error", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	}
	switch openAI5hWakeResponseStatus(event) {
	case "failed", "incomplete", "cancelled", "canceled", "error":
		return true
	}
	return false
}

func openAI5hWakeResponseStatus(event map[string]any) string {
	if event == nil {
		return ""
	}
	if status, ok := event["status"].(string); ok {
		return strings.ToLower(strings.TrimSpace(status))
	}
	if response, ok := event["response"].(map[string]any); ok {
		if status, ok := response["status"].(string); ok {
			return strings.ToLower(strings.TrimSpace(status))
		}
	}
	return ""
}

func wakeHTTPErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusTooManyRequests:
		return "rate_limited_unconfirmed"
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "upstream_unavailable"
	default:
		return "upstream_http_error"
	}
}

func activeWakeSnapshot(accounts []*Account, now time.Time) (*Account, time.Time, bool) {
	var source *Account
	var resetAt time.Time
	for _, account := range accounts {
		if !hasTrustedOpenAI5hWakeSnapshot(account) {
			continue
		}
		usedPercent, observed := openAIWakeUsedPercent(account.Extra)
		if !observed || usedPercent <= 0 {
			continue
		}
		candidate, ok := openAIWakeResetAt(account.Extra)
		if !ok || !candidate.After(now) {
			continue
		}
		if source == nil || candidate.Before(resetAt) {
			source = account
			resetAt = candidate
		}
	}
	return source, resetAt, source != nil
}

func openAIWakeUsedPercent(extra map[string]any) (float64, bool) {
	if extra == nil {
		return 0, false
	}
	value, ok := extra["codex_5h_used_percent"]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func openAI5hWakeIdentityHash(account *Account) string {
	parts := openAI5hWakeIdentityParts(account)
	if len(parts) == 0 {
		return ""
	}
	hash := sha256.Sum256([]byte("openai-5h-quota-pool\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:])
}

func hasTrustedOpenAI5hWakeSnapshot(account *Account) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	stored, ok := account.Extra[openAI5hWakeSnapshotIdentityKey].(string)
	return ok && strings.TrimSpace(stored) != "" &&
		strings.EqualFold(strings.TrimSpace(stored), openAI5hWakeIdentityHash(account))
}

func openAIWakeResetAt(extra map[string]any) (time.Time, bool) {
	if extra == nil {
		return time.Time{}, false
	}
	value, ok := extra["codex_5h_reset_at"]
	if !ok || value == nil {
		return time.Time{}, false
	}
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), !typed.IsZero()
	case *time.Time:
		if typed != nil && !typed.IsZero() {
			return typed.UTC(), true
		}
	case string:
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(typed))
		return parsed.UTC(), err == nil
	case json.Number:
		unix, err := typed.Int64()
		return time.Unix(unix, 0).UTC(), err == nil && unix > 0
	case float64:
		if typed > 0 {
			return time.Unix(int64(typed), 0).UTC(), true
		}
	case int64:
		if typed > 0 {
			return time.Unix(typed, 0).UTC(), true
		}
	case int:
		if typed > 0 {
			return time.Unix(int64(typed), 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func (s *OpenAI5hWakeService) persistWakeSnapshot(ctx context.Context, account *Account, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	if s == nil || s.accountRepo == nil {
		return fmt.Errorf("account repository unavailable")
	}
	if account == nil || account.ID <= 0 {
		return fmt.Errorf("invalid account")
	}
	identityHash := openAI5hWakeIdentityHash(account)
	if identityHash == "" {
		return fmt.Errorf("account %d has no wake identity", account.ID)
	}
	annotated := make(map[string]any, len(updates)+1)
	for key, value := range updates {
		annotated[key] = value
	}
	// A 7d-only or otherwise partial response must invalidate an older 5h
	// marker. Otherwise a stale codex_5h_reset_at left in the row could become
	// trusted merely because a later request returned unrelated quota fields.
	if _, has5hReset := openAIWakeResetAt(updates); has5hReset {
		annotated[openAI5hWakeSnapshotIdentityKey] = identityHash
	} else {
		annotated[openAI5hWakeSnapshotIdentityKey] = nil
	}
	if err := persistOpenAICodexSnapshotForAccount(ctx, s.accountRepo, account, annotated); err != nil {
		slog.Warn("openai_5h_wake_snapshot_persist_failed", "account_id", account.ID, "error", err)
		return err
	}
	return nil
}

func (s *OpenAI5hWakeService) persistWakeSnapshotAndResolve(ctx context.Context, account *Account, updates map[string]any) (*time.Time, error) {
	if err := s.persistWakeSnapshot(ctx, account, updates); err != nil {
		return nil, err
	}
	if s == nil || s.accountRepo == nil || account == nil {
		return nil, fmt.Errorf("account repository unavailable")
	}

	current, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil {
		return nil, fmt.Errorf("verify persisted OpenAI 5h wake snapshot: %w", err)
	}
	if current == nil || current.ID != account.ID ||
		openAI5hWakeIdentityFingerprintFor(current) != openAI5hWakeIdentityFingerprintFor(account) {
		return nil, errOpenAI5hWakeIdentityChanged
	}

	incomingObservedAt, incomingHasObservation := openAICodexSnapshotObservedAtFromExtra(updates)
	storedObservedAt, storedHasObservation := openAICodexSnapshotObservedAtFromExtra(current.Extra)
	if incomingHasObservation && (!storedHasObservation || storedObservedAt < incomingObservedAt) {
		return nil, fmt.Errorf("persisted OpenAI 5h wake snapshot is older than the submitted observation")
	}
	if !hasTrustedOpenAI5hWakeSnapshot(current) {
		return nil, nil
	}
	resetAt, ok := openAIWakeResetAt(current.Extra)
	if !ok || !resetAt.After(time.Now().UTC()) {
		return nil, nil
	}
	return &resetAt, nil
}
