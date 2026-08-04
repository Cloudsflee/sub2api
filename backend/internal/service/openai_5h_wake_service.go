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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	openaipkg "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

const (
	openAI5hWakeConcurrency     = 8
	openAI5hWakeItemTimeout     = 40 * time.Second
	openAI5hWakeLeaseDuration   = 60 * time.Second
	openAI5hWakeHeartbeat       = 20 * time.Second
	openAI5hWakeCancelPoll      = time.Second
	openAI5hWakeClaimPoll       = 2 * time.Second
	openAI5hWakeRetention       = 30 * 24 * time.Hour
	openAI5hWakeCleanupInterval = 24 * time.Hour
	openAI5hWakeEventTimeout    = 3 * time.Second
	openAI5hWakeEventMessageMax = 2000
	openAI5hWakeInstructions    = "Reply with OK."
	openAI5hWakeInput           = "hi"
	openAI5hWakeAuditPath       = "/api/v1/admin/accounts/openai-5h-wake/tasks/:id"
)

type openAI5hWakeQuotaGroup struct {
	identityHash string
	accounts     []*Account
}

type openAI5hWakePlan struct {
	preview OpenAI5hWakePreview
	groups  []openAI5hWakeQuotaGroup
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
	task, err := s.repo.RequestCancel(ctx, taskID, time.Now().UTC())
	if err != nil {
		return nil, err
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
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
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
	for identity, builder := range builders {
		sort.Slice(builder.accounts, func(i, j int) bool { return builder.accounts[i].ID < builder.accounts[j].ID })
		hash := sha256.Sum256([]byte("openai-5h-quota-pool\x00" + identity))
		groups = append(groups, openAI5hWakeQuotaGroup{
			identityHash: hex.EncodeToString(hash[:]),
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
		now := time.Now().UTC()
		task, err := s.repo.ClaimTask(context.Background(), s.owner, now, now.Add(openAI5hWakeLeaseDuration))
		if err != nil {
			slog.Error("openai_5h_wake_claim_failed", "error", err)
		} else if task != nil {
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
	s.running[task.ID] = cancel
	s.runningMu.Unlock()
	defer func() {
		cancel()
		s.runningMu.Lock()
		delete(s.running, task.ID)
		s.runningMu.Unlock()
	}()

	if err := s.repo.ResetRunningItems(ctx, task.ID, s.owner); err != nil {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "task_resume_failed", wakeEventErrorMessage(err))
		slog.Error("openai_5h_wake_resume_failed", "task_id", task.ID, "error", err)
		return
	}
	var lostLease atomic.Bool
	monitorDone := make(chan struct{})
	go s.monitorTask(ctx, cancel, task.ID, &lostLease, monitorDone)

	cancelRequested, err := s.repo.IsCancelRequested(ctx, task.ID)
	if err != nil {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "cancel_check_failed", wakeEventErrorMessage(err))
		slog.Error("openai_5h_wake_cancel_check_failed", "task_id", task.ID, "error", err)
		close(monitorDone)
		return
	}
	if cancelRequested {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelWarn, "cancel_observed", "")
		cancel()
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
					itemCtx, itemCancel := context.WithTimeout(ctx, openAI5hWakeItemTimeout)
					result := s.processItem(itemCtx, item)
					itemCancel()
					if ctx.Err() != nil {
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
	close(monitorDone)

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
	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finalizeCancel()
	cancelRequested, err = s.repo.IsCancelRequested(finalizeCtx, task.ID)
	if err != nil {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "final_cancel_check_failed", wakeEventErrorMessage(err))
		slog.Error("openai_5h_wake_final_cancel_check_failed", "task_id", task.ID, "error", err)
		return
	}
	finalTask, err := s.repo.FinalizeTask(finalizeCtx, task.ID, s.owner, cancelRequested, time.Now().UTC())
	if err != nil {
		s.recordTaskEvent(task.ID, nil, OpenAI5hWakeEventLevelError, "task_finalize_failed", wakeEventErrorMessage(err))
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

func (s *OpenAI5hWakeService) monitorTask(ctx context.Context, cancel context.CancelFunc, taskID int64, lostLease *atomic.Bool, done <-chan struct{}) {
	heartbeatTicker := time.NewTicker(openAI5hWakeHeartbeat)
	cancelTicker := time.NewTicker(openAI5hWakeCancelPoll)
	defer heartbeatTicker.Stop()
	defer cancelTicker.Stop()
	heartbeatFailureLogged := false
	cancelCheckFailureLogged := false
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case now := <-heartbeatTicker.C:
			heartbeatCtx, heartbeatCancel := context.WithTimeout(context.Background(), 5*time.Second)
			owned, err := s.repo.HeartbeatTask(heartbeatCtx, taskID, s.owner, now.UTC(), now.UTC().Add(openAI5hWakeLeaseDuration))
			heartbeatCancel()
			if err != nil {
				if !heartbeatFailureLogged {
					s.recordTaskEvent(taskID, nil, OpenAI5hWakeEventLevelWarn, "heartbeat_failed", wakeEventErrorMessage(err))
					heartbeatFailureLogged = true
				}
				slog.Warn("openai_5h_wake_heartbeat_failed", "task_id", taskID, "error", err)
				continue
			}
			heartbeatFailureLogged = false
			if !owned {
				s.recordTaskEvent(taskID, nil, OpenAI5hWakeEventLevelError, "lease_lost", "")
				lostLease.Store(true)
				cancel()
				return
			}
		case <-cancelTicker.C:
			checkCtx, checkCancel := context.WithTimeout(context.Background(), 3*time.Second)
			requested, err := s.repo.IsCancelRequested(checkCtx, taskID)
			checkCancel()
			if err != nil {
				if !cancelCheckFailureLogged {
					s.recordTaskEvent(taskID, nil, OpenAI5hWakeEventLevelWarn, "cancel_poll_failed", wakeEventErrorMessage(err))
					cancelCheckFailureLogged = true
				}
				continue
			}
			cancelCheckFailureLogged = false
			if requested {
				s.recordTaskEvent(taskID, nil, OpenAI5hWakeEventLevelWarn, "cancel_observed", "")
				cancel()
				return
			}
		}
	}
}

func (s *OpenAI5hWakeService) recordTaskEvent(taskID int64, itemID *int64, level, code, message string) {
	if s == nil || s.repo == nil || taskID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), openAI5hWakeEventTimeout)
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
	return truncateWakeEventMessage(err.Error())
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
	result := OpenAI5hWakeCompleteItemParams{ItemID: item.ID, Status: OpenAI5hWakeItemStatusFailed}
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

	requestSent := false
	lastErrorCode := "window_unconfirmed"
	for _, account := range candidates {
		if ctx.Err() != nil {
			result.ErrorCode = wakeErrorCode(ctx, "cancelled")
			return result
		}
		result.AttemptedAccountIDs = append(result.AttemptedAccountIDs, account.ID)
		if _, resetAt, queryErr := s.queryAndPersistGlobalUsage(ctx, account); queryErr == nil && resetAt != nil && resetAt.After(time.Now()) {
			result.Status = OpenAI5hWakeItemStatusSkippedActive
			if requestSent {
				result.Status = OpenAI5hWakeItemStatusWoken
			}
			result.ResetAt = resetAt
			successID := account.ID
			result.SuccessfulAccountID = &successID
			return result
		}

		requestSent = true
		wakeResult := s.sendMinimumWakeRequest(ctx, account)
		if wakeResult.err != nil {
			lastErrorCode = wakeResult.errorCode
			continue
		}
		if len(wakeResult.updates) > 0 {
			s.persistWakeSnapshot(ctx, account.ID, wakeResult.updates)
		}
		if wakeResult.resetAt != nil && wakeResult.resetAt.After(time.Now()) {
			if wakeResult.statusCode == http.StatusTooManyRequests {
				result.Status = OpenAI5hWakeItemStatusSkippedActive
			} else if wakeResult.statusCode >= 200 && wakeResult.statusCode < 300 {
				result.Status = OpenAI5hWakeItemStatusWoken
			} else {
				lastErrorCode = wakeHTTPErrorCode(wakeResult.statusCode)
				continue
			}
			result.ResetAt = wakeResult.resetAt
			successID := account.ID
			result.SuccessfulAccountID = &successID
			return result
		}
		if wakeResult.statusCode == http.StatusUnauthorized {
			lastErrorCode = "unauthorized"
			continue
		}
		if wakeResult.statusCode == http.StatusForbidden {
			lastErrorCode = "forbidden"
			continue
		}
		if wakeResult.statusCode < 200 || wakeResult.statusCode >= 300 {
			lastErrorCode = wakeHTTPErrorCode(wakeResult.statusCode)
			continue
		}
		_, resetAt, queryErr := s.queryAndPersistGlobalUsage(ctx, account)
		if queryErr != nil {
			lastErrorCode = "post_usage_check_failed"
			continue
		}
		if resetAt == nil || !resetAt.After(time.Now()) {
			lastErrorCode = "window_unconfirmed"
			continue
		}
		result.Status = OpenAI5hWakeItemStatusWoken
		result.ResetAt = resetAt
		successID := account.ID
		result.SuccessfulAccountID = &successID
		return result
	}
	result.ErrorCode = wakeErrorCode(ctx, lastErrorCode)
	return result
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
	now := time.Now().UTC()
	if usage != nil && usage.FetchedAt > 0 {
		now = time.Unix(usage.FetchedAt, 0).UTC()
	}
	updates := buildCodexGlobalWindowExtraUpdates(usage, now)
	if len(updates) == 0 {
		return nil, nil, nil
	}
	s.persistWakeSnapshot(ctx, account.ID, updates)
	resetAt, ok := openAIWakeResetAt(updates)
	if !ok {
		return updates, nil, nil
	}
	return updates, &resetAt, nil
}

type openAI5hWakeHTTPResult struct {
	statusCode int
	updates    map[string]any
	resetAt    *time.Time
	errorCode  string
	err        error
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

func (s *OpenAI5hWakeService) sendMinimumWakeRequest(ctx context.Context, account *Account) openAI5hWakeHTTPResult {
	if s.httpUpstream == nil {
		return openAI5hWakeHTTPResult{errorCode: "upstream_unavailable", err: fmt.Errorf("HTTP upstream unavailable")}
	}
	payload, err := buildOpenAI5hWakePayload(account)
	if err != nil {
		return openAI5hWakeHTTPResult{errorCode: "payload_build_failed", err: err}
	}
	for recovered := false; ; {
		authToken := ""
		if !account.IsOpenAIAgentIdentity() {
			if s.tokenProvider == nil {
				return openAI5hWakeHTTPResult{errorCode: "token_unavailable", err: fmt.Errorf("token provider unavailable")}
			}
			authToken, err = s.tokenProvider.GetAccessToken(ctx, account)
			if err != nil || strings.TrimSpace(authToken) == "" {
				return openAI5hWakeHTTPResult{errorCode: "token_unavailable", err: fmt.Errorf("access token unavailable")}
			}
		}
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexAPIURL, bytes.NewReader(payload))
		if requestErr != nil {
			return openAI5hWakeHTTPResult{errorCode: "request_build_failed", err: requestErr}
		}
		req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
		req.Host = "chatgpt.com"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("Originator", "codex_cli_rs")
		if customUA := strings.TrimSpace(account.GetOpenAIUserAgent()); customUA != "" {
			req.Header.Set("User-Agent", customUA)
		} else {
			req.Header.Set("User-Agent", codexCLIUserAgent)
		}
		if account.IsOpenAIAgentIdentity() {
			authHeaders, authErr := buildAgentIdentityAuthenticationHeaders(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityMu, account)
			if authErr != nil {
				return openAI5hWakeHTTPResult{errorCode: "auth_build_failed", err: authErr}
			}
			for key, values := range authHeaders {
				for _, value := range values {
					req.Header.Add(key, value)
				}
			}
		} else {
			req.Header.Set("Authorization", "Bearer "+authToken)
		}
		setOpenAIChatGPTAccountHeaders(req.Header, account)
		enforceCodexIdentityHeaders(req.Header)
		account.ApplyHeaderOverrides(req.Header)

		proxyURL := ""
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		var resp *http.Response
		var sendErr error
		if s.tlsProfiles != nil {
			resp, sendErr = s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsProfiles.ResolveTLSProfile(account))
		} else {
			resp, sendErr = s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, nil)
		}
		if sendErr != nil {
			return openAI5hWakeHTTPResult{errorCode: wakeErrorCode(ctx, "request_failed"), err: sendErr}
		}
		if resp == nil {
			return openAI5hWakeHTTPResult{errorCode: "empty_response", err: fmt.Errorf("upstream returned no response")}
		}
		result := openAI5hWakeHTTPResult{statusCode: resp.StatusCode}
		if updates, updateErr := extractOpenAICodexProbeUpdates(resp); updateErr == nil {
			result.updates = updates
			if resetAt, ok := openAIWakeResetAt(updates); ok {
				result.resetAt = &resetAt
			}
		}
		if account.IsOpenAIAgentIdentity() && !recovered && resp != nil && resp.Body != nil && resp.StatusCode >= 400 && result.resetAt == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			if isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, body) {
				recovered = true
				expectedTaskID := account.GetCredential("task_id")
				if recoverErr := ensureAgentIdentityTaskForAccount(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityMu, account, expectedTaskID); recoverErr != nil {
					return openAI5hWakeHTTPResult{errorCode: "auth_recovery_failed", err: recoverErr}
				}
				continue
			}
			return result
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return result
	}
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

func (s *OpenAI5hWakeService) persistWakeSnapshot(ctx context.Context, accountID int64, updates map[string]any) {
	if accountID <= 0 || len(updates) == 0 || s.accountRepo == nil {
		return
	}
	if err := s.accountRepo.UpdateExtra(ctx, accountID, updates); err != nil {
		slog.Warn("openai_5h_wake_snapshot_persist_failed", "account_id", accountID, "error", err)
	}
}
