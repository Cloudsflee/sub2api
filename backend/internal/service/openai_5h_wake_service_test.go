package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openaipkg "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type openAI5hWakeAccountRepoStub struct {
	AccountRepository
	mu                 sync.Mutex
	listed             []Account
	accounts           map[int64]*Account
	updates            map[int64][]map[string]any
	updateErrByID      map[int64]error
	snapshotCASOK      *bool
	snapshotCASOKByID  map[int64]bool
	snapshotCASResults []bool
	snapshotCASCalls   int
	afterUpdate        func(int64, map[string]any)
}

type openAI5hWakeSupersededSnapshotRepo struct {
	*openAI5hWakeAccountRepoStub
	onSupersededUpdate func()
}

func (r *openAI5hWakeSupersededSnapshotRepo) UpdateOpenAICodexSnapshot(
	context.Context,
	int64,
	*Account,
	map[string]any,
	map[string]any,
) (bool, error) {
	// The SQL repository reports identity-match success even when its monotonic
	// guard discards an older managed snapshot. Keep the authoritative row
	// unchanged to reproduce that production behavior.
	if r.onSupersededUpdate != nil {
		r.onSupersededUpdate()
	}
	return true, nil
}

func (r *openAI5hWakeAccountRepoStub) UpdateOpenAICodexSnapshot(
	ctx context.Context,
	id int64,
	_ *Account,
	ordinaryUpdates map[string]any,
	managedUpdates map[string]any,
) (bool, error) {
	r.mu.Lock()
	callIndex := r.snapshotCASCalls
	r.snapshotCASCalls++
	var sequencedResult *bool
	if callIndex < len(r.snapshotCASResults) {
		result := r.snapshotCASResults[callIndex]
		sequencedResult = &result
	}
	r.mu.Unlock()
	if sequencedResult != nil && !*sequencedResult {
		return false, nil
	}
	if allowed, ok := r.snapshotCASOKByID[id]; ok && !allowed {
		return false, nil
	}
	if r.snapshotCASOK != nil && !*r.snapshotCASOK {
		return false, nil
	}
	updates := make(map[string]any, len(ordinaryUpdates)+len(managedUpdates))
	for key, value := range ordinaryUpdates {
		updates[key] = value
	}
	for key, value := range managedUpdates {
		updates[key] = value
	}
	if err := r.UpdateExtra(ctx, id, updates); err != nil {
		return false, err
	}
	return true, nil
}

func (r *openAI5hWakeAccountRepoStub) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	if platform != PlatformOpenAI {
		return nil, nil
	}
	active := make([]Account, 0, len(r.listed))
	for _, account := range r.listed {
		if account.Status == StatusActive {
			active = append(active, account)
		}
	}
	return active, nil
}

func (r *openAI5hWakeAccountRepoStub) ListByPlatformAllStatuses(_ context.Context, platform string) ([]Account, error) {
	if platform != PlatformOpenAI {
		return nil, nil
	}
	return append([]Account(nil), r.listed...), nil
}

func (r *openAI5hWakeAccountRepoStub) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	result := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if account := r.accounts[id]; account != nil {
			result = append(result, account)
		}
	}
	return result, nil
}

func (r *openAI5hWakeAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	if account := r.accounts[id]; account != nil {
		return account, nil
	}
	return nil, errors.New("account not found")
}

func (r *openAI5hWakeAccountRepoStub) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	if err := r.updateErrByID[id]; err != nil {
		r.mu.Unlock()
		return err
	}
	if r.updates == nil {
		r.updates = make(map[int64][]map[string]any)
	}
	copied := make(map[string]any, len(updates))
	for key, value := range updates {
		copied[key] = value
	}
	r.updates[id] = append(r.updates[id], copied)
	if account := r.accounts[id]; account != nil {
		if account.Extra == nil {
			account.Extra = make(map[string]any)
		}
		for key, value := range copied {
			account.Extra[key] = value
		}
	}
	afterUpdate := r.afterUpdate
	r.mu.Unlock()
	if afterUpdate != nil {
		afterUpdate(id, copied)
	}
	return nil
}

type openAI5hWakeStubResponse struct {
	status  int
	headers http.Header
	body    string
	err     error
	block   bool
}

type openAI5hWakeCapturedRequest struct {
	body        []byte
	headers     http.Header
	proxyURL    string
	accountID   int64
	concurrency int
	profile     HTTPUpstreamProfile
}

type openAI5hWakeHTTPStub struct {
	mu        sync.Mutex
	responses []openAI5hWakeStubResponse
	requests  []openAI5hWakeCapturedRequest
}

type openAI5hWakeWorkerRepo struct {
	OpenAI5hWakeTaskRepository
	mu              sync.Mutex
	task            *OpenAI5hWakeTask
	items           []*OpenAI5hWakeTaskItem
	events          []*OpenAI5hWakeTaskEvent
	completeErr     error
	cancelRequested bool
	cancelErr       error
	recoverFn       func(context.Context, int64, string, int) (int, error)
	heartbeatFn     func(context.Context, int64, string, time.Time, time.Time) (bool, error)
	appendEventFn   func(OpenAI5hWakeTaskEventParams)
	finalizeCalls   int
	createCalls     int
}

type nilTaskOpenAI5hWakeRepo struct {
	openAI5hWakeWorkerRepo
}

func (*nilTaskOpenAI5hWakeRepo) CreateOrGetActive(context.Context, OpenAI5hWakeCreateParams) (*OpenAI5hWakeTask, bool, error) {
	return nil, false, nil
}

func (r *openAI5hWakeWorkerRepo) CreateOrGetActive(_ context.Context, params OpenAI5hWakeCreateParams) (*OpenAI5hWakeTask, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	if r.task != nil {
		copyTask := *r.task
		return &copyTask, false, nil
	}
	if len(params.Items) == 0 {
		return nil, false, ErrOpenAI5hWakeNoEligiblePools
	}
	r.task = &OpenAI5hWakeTask{ID: 1, Status: OpenAI5hWakeTaskStatusPending}
	copyTask := *r.task
	return &copyTask, true, nil
}

func (r *openAI5hWakeWorkerRepo) AppendTaskEvent(_ context.Context, params OpenAI5hWakeTaskEventParams) error {
	if r.appendEventFn != nil {
		r.appendEventFn(params)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	event := &OpenAI5hWakeTaskEvent{
		ID: int64(len(r.events) + 1), TaskID: params.TaskID, ItemID: params.ItemID,
		Level: params.Level, Code: params.Code, Message: params.Message, CreatedAt: time.Now().UTC(),
	}
	r.events = append(r.events, event)
	return nil
}

func (r *openAI5hWakeWorkerRepo) ListTaskEvents(_ context.Context, _ int64, _, _ int) ([]*OpenAI5hWakeTaskEvent, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := append([]*OpenAI5hWakeTaskEvent(nil), r.events...)
	return result, int64(len(result)), nil
}

func (r *openAI5hWakeWorkerRepo) RecoverTaskItems(ctx context.Context, taskID int64, owner string, maxAttempts int) (int, error) {
	if r.recoverFn != nil {
		return r.recoverFn(ctx, taskID, owner, maxAttempts)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, item := range r.items {
		if item.Status == OpenAI5hWakeItemStatusRunning {
			item.Status = OpenAI5hWakeItemStatusPending
		}
		if r.cancelRequested {
			continue
		}
		if item.Status != OpenAI5hWakeItemStatusPending || item.AttemptCount < maxAttempts {
			continue
		}
		item.Status = OpenAI5hWakeItemStatusFailed
		item.ErrorCode = "worker_retry_exhausted"
		count++
	}
	r.task.ProcessedItems = 0
	r.task.WokenCount = 0
	r.task.SkippedActiveCount = 0
	r.task.FailedCount = 0
	r.task.CancelledCount = 0
	for _, item := range r.items {
		switch item.Status {
		case OpenAI5hWakeItemStatusWoken:
			r.task.ProcessedItems++
			r.task.WokenCount++
		case OpenAI5hWakeItemStatusSkippedActive:
			r.task.ProcessedItems++
			r.task.SkippedActiveCount++
		case OpenAI5hWakeItemStatusFailed:
			r.task.ProcessedItems++
			r.task.FailedCount++
		case OpenAI5hWakeItemStatusCancelled:
			r.task.ProcessedItems++
			r.task.CancelledCount++
		}
	}
	return count, nil
}

func (r *openAI5hWakeWorkerRepo) ClaimNextItem(_ context.Context, _ int64, _ string) (*OpenAI5hWakeTaskItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelRequested {
		return nil, nil
	}
	for _, item := range r.items {
		if item.Status == OpenAI5hWakeItemStatusPending {
			item.Status = OpenAI5hWakeItemStatusRunning
			item.AttemptCount++
			return item, nil
		}
	}
	return nil, nil
}

func (r *openAI5hWakeWorkerRepo) CompleteItem(_ context.Context, _ int64, _ string, params OpenAI5hWakeCompleteItemParams) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.completeErr != nil {
		return false, r.completeErr
	}
	for _, item := range r.items {
		if item.ID != params.ItemID || item.Status != OpenAI5hWakeItemStatusRunning {
			continue
		}
		item.Status = params.Status
		r.task.ProcessedItems++
		switch params.Status {
		case OpenAI5hWakeItemStatusWoken:
			r.task.WokenCount++
		case OpenAI5hWakeItemStatusSkippedActive:
			r.task.SkippedActiveCount++
		case OpenAI5hWakeItemStatusFailed:
			r.task.FailedCount++
		}
		return true, nil
	}
	return false, nil
}

func (r *openAI5hWakeWorkerRepo) HeartbeatTask(ctx context.Context, taskID int64, owner string, now, leaseUntil time.Time) (bool, error) {
	if r.heartbeatFn != nil {
		return r.heartbeatFn(ctx, taskID, owner, now, leaseUntil)
	}
	return true, nil
}

func (r *openAI5hWakeWorkerRepo) IsCancelRequested(context.Context, int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelErr != nil {
		return false, r.cancelErr
	}
	return r.cancelRequested, nil
}

func (r *openAI5hWakeWorkerRepo) RequestCancel(_ context.Context, _ int64, now time.Time) (*OpenAI5hWakeTask, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelRequested || (r.task.Status != OpenAI5hWakeTaskStatusPending && r.task.Status != OpenAI5hWakeTaskStatusRunning) {
		copyTask := *r.task
		return &copyTask, false, nil
	}
	r.cancelRequested = true
	r.task.CancelRequestedAt = &now
	copyTask := *r.task
	return &copyTask, true, nil
}

func (r *openAI5hWakeWorkerRepo) FinalizeTask(_ context.Context, _ int64, _ string, cancelled bool, now time.Time) (*OpenAI5hWakeTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finalizeCalls++
	if cancelled {
		for _, item := range r.items {
			if item.Status == OpenAI5hWakeItemStatusPending || item.Status == OpenAI5hWakeItemStatusRunning {
				item.Status = OpenAI5hWakeItemStatusCancelled
				r.task.CancelledCount++
				r.task.ProcessedItems++
			}
		}
		r.task.Status = OpenAI5hWakeTaskStatusCancelled
	} else if r.task.TotalItems == 0 {
		r.task.Status = OpenAI5hWakeTaskStatusFailed
	} else if r.task.FailedCount > 0 {
		if r.task.WokenCount+r.task.SkippedActiveCount > 0 {
			r.task.Status = OpenAI5hWakeTaskStatusPartialSucceeded
		} else {
			r.task.Status = OpenAI5hWakeTaskStatusFailed
		}
	} else {
		r.task.Status = OpenAI5hWakeTaskStatusSucceeded
	}
	r.task.FinishedAt = &now
	copyTask := *r.task
	return &copyTask, nil
}

func TestOpenAI5hWakeWorkerMarksLegacyEmptyTaskFailedWithDiagnostic(t *testing.T) {
	now := time.Now().UTC()
	repo := &openAI5hWakeWorkerRepo{task: &OpenAI5hWakeTask{
		ID: 19, Status: OpenAI5hWakeTaskStatusRunning, TotalItems: 0,
		LeaseExpiresAt: ptrTime(now.Add(time.Minute)),
	}}
	wake := NewOpenAI5hWakeService(repo, &openAI5hWakeAccountRepoStub{}, nil, nil, nil, nil, nil, nil)

	wake.processTask(repo.task)

	require.Equal(t, OpenAI5hWakeTaskStatusFailed, repo.task.Status)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	codes := make([]string, 0, len(repo.events))
	for _, event := range repo.events {
		codes = append(codes, event.Code)
	}
	require.Contains(t, codes, "empty_task")
	require.Contains(t, codes, "task_finished")
}

func TestOpenAI5hWakeProcessTaskInitializesCancellationMapForLiteralService(t *testing.T) {
	now := time.Now().UTC()
	repo := &openAI5hWakeWorkerRepo{task: &OpenAI5hWakeTask{
		ID: 20, Status: OpenAI5hWakeTaskStatusRunning, TotalItems: 0,
		LeaseExpiresAt: ptrTime(now.Add(time.Minute)),
	}}
	wake := NewOpenAI5hWakeService(repo, &openAI5hWakeAccountRepoStub{}, nil, nil, nil, nil, nil, nil)
	wake.running = nil

	require.NotPanics(t, func() { wake.processTask(repo.task) })
	require.Equal(t, OpenAI5hWakeTaskStatusFailed, repo.task.Status)
}

func TestOpenAI5hWakeProcessItemRejectsNilItemWithoutPanicking(t *testing.T) {
	wake := &OpenAI5hWakeService{}

	result := wake.processItem(context.Background(), nil)

	require.Equal(t, OpenAI5hWakeCompleteItemParams{
		Status:    OpenAI5hWakeItemStatusFailed,
		ErrorCode: "invalid_item",
	}, result)
}

func TestOpenAI5hWakeProcessItemHandlesMissingAccountRepository(t *testing.T) {
	wake := &OpenAI5hWakeService{}
	var result OpenAI5hWakeCompleteItemParams
	require.NotPanics(t, func() {
		result = wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
			ID: 21, MemberAccountIDs: []int64{1},
		})
	})
	require.Equal(t, OpenAI5hWakeCompleteItemParams{
		ItemID:    21,
		Status:    OpenAI5hWakeItemStatusFailed,
		ErrorCode: "account_reload_failed",
	}, result)
}

type openAI5hWakeObservedUpstream struct {
	delay        time.Duration
	block        bool
	started      chan struct{}
	active       atomic.Int32
	maxActive    atomic.Int32
	cancelled    atomic.Int32
	requestCount atomic.Int32
}

func (s *openAI5hWakeObservedUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, errors.New("unexpected HTTPUpstream.Do call")
}

func (s *openAI5hWakeObservedUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.requestCount.Add(1)
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		maximum := s.maxActive.Load()
		if active <= maximum || s.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.block {
		<-req.Context().Done()
		s.cancelled.Add(1)
		return nil, req.Context().Err()
	}
	select {
	case <-time.After(s.delay):
	case <-req.Context().Done():
		s.cancelled.Add(1)
		return nil, req.Context().Err()
	}
	response := openAI5hWakeHeaders(http.StatusOK)
	return &http.Response{
		StatusCode: response.status,
		Header:     response.headers,
		Body:       io.NopCloser(strings.NewReader(openAI5hWakeResponseBody(response))),
	}, nil
}

func (s *openAI5hWakeHTTPStub) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, errors.New("unexpected HTTPUpstream.Do call")
}

func (s *openAI5hWakeHTTPStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	s.mu.Lock()
	s.requests = append(s.requests, openAI5hWakeCapturedRequest{
		body:        body,
		headers:     req.Header.Clone(),
		proxyURL:    proxyURL,
		accountID:   accountID,
		concurrency: concurrency,
		profile:     HTTPUpstreamProfileFromContext(req.Context()),
	})
	if len(s.responses) == 0 {
		s.mu.Unlock()
		return nil, errors.New("missing stub response")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	s.mu.Unlock()
	if response.block {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	if response.err != nil {
		return nil, response.err
	}
	return &http.Response{
		StatusCode: response.status,
		Header:     response.headers,
		Body:       io.NopCloser(strings.NewReader(openAI5hWakeResponseBody(response))),
	}, nil
}

func newOpenAI5hWakeAccount(id int64, identity string) *Account {
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

func openAI5hWakeHeaders(status int) openAI5hWakeStubResponse {
	headers := make(http.Header)
	headers.Set("x-codex-primary-used-percent", "0")
	headers.Set("x-codex-primary-reset-after-seconds", "18000")
	headers.Set("x-codex-primary-window-minutes", "300")
	return openAI5hWakeStubResponse{status: status, headers: headers}
}

func openAI5hWakeResponseBody(response openAI5hWakeStubResponse) string {
	if response.body != "" {
		return response.body
	}
	if response.status >= http.StatusOK && response.status < http.StatusMultipleChoices {
		return "data: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n"
	}
	return ""
}

func TestOpenAI5hWakeBuildPlanFiltersAndKeepsTypedIdentitiesSeparate(t *testing.T) {
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	parentID := int64(1)

	sharedA := *newOpenAI5hWakeAccount(1, "shared-pool")
	sharedB := *newOpenAI5hWakeAccount(2, "")
	sharedB.Credentials["organization_id"] = "shared-pool"
	active := *newOpenAI5hWakeAccount(3, "active-pool")
	active.Extra["codex_5h_reset_at"] = future.Format(time.RFC3339)
	active.Extra["codex_5h_used_percent"] = float64(1)
	active.Extra[openAI5hWakeSnapshotIdentityKey] = openAI5hWakeIdentityHash(&active)
	apiKey := *newOpenAI5hWakeAccount(4, "api-key")
	apiKey.Type = AccountTypeAPIKey
	nonOAuth := *newOpenAI5hWakeAccount(5, "non-oauth")
	nonOAuth.Type = AccountTypeUpstream
	shadow := *newOpenAI5hWakeAccount(6, "shadow")
	shadow.ParentAccountID = &parentID
	shadow.QuotaDimension = QuotaDimensionSpark
	nonGlobal := *newOpenAI5hWakeAccount(7, "non-global")
	nonGlobal.QuotaDimension = QuotaDimensionSpark
	disabled := *newOpenAI5hWakeAccount(8, "disabled")
	disabled.Status = StatusDisabled
	disabled.Credentials["plan_type"] = "free"
	unschedulable := *newOpenAI5hWakeAccount(9, "unschedulable")
	unschedulable.Schedulable = false
	expiredAccount := *newOpenAI5hWakeAccount(10, "expired")
	expiredAccount.AutoPauseOnExpired = true
	expiredAccount.ExpiresAt = &expired
	rateLimited := *newOpenAI5hWakeAccount(11, "limited")
	rateLimited.RateLimitResetAt = &future
	cooling := *newOpenAI5hWakeAccount(12, "cooling")
	cooling.TempUnschedulableUntil = &future
	missingIdentity := *newOpenAI5hWakeAccount(13, "")
	freePlan := *newOpenAI5hWakeAccount(14, "free-plan")
	freePlan.Credentials["plan_type"] = "free"
	no5hWindow := *newOpenAI5hWakeAccount(15, "no-5h-window")
	no5hWindow.Credentials["plan_type"] = "plus"
	no5hWindow.Extra["codex_5h_window_minutes"] = float64(0)

	repo := &openAI5hWakeAccountRepoStub{listed: []Account{
		sharedA, sharedB, active, apiKey, nonOAuth, shadow, nonGlobal, disabled,
		unschedulable, expiredAccount, rateLimited, cooling, missingIdentity, freePlan, no5hWindow,
	}}
	service := &OpenAI5hWakeService{accountRepo: repo}

	plan, err := service.buildPlan(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, 15, plan.preview.TotalOpenAIAccounts)
	require.Equal(t, 3, plan.preview.EligibleAccounts)
	require.Equal(t, 3, plan.preview.UniqueQuotaPools)
	require.Equal(t, 1, plan.preview.ActiveWindows)
	require.Equal(t, 2, plan.preview.EstimatedRequests)
	require.Equal(t, OpenAI5hWakeExclusions{
		APIKey: 1, NonOAuth: 1, SparkShadow: 1, NonGlobal: 1, Disabled: 1,
		No5hEntitlement: 2, Unschedulable: 1, Expired: 1, RateLimited: 1, CoolingDown: 1, MissingIdentity: 1,
	}, plan.preview.Excluded)
	require.Equal(t, int64(1), plan.groups[0].accounts[0].ID)
	require.Equal(t, int64(2), plan.groups[1].accounts[0].ID)
	require.Len(t, plan.groups[0].identityHash, 64)
	require.NotContains(t, plan.groups[0].identityHash, "shared-pool")
}

func TestOpenAI5hWakeKnownEntitlementClassification(t *testing.T) {
	tests := []struct {
		name          string
		planType      string
		windowMinutes any
		wantExcluded  bool
	}{
		{name: "explicit free plan", planType: "free", wantExcluded: true},
		{name: "explicit abnormal plan", planType: "ABNORMAL", wantExcluded: true},
		{name: "paid plan with explicit zero window", planType: "plus", windowMinutes: json.Number("0"), wantExcluded: true},
		{name: "paid 5h window", planType: "k12", windowMinutes: float64(300)},
		{name: "unknown plan and window remains probeable"},
		{name: "negative window is malformed rather than negative entitlement", planType: "plus", windowMinutes: int64(-1)},
		{name: "malformed window remains probeable", planType: "plus", windowMinutes: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := newOpenAI5hWakeAccount(1, "pool")
			if tt.planType != "" {
				account.Credentials["plan_type"] = tt.planType
			}
			if tt.windowMinutes != nil {
				account.Extra["codex_5h_window_minutes"] = tt.windowMinutes
			}
			require.Equal(t, tt.wantExcluded, openAI5hWakeHasKnownNoEntitlement(account))
		})
	}
}

func TestOpenAI5hWakeCreateTaskRejectsEmptyPlan(t *testing.T) {
	taskRepo := &openAI5hWakeWorkerRepo{}
	wake := &OpenAI5hWakeService{
		repo:        taskRepo,
		accountRepo: &openAI5hWakeAccountRepoStub{},
	}

	task, created, err := wake.CreateTask(context.Background(), nil, "admin@example.com")

	require.Error(t, err)
	require.ErrorContains(t, err, "no eligible quota pools")
	require.Nil(t, task)
	require.False(t, created)
	require.Equal(t, 1, taskRepo.createCalls)
}

func TestOpenAI5hWakeCreateTaskReturnsActiveTaskForEmptyPlan(t *testing.T) {
	taskRepo := &openAI5hWakeWorkerRepo{task: &OpenAI5hWakeTask{
		ID: 42, Status: OpenAI5hWakeTaskStatusRunning,
	}}
	wake := &OpenAI5hWakeService{
		repo:        taskRepo,
		accountRepo: &openAI5hWakeAccountRepoStub{},
		notify:      make(chan struct{}, 1),
	}

	task, created, err := wake.CreateTask(context.Background(), nil, "admin@example.com")

	require.NoError(t, err)
	require.False(t, created)
	require.NotNil(t, task)
	require.Equal(t, int64(42), task.ID)
	require.Equal(t, 1, taskRepo.createCalls)
}

func TestOpenAI5hWakeCreateTaskRejectsNilRepositoryTask(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "nil-task")
	wake := &OpenAI5hWakeService{
		repo:        &nilTaskOpenAI5hWakeRepo{},
		accountRepo: &openAI5hWakeAccountRepoStub{listed: []Account{*account}},
	}

	task, created, err := wake.CreateTask(context.Background(), nil, "admin@example.com")

	require.ErrorIs(t, err, errOpenAI5hWakeRepositoryContract)
	require.Nil(t, task)
	require.False(t, created)
}

func TestOpenAI5hWakeQuotaGroupsRequireExactTypedIdentity(t *testing.T) {
	tests := []struct {
		name   string
		first  map[string]string
		second map[string]string
	}{
		{
			name:   "same organization with different chatgpt accounts",
			first:  map[string]string{"chatgpt_account_id": "account-a", "organization_id": "shared-org"},
			second: map[string]string{"chatgpt_account_id": "account-b", "organization_id": "shared-org"},
		},
		{
			name:   "same chatgpt account with different organizations",
			first:  map[string]string{"chatgpt_account_id": "shared-account", "organization_id": "org-a"},
			second: map[string]string{"chatgpt_account_id": "shared-account", "organization_id": "org-b"},
		},
		{
			name:   "equal values in different fields",
			first:  map[string]string{"chatgpt_account_id": "cross-field"},
			second: map[string]string{"organization_id": "cross-field"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := newOpenAI5hWakeAccount(1, "")
			second := newOpenAI5hWakeAccount(2, "")
			for key, value := range tt.first {
				first.Credentials[key] = value
			}
			for key, value := range tt.second {
				second.Credentials[key] = value
			}

			groups := buildOpenAI5hWakeQuotaGroups([]*Account{first, second})

			require.Len(t, groups, 2)
			require.Equal(t, int64(1), groups[0].accounts[0].ID)
			require.Equal(t, int64(2), groups[1].accounts[0].ID)
		})
	}
}

func TestBuildOpenAI5hWakePayloadIsMinimalAndUsesMappedModel(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	account.Credentials["model_mapping"] = map[string]any{
		openaipkg.DefaultTestModel: "custom-upstream-model",
	}

	payload, err := buildOpenAI5hWakePayload(account)
	require.NoError(t, err)
	require.Less(t, len(payload), 300)

	var body map[string]any
	require.NoError(t, json.Unmarshal(payload, &body))
	require.Equal(t, "custom-upstream-model", body["model"])
	require.Equal(t, openAI5hWakeInstructions, body["instructions"])
	require.Equal(t, true, body["stream"])
	require.Equal(t, false, body["store"])
	require.Equal(t, []string{"input", "instructions", "model", "store", "stream"}, sortedMapKeys(body))
	require.NotContains(t, string(payload), "max_output_tokens")
	require.NotContains(t, string(payload), "You are")
}

func TestOpenAI5hWakeRequestUsesLegacyOrganizationIDHeader(t *testing.T) {
	t.Parallel()

	account := newOpenAI5hWakeAccount(1, "")
	account.Credentials["organization_id"] = "legacy-workspace"
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{
		openAI5hWakeHeaders(http.StatusOK),
	}}
	wake := &OpenAI5hWakeService{
		accountRepo:   &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{account.ID: account}},
		tokenProvider: NewOpenAITokenProvider(&openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{account.ID: account}}, nil, nil),
		httpUpstream:  upstream,
	}

	result := wake.sendMinimumWakeRequest(context.Background(), account)

	require.NoError(t, result.err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "legacy-workspace", upstream.requests[0].headers.Get("chatgpt-account-id"))
}

func TestValidateOpenAI5hWakeResponseBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name: "completed SSE",
			body: "data: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n",
		},
		{
			name: "event name with buffered response object",
			body: "event: response.completed\ndata: {\"response\":{\"status\":\"completed\"}}\n\n",
		},
		{
			name: "buffered JSON",
			body: `{"type":"response.done","response":{"status":"completed"}}`,
		},
		{
			name: "buffered JSON without event type",
			body: `{"status":"completed"}`,
		},
		{
			name:    "explicit failure",
			body:    "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n",
			wantErr: true,
		},
		{
			name:    "top-level incomplete status",
			body:    `{"type":"response.completed","status":"incomplete"}`,
			wantErr: true,
		},
		{
			name:    "incomplete terminal event",
			body:    "event: response.incomplete\ndata: {\"status\":\"incomplete\"}\n\n",
			wantErr: true,
		},
		{
			name:    "incomplete stream",
			body:    "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOpenAI5hWakeResponseBody([]byte(tt.body))
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestOpenAI5hWakeDiagnosticExtractsAllowlistedFieldsAndRedactsSecrets(t *testing.T) {
	body := []byte(`{
		"error": {
			"code": "account_deactivated",
			"type": "invalid_request_error",
			"message": "request denied access_token=top-secret"
		},
		"internal_trace": "must-not-be-persisted"
	}`)

	diagnostic := wakeDiagnosticFromBody(body)

	require.Contains(t, diagnostic, "upstream_error_code=account_deactivated")
	require.Contains(t, diagnostic, "upstream_error_type=invalid_request_error")
	require.Contains(t, diagnostic, "upstream_error_message=")
	require.NotContains(t, diagnostic, "top-secret")
	require.NotContains(t, diagnostic, "internal_trace")
}

func TestOpenAI5hWakeHTTPFailureKeepsSafePhaseStatusAndDiagnostic(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	response := openAI5hWakeHeaders(http.StatusForbidden)
	response.headers.Set("Content-Type", "application/json")
	response.body = `{"error":{"code":"account_deactivated","message":"denied access_token=top-secret"}}`
	wake := &OpenAI5hWakeService{
		accountRepo:   repo,
		tokenProvider: NewOpenAITokenProvider(repo, nil, nil),
		httpUpstream:  &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{response}},
	}

	result := wake.sendMinimumWakeRequest(context.Background(), account)

	require.NoError(t, result.err)
	require.Equal(t, http.StatusForbidden, result.statusCode)
	require.Equal(t, "application/json", result.contentType)
	require.Equal(t, "upstream_request", result.phase)
	require.Equal(t, "forbidden", result.errorCode)
	require.Contains(t, result.diagnostic, "upstream_error_code=account_deactivated")
	require.NotContains(t, result.diagnostic, "top-secret")
}

func TestOpenAI5hWakeProcessItemPersistsRequestFailureDiagnostics(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	accountRepo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	eventRepo := &openAI5hWakeWorkerRepo{}
	response := openAI5hWakeHeaders(http.StatusForbidden)
	response.headers.Set("Content-Type", "application/json")
	response.body = `{"error":{"code":"account_deactivated","message":"denied access_token=top-secret"}}`
	wake := &OpenAI5hWakeService{
		repo:          eventRepo,
		accountRepo:   accountRepo,
		tokenProvider: NewOpenAITokenProvider(accountRepo, nil, nil),
		httpUpstream:  &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{response}},
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 9, TaskID: 77, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
	})

	require.Equal(t, OpenAI5hWakeItemStatusFailed, result.Status)
	require.Equal(t, "forbidden", result.ErrorCode)
	eventRepo.mu.Lock()
	defer eventRepo.mu.Unlock()
	codes := make([]string, 0, len(eventRepo.events))
	for _, event := range eventRepo.events {
		codes = append(codes, event.Code)
		if event.Code == "usage_check_failed" {
			require.Equal(t, OpenAI5hWakeEventLevelWarn, event.Level)
		}
		if event.Code == "wake_request_failed" || event.Code == "account_attempt_failed" {
			require.Contains(t, event.Message, "account_id=1")
			require.Contains(t, event.Message, "phase=upstream_request")
			require.Contains(t, event.Message, "status=403")
			require.Contains(t, event.Message, `content_type="application/json"`)
			require.Contains(t, event.Message, "error_code=forbidden")
			require.Contains(t, event.Message, "upstream_error_code=account_deactivated")
			require.NotContains(t, event.Message, "top-secret")
		}
	}
	require.Contains(t, codes, "account_attempt_started")
	require.Contains(t, codes, "usage_check_failed")
	require.Contains(t, codes, "wake_request_started")
	require.Contains(t, codes, "wake_request_failed")
	require.Contains(t, codes, "account_attempt_failed")
}

func TestOpenAI5hWakeProcessItemAttributesDelayedSharedWindowToAcceptedRequestAccount(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		responseBody string
	}{
		{name: "complete stream"},
		{name: "incomplete stream", responseBody: "data: {\"type\":\"response.created\"}\n\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			first := newOpenAI5hWakeAccount(1, "shared-pool")
			second := newOpenAI5hWakeAccount(2, "shared-pool")
			repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: first, 2: second}}
			var usageCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch usageCalls.Add(1) {
				case 1:
					_, _ = io.WriteString(w, `{"rate_limit":null}`)
				case 2:
					w.WriteHeader(http.StatusBadGateway)
				default:
					_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
				}
			}))
			defer server.Close()

			tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
			quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
			response := openAI5hWakeStubResponse{status: http.StatusOK, headers: make(http.Header), body: testCase.responseBody}
			upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{response}}
			eventRepo := &openAI5hWakeWorkerRepo{}
			wake := &OpenAI5hWakeService{
				repo: eventRepo, accountRepo: repo, quotaService: quota, tokenProvider: tokenProvider, httpUpstream: upstream,
			}
			group := buildOpenAI5hWakeQuotaGroups([]*Account{first, second})[0]

			result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
				ID: 9, TaskID: 77, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1, 2},
			})

			require.Equal(t, OpenAI5hWakeItemStatusWoken, result.Status)
			require.Equal(t, []int64{1, 2}, result.AttemptedAccountIDs)
			require.NotNil(t, result.SuccessfulAccountID)
			require.Equal(t, int64(1), *result.SuccessfulAccountID)
			require.Equal(t, int32(3), usageCalls.Load())
			require.Len(t, upstream.requests, 1)
			require.Equal(t, int64(1), upstream.requests[0].accountID)

			eventRepo.mu.Lock()
			defer eventRepo.mu.Unlock()
			foundAccepted := false
			foundSucceeded := false
			for _, event := range eventRepo.events {
				if event.Code == "wake_request_accepted" {
					foundAccepted = true
					require.Contains(t, event.Message, "account_id=1")
				}
				if event.Code == "wake_request_succeeded" {
					foundSucceeded = true
					require.Contains(t, event.Message, "phase=window_confirmation")
					require.Contains(t, event.Message, "account_id=1")
				}
			}
			require.True(t, foundAccepted)
			require.True(t, foundSucceeded)
		})
	}
}

func TestOpenAI5hWakeProcessItemAttributesFallback429WindowToAcceptedRequestAccount(t *testing.T) {
	first := newOpenAI5hWakeAccount(1, "shared-pool")
	second := newOpenAI5hWakeAccount(2, "shared-pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: first, 2: second}}
	var usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch usageCalls.Add(1) {
		case 1, 3:
			_, _ = io.WriteString(w, `{"rate_limit":null}`)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			t.Fatalf("unexpected usage request")
		}
	}))
	defer server.Close()

	tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
	quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
	accepted := openAI5hWakeStubResponse{status: http.StatusOK, headers: make(http.Header)}
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{
		accepted,
		openAI5hWakeHeaders(http.StatusTooManyRequests),
	}}
	wake := &OpenAI5hWakeService{
		accountRepo: repo, quotaService: quota, tokenProvider: tokenProvider, httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{first, second})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 9, TaskID: 77, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1, 2},
	})

	require.Equal(t, OpenAI5hWakeItemStatusWoken, result.Status)
	require.Equal(t, []int64{1, 2}, result.AttemptedAccountIDs)
	require.NotNil(t, result.SuccessfulAccountID)
	require.Equal(t, int64(1), *result.SuccessfulAccountID)
	require.NotNil(t, result.ResetAt)
	require.Equal(t, int32(3), usageCalls.Load())
	require.Len(t, upstream.requests, 2)
}

func TestOpenAI5hWakePartialSnapshotInvalidatesTrustedMarker(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	account.Extra["codex_5h_reset_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	account.Extra[openAI5hWakeSnapshotIdentityKey] = openAI5hWakeIdentityHash(account)
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{account.ID: account}}
	service := &OpenAI5hWakeService{accountRepo: repo}

	require.NoError(t, service.persistWakeSnapshot(context.Background(), account, map[string]any{
		"codex_7d_used_percent": 42,
		"codex_7d_reset_at":     time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}))

	updates := repo.updates[account.ID]
	require.Len(t, updates, 1)
	marker, exists := updates[0][openAI5hWakeSnapshotIdentityKey]
	require.True(t, exists)
	require.Nil(t, marker)
	require.False(t, hasTrustedOpenAI5hWakeSnapshot(account))
}

func TestOpenAI5hWakePersistSnapshotRejectsIdentityChangedCAS(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	ok := false
	repo := &openAI5hWakeAccountRepoStub{
		accounts:      map[int64]*Account{account.ID: account},
		snapshotCASOK: &ok,
	}
	wake := &OpenAI5hWakeService{accountRepo: repo}

	err := wake.persistWakeSnapshot(context.Background(), account, map[string]any{
		"codex_5h_used_percent": 42,
		"codex_5h_reset_at":     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})

	require.ErrorIs(t, err, errOpenAI5hWakeIdentityChanged)
	require.Empty(t, repo.updates)
}

func TestOpenAI5hWakeUsageQueryPreservesIdentityConflict(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{
		accounts:          map[int64]*Account{account.ID: account},
		snapshotCASOKByID: map[int64]bool{account.ID: false},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":7,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
	}))
	defer server.Close()
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, nil, nil), newQuotaRedirectingFactory(server))
	wake := &OpenAI5hWakeService{accountRepo: repo, quotaService: quota}

	_, _, err := wake.queryAndPersistGlobalUsage(context.Background(), account)

	require.ErrorIs(t, err, errOpenAI5hWakeSnapshotPersist)
	require.ErrorIs(t, err, errOpenAI5hWakeIdentityChanged)
	require.Empty(t, repo.updates)
}

func TestOpenAI5hWakeUsageQueryUsesAuthoritativeSnapshotWhenIncomingWriteIsSuperseded(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	account.Extra[OpenAICodexSnapshotObservedAtExtraKey] = "99999999999999999999"
	repo := &openAI5hWakeSupersededSnapshotRepo{openAI5hWakeAccountRepoStub: &openAI5hWakeAccountRepoStub{
		accounts: map[int64]*Account{account.ID: account},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":7,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
	}))
	defer server.Close()
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, nil, nil), newQuotaRedirectingFactory(server))
	wake := &OpenAI5hWakeService{accountRepo: repo, quotaService: quota}

	_, resetAt, err := wake.queryAndPersistGlobalUsage(context.Background(), account)

	require.NoError(t, err)
	require.Nil(t, resetAt, "a reset timestamp discarded by the database must not drive the wake result")
	require.NotContains(t, account.Extra, "codex_5h_reset_at")
}

func TestOpenAI5hWakeRuntimePlanWithoutEntitlementDoesNotWake(t *testing.T) {
	for _, planType := range []string{"free", "ABNORMAL"} {
		t.Run(strings.ToLower(planType), func(t *testing.T) {
			account := newOpenAI5hWakeAccount(1, "pool")
			repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{account.ID: account}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"plan_type":%q,"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`, planType)
			}))
			defer server.Close()

			tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
			quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
			upstream := &openAI5hWakeHTTPStub{}
			wake := &OpenAI5hWakeService{
				accountRepo: repo, quotaService: quota, tokenProvider: tokenProvider, httpUpstream: upstream,
			}
			group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

			result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
				ID: 1, TaskID: 11, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
			})

			require.Equal(t, OpenAI5hWakeItemStatusFailed, result.Status)
			require.Equal(t, "no_5h_entitlement", result.ErrorCode)
			require.Equal(t, []int64{account.ID}, result.AttemptedAccountIDs)
			require.Empty(t, upstream.requests, "an ineligible runtime plan must not receive a wake request")
			require.Empty(t, repo.updates, "an ineligible runtime plan must not overwrite its saved window")
		})
	}
}

func TestOpenAI5hWakeRuntimeNoEntitlementFallsBackToAnotherPoolMember(t *testing.T) {
	first := newOpenAI5hWakeAccount(1, "shared-pool")
	second := newOpenAI5hWakeAccount(2, "shared-pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: first, 2: second}}
	var usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if usageCalls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"plan_type":"free","rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"plan_type":"pro","rate_limit":null}`)
	}))
	defer server.Close()

	tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
	quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{openAI5hWakeHeaders(http.StatusOK)}}
	eventRepo := &openAI5hWakeWorkerRepo{}
	wake := &OpenAI5hWakeService{
		repo: eventRepo, accountRepo: repo, quotaService: quota, tokenProvider: tokenProvider, httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{first, second})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, TaskID: 11, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1, 2},
	})

	require.Equal(t, OpenAI5hWakeItemStatusWoken, result.Status)
	require.Equal(t, []int64{1, 2}, result.AttemptedAccountIDs)
	require.NotNil(t, result.SuccessfulAccountID)
	require.Equal(t, int64(2), *result.SuccessfulAccountID)
	require.Equal(t, int32(2), usageCalls.Load())
	require.Len(t, upstream.requests, 1)
	require.Equal(t, int64(2), upstream.requests[0].accountID)
	require.Empty(t, repo.updates[first.ID])

	eventRepo.mu.Lock()
	defer eventRepo.mu.Unlock()
	found := false
	for _, event := range eventRepo.events {
		if event.Code == "no_5h_entitlement" {
			found = true
			require.Contains(t, event.Message, "account_id=1")
			require.Contains(t, event.Message, "error_code=no_5h_entitlement")
			require.NotContains(t, event.Message, "free")
		}
	}
	require.True(t, found, "operators need a durable no-entitlement event for the skipped candidate")
}

func TestOpenAI5hWake429UsesNewerAuthoritativeActiveSnapshot(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	future := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	baseRepo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{account.ID: account}}
	repo := &openAI5hWakeSupersededSnapshotRepo{openAI5hWakeAccountRepoStub: baseRepo}
	repo.onSupersededUpdate = func() {
		account.Extra[OpenAICodexSnapshotObservedAtExtraKey] = "99999999999999999999"
		account.Extra["codex_5h_window_minutes"] = 300
		account.Extra["codex_5h_used_percent"] = float64(1)
		account.Extra["codex_5h_reset_at"] = future.Format(time.RFC3339)
		account.Extra[openAI5hWakeSnapshotIdentityKey] = openAI5hWakeIdentityHash(account)
	}
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{openAI5hWakeHeaders(http.StatusTooManyRequests)}}
	wake := &OpenAI5hWakeService{
		accountRepo: repo, tokenProvider: NewOpenAITokenProvider(repo, nil, nil), httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
	})

	require.Equal(t, OpenAI5hWakeItemStatusSkippedActive, result.Status)
	require.Empty(t, result.ErrorCode)
	require.NotNil(t, result.ResetAt)
	require.Equal(t, future, result.ResetAt.UTC())
	require.Len(t, upstream.requests, 1)
}

func TestOpenAI5hWakeResponseHeadersCannotOverrideNewerSnapshotWithout5hWindow(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			account := newOpenAI5hWakeAccount(1, "pool")
			account.Extra[OpenAICodexSnapshotObservedAtExtraKey] = "99999999999999999999"
			repo := &openAI5hWakeSupersededSnapshotRepo{openAI5hWakeAccountRepoStub: &openAI5hWakeAccountRepoStub{
				accounts: map[int64]*Account{account.ID: account},
			}}
			upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{openAI5hWakeHeaders(status)}}
			eventRepo := &openAI5hWakeWorkerRepo{}
			wake := &OpenAI5hWakeService{
				repo: eventRepo, accountRepo: repo, tokenProvider: NewOpenAITokenProvider(repo, nil, nil), httpUpstream: upstream,
			}
			group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

			result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
				ID: 1, TaskID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
			})

			require.Equal(t, OpenAI5hWakeItemStatusFailed, result.Status)
			require.Nil(t, result.ResetAt)
			require.Nil(t, result.SuccessfulAccountID)
			if status == http.StatusTooManyRequests {
				require.Equal(t, "rate_limited_unconfirmed", result.ErrorCode)
			} else {
				require.Equal(t, "post_usage_check_failed", result.ErrorCode)
			}
			require.Len(t, upstream.requests, 1)
			require.NotContains(t, account.Extra, "codex_5h_reset_at")
			if status == http.StatusOK {
				eventRepo.mu.Lock()
				codes := make([]string, 0, len(eventRepo.events))
				for _, event := range eventRepo.events {
					codes = append(codes, event.Code)
				}
				eventRepo.mu.Unlock()
				require.Contains(t, codes, "wake_request_accepted")
				require.NotContains(t, codes, "wake_request_succeeded")
			}
		})
	}
}

func TestOpenAI5hWakePostUsageIdentityConflictIsReported(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{
		accounts:           map[int64]*Account{account.ID: account},
		snapshotCASResults: []bool{true, false},
	}
	var usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if usageCalls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"rate_limit":null}`)
			return
		}
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
	}))
	defer server.Close()
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, nil, nil), newQuotaRedirectingFactory(server))
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{{status: http.StatusOK, headers: make(http.Header)}}}
	wake := &OpenAI5hWakeService{
		accountRepo: repo, quotaService: quota, tokenProvider: NewOpenAITokenProvider(repo, nil, nil), httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
	})

	require.Equal(t, OpenAI5hWakeItemStatusFailed, result.Status)
	require.Equal(t, "identity_changed", result.ErrorCode)
	require.Equal(t, int32(2), usageCalls.Load())
	require.Len(t, upstream.requests, 1)
	require.Len(t, repo.updates[account.ID], 1, "the authoritative pre-wake null snapshot should persist before the later identity conflict")
}

func TestOpenAI5hWakeProcessItemContinuesAfterUsageSnapshotIdentityConflict(t *testing.T) {
	first := newOpenAI5hWakeAccount(1, "shared-pool")
	second := newOpenAI5hWakeAccount(2, "shared-pool")
	repo := &openAI5hWakeAccountRepoStub{
		accounts:          map[int64]*Account{1: first, 2: second},
		snapshotCASOKByID: map[int64]bool{1: false, 2: true},
	}
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{
		openAI5hWakeHeaders(http.StatusOK),
		openAI5hWakeHeaders(http.StatusOK),
	}}
	wake := &OpenAI5hWakeService{accountRepo: repo, httpUpstream: upstream, tokenProvider: NewOpenAITokenProvider(repo, nil, nil)}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{first, second})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1, 2},
	})

	require.Equal(t, OpenAI5hWakeItemStatusWoken, result.Status)
	require.Equal(t, []int64{1, 2}, result.AttemptedAccountIDs)
	require.NotNil(t, result.SuccessfulAccountID)
	require.Equal(t, int64(2), *result.SuccessfulAccountID)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, int64(1), upstream.requests[0].accountID)
	require.Equal(t, int64(2), upstream.requests[1].accountID)
	require.Empty(t, repo.updates[1])
	require.NotEmpty(t, repo.updates[2])
}

func TestOpenAI5hWakeProcessItemRejectsIncompleteSuccessfulStream(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	response := openAI5hWakeHeaders(http.StatusOK)
	response.body = "data: {\"type\":\"response.created\"}\n\n"
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	wake := &OpenAI5hWakeService{
		accountRepo:   repo,
		tokenProvider: NewOpenAITokenProvider(repo, nil, nil),
		httpUpstream:  &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{response}},
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
	})

	require.Equal(t, OpenAI5hWakeItemStatusFailed, result.Status)
	require.Equal(t, "response_stream_incomplete", result.ErrorCode)
}

func TestOpenAI5hWakeProcessItemCountsConfirmedIncomplete2xxAsWoken(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	var usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if usageCalls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"rate_limit":null}`)
			return
		}
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
	}))
	defer server.Close()

	tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
	quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
	response := openAI5hWakeHeaders(http.StatusOK)
	response.body = "data: {\"type\":\"response.created\"}\n\n"
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{response}}
	wake := &OpenAI5hWakeService{
		accountRepo: repo, quotaService: quota, tokenProvider: tokenProvider, httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
	})

	require.Equal(t, OpenAI5hWakeItemStatusWoken, result.Status)
	require.Empty(t, result.ErrorCode)
	require.Equal(t, int32(2), usageCalls.Load())
	require.Len(t, upstream.requests, 1)
	require.NotNil(t, result.ResetAt)
}

func TestOpenAI5hWakeProcessItemPollsUntilWindowBecomesVisible(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "complete stream"},
		{name: "incomplete stream", body: "data: {\"type\":\"response.created\"}\n\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			account := newOpenAI5hWakeAccount(1, "pool")
			repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
			var usageCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if usageCalls.Add(1) < 3 {
					_, _ = io.WriteString(w, `{"rate_limit":null}`)
					return
				}
				_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
			}))
			defer server.Close()

			tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
			quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
			response := openAI5hWakeStubResponse{status: http.StatusOK, headers: make(http.Header), body: testCase.body}
			upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{response}}
			wake := &OpenAI5hWakeService{
				accountRepo: repo, quotaService: quota, tokenProvider: tokenProvider, httpUpstream: upstream,
			}
			group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

			result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
				ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
			})

			require.Equal(t, OpenAI5hWakeItemStatusWoken, result.Status)
			require.Empty(t, result.ErrorCode)
			require.Equal(t, int32(3), usageCalls.Load())
			require.Len(t, upstream.requests, 1)
			require.NotNil(t, result.ResetAt)
		})
	}
}

func TestOpenAI5hWakeConfirmationStopsAtItsOwnTimeout(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	var usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		usageCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":null}`)
	}))
	defer server.Close()

	eventRepo := &openAI5hWakeWorkerRepo{}
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, nil, nil), newQuotaRedirectingFactory(server))
	wake := &OpenAI5hWakeService{repo: eventRepo, accountRepo: repo, quotaService: quota}
	startedAt := time.Now()

	resetAt, err := wake.confirmWakeWindowWithTimings(
		context.Background(), 7, 9, account, 1, 1,
		openAI5hWakeConfirmationTimings{timeout: 45 * time.Millisecond, delay: 5 * time.Millisecond, maxDelay: 10 * time.Millisecond},
	)

	require.NoError(t, err)
	require.Nil(t, resetAt)
	require.GreaterOrEqual(t, usageCalls.Load(), int32(2))
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
	eventRepo.mu.Lock()
	defer eventRepo.mu.Unlock()
	require.NotEmpty(t, eventRepo.events)
	for _, event := range eventRepo.events {
		require.Equal(t, "wake_confirmation_pending", event.Code)
	}
}

func TestOpenAI5hWakeConfirmationCancelsWhileWaiting(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	var usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		usageCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":null}`)
	}))
	defer server.Close()

	pending := make(chan struct{}, 1)
	eventRepo := &openAI5hWakeWorkerRepo{appendEventFn: func(event OpenAI5hWakeTaskEventParams) {
		if event.Code == "wake_confirmation_pending" {
			select {
			case pending <- struct{}{}:
			default:
			}
		}
	}}
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, nil, nil), newQuotaRedirectingFactory(server))
	wake := &OpenAI5hWakeService{repo: eventRepo, accountRepo: repo, quotaService: quota}
	ctx, cancel := context.WithCancel(context.Background())
	type confirmationResult struct {
		resetAt *time.Time
		err     error
	}
	resultCh := make(chan confirmationResult, 1)
	go func() {
		resetAt, err := wake.confirmWakeWindowWithTimings(
			ctx, 7, 9, account, 1, 1,
			openAI5hWakeConfirmationTimings{timeout: 5 * time.Second, delay: time.Second, maxDelay: time.Second},
		)
		resultCh <- confirmationResult{resetAt: resetAt, err: err}
	}()

	select {
	case <-pending:
	case <-time.After(time.Second):
		t.Fatal("confirmation did not enter its cancellable wait")
	}
	callsBeforeCancel := usageCalls.Load()
	cancel()
	select {
	case result := <-resultCh:
		require.ErrorIs(t, result.err, context.Canceled)
		require.Nil(t, result.resetAt)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("confirmation did not stop promptly after cancellation")
	}
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, callsBeforeCancel, usageCalls.Load())
}

func TestOpenAI5hWakeProcessItemContinuesAfterUsageFailureAndFallsBackAccount(t *testing.T) {
	proxyID := int64(9)
	proxy := &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 7890}
	first := newOpenAI5hWakeAccount(1, "shared-pool")
	second := newOpenAI5hWakeAccount(2, "shared-pool")
	first.ProxyID, first.Proxy = &proxyID, proxy
	second.ProxyID, second.Proxy = &proxyID, proxy
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: first, 2: second}}
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{
		{status: http.StatusUnauthorized, headers: make(http.Header)},
		openAI5hWakeHeaders(http.StatusOK),
	}}
	service := &OpenAI5hWakeService{
		accountRepo:   repo,
		quotaService:  nil,
		tokenProvider: NewOpenAITokenProvider(repo, nil, nil),
		httpUpstream:  upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{first, second})[0]
	item := &OpenAI5hWakeTaskItem{
		ID: 10, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1, 2},
	}

	result := service.processItem(context.Background(), item)
	require.Equal(t, OpenAI5hWakeItemStatusWoken, result.Status)
	require.Equal(t, []int64{1, 2}, result.AttemptedAccountIDs)
	require.NotNil(t, result.SuccessfulAccountID)
	require.Equal(t, int64(2), *result.SuccessfulAccountID)
	require.NotNil(t, result.ResetAt)
	require.Len(t, upstream.requests, 2)
	for index, request := range upstream.requests {
		require.Equal(t, int64(index+1), request.accountID)
		require.Equal(t, 3, request.concurrency)
		require.Equal(t, "http://127.0.0.1:7890", request.proxyURL)
		require.Equal(t, HTTPUpstreamProfileOpenAI, request.profile)
		require.Equal(t, "Bearer token", request.headers.Get("Authorization"))
		require.Equal(t, "shared-pool", request.headers.Get("chatgpt-account-id"))
		require.Equal(t, codexCLIUserAgent, request.headers.Get("User-Agent"))
		require.NotContains(t, string(request.body), "max_output_tokens")
	}
	require.Empty(t, repo.updates[1])
	require.NotEmpty(t, repo.updates[2])
	require.NotContains(t, first.Extra, "codex_5h_reset_at")
	require.Contains(t, second.Extra, "codex_5h_reset_at")
}

func TestOpenAI5hWakeProcessItemDoesNotCallFailedRequestWoken(t *testing.T) {
	first := newOpenAI5hWakeAccount(1, "shared-pool")
	second := newOpenAI5hWakeAccount(2, "shared-pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: first, 2: second}}
	var usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if usageCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":3,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
	}))
	defer server.Close()
	tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
	quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{{err: errors.New("dial timeout")}}}
	wake := &OpenAI5hWakeService{
		accountRepo: repo, quotaService: quota, tokenProvider: tokenProvider, httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{first, second})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1, 2},
	})

	require.Equal(t, OpenAI5hWakeItemStatusSkippedActive, result.Status)
	require.Equal(t, []int64{1, 2}, result.AttemptedAccountIDs)
	require.NotNil(t, result.SuccessfulAccountID)
	require.Equal(t, int64(2), *result.SuccessfulAccountID)
	require.Len(t, upstream.requests, 1)
	require.Empty(t, repo.updates[1])
	require.NotEmpty(t, repo.updates[2])
}

func TestOpenAI5hWakeProcessItemSkipsPersistedActiveWindow(t *testing.T) {
	first := newOpenAI5hWakeAccount(1, "shared-pool")
	second := newOpenAI5hWakeAccount(2, "shared-pool")
	resetAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	first.Extra["codex_5h_reset_at"] = resetAt.Format(time.RFC3339)
	first.Extra["codex_5h_used_percent"] = float64(1)
	first.Extra[openAI5hWakeSnapshotIdentityKey] = openAI5hWakeIdentityHash(first)
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: first, 2: second}}
	upstream := &openAI5hWakeHTTPStub{}
	service := &OpenAI5hWakeService{accountRepo: repo, httpUpstream: upstream}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{first, second})[0]

	result := service.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1, 2},
	})

	require.Equal(t, OpenAI5hWakeItemStatusSkippedActive, result.Status)
	require.Empty(t, result.AttemptedAccountIDs)
	require.Empty(t, upstream.requests)
	require.Empty(t, repo.updates)
	require.NotContains(t, second.Extra, "codex_5h_reset_at")
}

func TestOpenAI5hWakeProcessItemWakesZeroPercentWindow(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	resetAt := time.Now().Add(5 * time.Hour).UTC().Truncate(time.Second)
	account.Extra["codex_5h_reset_at"] = resetAt.Format(time.RFC3339)
	account.Extra["codex_5h_used_percent"] = float64(0)
	account.Extra[openAI5hWakeSnapshotIdentityKey] = openAI5hWakeIdentityHash(account)
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{account.ID: account}}
	var usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		usedPercent := 0
		if usageCalls.Add(1) > 1 {
			usedPercent = 1
		}
		_, _ = fmt.Fprintf(w, `{"rate_limit":{"primary_window":{"used_percent":%d,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`, usedPercent)
	}))
	defer server.Close()

	tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
	quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{{status: http.StatusOK}}}
	wake := &OpenAI5hWakeService{
		accountRepo: repo, quotaService: quota, tokenProvider: tokenProvider, httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, TaskID: 12, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
	})

	require.Equal(t, OpenAI5hWakeItemStatusWoken, result.Status)
	require.Equal(t, []int64{account.ID}, result.AttemptedAccountIDs)
	require.Len(t, upstream.requests, 1)
	require.GreaterOrEqual(t, usageCalls.Load(), int32(2))
}

func TestOpenAI5hWakeDoesNotTrustLegacyActiveSnapshot(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	account.Extra["codex_5h_reset_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":9,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
	}))
	defer server.Close()
	tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
	quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
	wake := &OpenAI5hWakeService{accountRepo: repo, quotaService: quota, tokenProvider: tokenProvider}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
	})

	require.Equal(t, OpenAI5hWakeItemStatusSkippedActive, result.Status)
	require.Equal(t, []int64{account.ID}, result.AttemptedAccountIDs)
	require.NotEmpty(t, repo.updates[account.ID])
	lastUpdate := repo.updates[account.ID][len(repo.updates[account.ID])-1]
	require.Equal(t, group.identityHash, lastUpdate[openAI5hWakeSnapshotIdentityKey])
	require.Equal(t, float64(9), account.Extra["codex_5h_used_percent"])
}

func TestOpenAI5hWakeUsageQueryOnlyUpdatesQueriedAccount(t *testing.T) {
	first := newOpenAI5hWakeAccount(1, "shared-pool")
	second := newOpenAI5hWakeAccount(2, "shared-pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: first, 2: second}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":7,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
	}))
	defer server.Close()
	tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
	quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))
	service := &OpenAI5hWakeService{accountRepo: repo, quotaService: quota}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{first, second})[0]

	result := service.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1, 2},
	})

	require.Equal(t, OpenAI5hWakeItemStatusSkippedActive, result.Status)
	require.Equal(t, []int64{1}, result.AttemptedAccountIDs)
	require.NotEmpty(t, repo.updates[1])
	require.Empty(t, repo.updates[2])
	require.Contains(t, first.Extra, "codex_5h_reset_at")
	require.NotContains(t, second.Extra, "codex_5h_reset_at")
}

func TestOpenAI5hWakeUsageSnapshotPersistenceFailureDoesNotReportSuccess(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{
		accounts:      map[int64]*Account{1: account},
		updateErrByID: map[int64]error{1: errors.New("database unavailable")},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":7,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
	}))
	defer server.Close()
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, nil, nil), newQuotaRedirectingFactory(server))
	upstream := &openAI5hWakeHTTPStub{}
	wake := &OpenAI5hWakeService{accountRepo: repo, quotaService: quota, httpUpstream: upstream}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1},
	})

	require.Equal(t, OpenAI5hWakeItemStatusFailed, result.Status)
	require.Equal(t, "snapshot_persist_failed", result.ErrorCode)
	require.Empty(t, upstream.requests)
	require.Empty(t, repo.updates)
}

func TestOpenAI5hWakeResponseSnapshotPersistenceFailureDoesNotReportWoken(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{
		accounts:      map[int64]*Account{1: account},
		updateErrByID: map[int64]error{1: errors.New("database unavailable")},
	}
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{openAI5hWakeHeaders(http.StatusOK)}}
	wake := &OpenAI5hWakeService{
		accountRepo: repo, tokenProvider: NewOpenAITokenProvider(repo, nil, nil), httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1},
	})

	require.Equal(t, OpenAI5hWakeItemStatusFailed, result.Status)
	require.Equal(t, "snapshot_persist_failed", result.ErrorCode)
	require.Len(t, upstream.requests, 1)
	require.Empty(t, repo.updates)
}

func TestOpenAI5hWakeProcessItemTreats429WithFutureResetAsActive(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{
		openAI5hWakeHeaders(http.StatusTooManyRequests),
	}}
	service := &OpenAI5hWakeService{
		accountRepo: repo, tokenProvider: NewOpenAITokenProvider(repo, nil, nil), httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := service.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1},
	})
	require.Equal(t, OpenAI5hWakeItemStatusSkippedActive, result.Status)
	require.Empty(t, result.ErrorCode)
}

func TestOpenAI5hWakePreservesAuthErrorWhenErrorResponseHasRateLimitHeaders(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	response := openAI5hWakeHeaders(http.StatusUnauthorized)
	wake := &OpenAI5hWakeService{
		accountRepo:   repo,
		tokenProvider: NewOpenAITokenProvider(repo, nil, nil),
		httpUpstream:  &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{response}},
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]

	result := wake.processItem(context.Background(), &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{account.ID},
	})

	require.Equal(t, OpenAI5hWakeItemStatusFailed, result.Status)
	require.Equal(t, "unauthorized", result.ErrorCode)
}

func TestOpenAI5hWakeProcessItemReportsTimeoutWithoutMutatingAccountHealth(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	upstream := &openAI5hWakeHTTPStub{responses: []openAI5hWakeStubResponse{{block: true}}}
	service := &OpenAI5hWakeService{
		accountRepo: repo, tokenProvider: NewOpenAITokenProvider(repo, nil, nil), httpUpstream: upstream,
	}
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	result := service.processItem(ctx, &OpenAI5hWakeTaskItem{
		ID: 1, IdentityHash: group.identityHash, MemberAccountIDs: []int64{1},
	})
	require.Equal(t, OpenAI5hWakeItemStatusFailed, result.Status)
	require.Equal(t, "timeout", result.ErrorCode)
	require.Equal(t, StatusActive, account.Status)
	require.True(t, account.Schedulable)
}

func TestOpenAI5hWakeItemBudgetScalesWithFallbackAccounts(t *testing.T) {
	tests := []struct {
		name       string
		memberIDs  []int64
		wantBudget time.Duration
	}{
		{name: "nil item", wantBudget: openAI5hWakeAccountAttemptTimeout},
		{name: "empty members", memberIDs: []int64{}, wantBudget: openAI5hWakeAccountAttemptTimeout},
		{name: "one account", memberIDs: []int64{1}, wantBudget: openAI5hWakeAccountAttemptTimeout},
		{name: "four fallbacks", memberIDs: []int64{1, 2, 3, 4}, wantBudget: 4 * openAI5hWakeAccountAttemptTimeout},
		{name: "large pool is bounded", memberIDs: []int64{1, 2, 3, 4, 5, 6, 7, 8, 9}, wantBudget: openAI5hWakeItemTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var item *OpenAI5hWakeTaskItem
			if tt.memberIDs != nil {
				item = &OpenAI5hWakeTaskItem{MemberAccountIDs: tt.memberIDs}
			}
			require.Equal(t, tt.wantBudget, openAI5hWakeItemBudget(item))
		})
	}
}

func TestOpenAI5hWakeLightweightUsageSkipsResetCreditsAndOmitsErrorBody(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "pool")
	repo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{1: account}}
	tokenProvider := NewOpenAITokenProvider(repo, nil, nil)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_after_seconds":18000}}}`)
	}))
	defer server.Close()
	quota := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(server))

	usage, err := quota.QueryUsageLightweight(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, usage.RateLimit)
	require.Equal(t, []string{"/backend-api/wham/usage"}, paths)

	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "secret-upstream-response-body")
	}))
	defer errorServer.Close()
	quota = NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(errorServer))
	_, err = quota.QueryUsageLightweight(context.Background(), 1)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-upstream-response-body")
}

func newOpenAI5hWakeWorkerFixture(itemCount int, upstream HTTPUpstream) (*OpenAI5hWakeService, *openAI5hWakeWorkerRepo) {
	accounts := make(map[int64]*Account, itemCount)
	items := make([]*OpenAI5hWakeTaskItem, 0, itemCount)
	for index := 0; index < itemCount; index++ {
		id := int64(index + 1)
		account := newOpenAI5hWakeAccount(id, fmt.Sprintf("pool-%d", id))
		accounts[id] = account
		group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]
		items = append(items, &OpenAI5hWakeTaskItem{
			ID: id, TaskID: 71, IdentityHash: group.identityHash,
			MemberAccountIDs: []int64{id}, Status: OpenAI5hWakeItemStatusPending,
		})
	}
	task := &OpenAI5hWakeTask{
		ID: 71, Status: OpenAI5hWakeTaskStatusRunning, TotalItems: itemCount,
	}
	taskRepo := &openAI5hWakeWorkerRepo{task: task, items: items}
	accountRepo := &openAI5hWakeAccountRepoStub{accounts: accounts}
	wake := NewOpenAI5hWakeService(
		taskRepo, accountRepo, nil, NewOpenAITokenProvider(accountRepo, nil, nil),
		upstream, nil, nil, nil,
	)
	return wake, taskRepo
}

func TestOpenAI5hWakeWorkerLimitsConcurrencyToEight(t *testing.T) {
	upstream := &openAI5hWakeObservedUpstream{delay: 25 * time.Millisecond}
	wake, repo := newOpenAI5hWakeWorkerFixture(24, upstream)

	wake.processTask(repo.task)

	require.Equal(t, int32(24), upstream.requestCount.Load())
	require.Greater(t, upstream.maxActive.Load(), int32(1))
	require.LessOrEqual(t, upstream.maxActive.Load(), int32(openAI5hWakeConcurrency))
	require.Equal(t, openAI5hWakeConcurrency, int(upstream.maxActive.Load()))
	require.Equal(t, OpenAI5hWakeTaskStatusSucceeded, repo.task.Status)
	require.Equal(t, 24, repo.task.ProcessedItems)
}

func TestOpenAI5hWakeClaimUsesLocalElapsedLeaseDeadline(t *testing.T) {
	wake, repo := newOpenAI5hWakeWorkerFixture(1, &openAI5hWakeObservedUpstream{})
	// Simulate a database clock far behind the application host. The absolute
	// timestamp returned by PostgreSQL must not make the local monitor abandon a
	// lease that was just claimed successfully.
	databaseLease := time.Now().Add(-time.Hour)
	repo.task.LeaseExpiresAt = &databaseLease
	claimStarted := time.Now()

	setOpenAI5hWakeLocalLeaseDeadline(repo.task, claimStarted)
	wake.processTask(repo.task)

	require.WithinDuration(t, claimStarted.Add(openAI5hWakeLeaseDuration), *repo.task.LeaseExpiresAt, time.Millisecond)
	require.Equal(t, OpenAI5hWakeTaskStatusSucceeded, repo.task.Status)
	require.Equal(t, 1, repo.task.ProcessedItems)
}

func TestOpenAI5hWakeCancellationDuringRecoveryFinalizesWithoutLeaseTakeover(t *testing.T) {
	wake, repo := newOpenAI5hWakeWorkerFixture(1, &openAI5hWakeObservedUpstream{})
	recoveryStarted := make(chan struct{})
	repo.recoverFn = func(ctx context.Context, _ int64, _ string, _ int) (int, error) {
		close(recoveryStarted)
		<-ctx.Done()
		return 0, ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		wake.processTask(repo.task)
		close(done)
	}()

	select {
	case <-recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("task recovery did not start")
	}
	_, err := wake.CancelTask(context.Background(), repo.task.ID)
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation during recovery waited for lease takeover")
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, OpenAI5hWakeTaskStatusCancelled, repo.task.Status)
	require.Equal(t, repo.task.TotalItems, repo.task.CancelledCount)
	require.Equal(t, 1, repo.finalizeCalls)
}

func TestOpenAI5hWakeRecoveryCancelCheckFailureLeavesLeaseForRecovery(t *testing.T) {
	wake, repo := newOpenAI5hWakeWorkerFixture(1, &openAI5hWakeObservedUpstream{})
	recoveryStarted := make(chan struct{})
	repo.cancelErr = errors.New("database unavailable")
	repo.recoverFn = func(ctx context.Context, _ int64, _ string, _ int) (int, error) {
		close(recoveryStarted)
		<-ctx.Done()
		return 0, ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		wake.processTask(repo.task)
		close(done)
	}()

	select {
	case <-recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("task recovery did not start")
	}
	_, err := wake.CancelTask(context.Background(), repo.task.ID)
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after recovery cancellation confirmation failed")
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, OpenAI5hWakeTaskStatusRunning, repo.task.Status)
	require.Zero(t, repo.finalizeCalls)
	require.Condition(t, func() bool {
		for _, event := range repo.events {
			if event.Code == "cancel_check_failed" && strings.Contains(event.Message, "database unavailable") {
				return true
			}
		}
		return false
	})
}

func TestOpenAI5hWakeRecoveryStopsWhenConfirmedLeaseExpires(t *testing.T) {
	wake, repo := newOpenAI5hWakeWorkerFixture(1, &openAI5hWakeObservedUpstream{})
	leaseExpiresAt := time.Now().UTC().Add(80 * time.Millisecond)
	repo.task.LeaseExpiresAt = &leaseExpiresAt
	recoveryCancelled := make(chan struct{})
	repo.recoverFn = func(ctx context.Context, _ int64, _ string, _ int) (int, error) {
		<-ctx.Done()
		close(recoveryCancelled)
		return 0, ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		wake.processTask(repo.task)
		close(done)
	}()

	select {
	case <-recoveryCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("expired lease did not cancel task recovery")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after recovery lost its lease")
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, OpenAI5hWakeTaskStatusRunning, repo.task.Status)
	require.Zero(t, repo.finalizeCalls)
	require.Condition(t, func() bool {
		for _, event := range repo.events {
			if event.Code == "lease_lost" {
				return true
			}
		}
		return false
	})
}

func TestOpenAI5hWakeFinalizationRequiresFreshLeaseConfirmation(t *testing.T) {
	wake, repo := newOpenAI5hWakeWorkerFixture(1, &openAI5hWakeObservedUpstream{})
	repo.heartbeatFn = func(context.Context, int64, string, time.Time, time.Time) (bool, error) {
		return false, nil
	}

	wake.processTask(repo.task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, OpenAI5hWakeTaskStatusRunning, repo.task.Status)
	require.Zero(t, repo.finalizeCalls)
	require.Condition(t, func() bool {
		for _, event := range repo.events {
			if event.Code == "lease_lost" {
				return true
			}
		}
		return false
	})
}

func TestOpenAI5hWakeMonitorStopsAtConfirmedLeaseAfterHeartbeatFailures(t *testing.T) {
	repo := &openAI5hWakeWorkerRepo{
		task: &OpenAI5hWakeTask{ID: 81, Status: OpenAI5hWakeTaskStatusRunning},
		heartbeatFn: func(context.Context, int64, string, time.Time, time.Time) (bool, error) {
			return false, errors.New("database unavailable")
		},
	}
	service := NewOpenAI5hWakeService(repo, nil, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var lostLease atomic.Bool
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		service.monitorTaskWithTimings(
			ctx,
			cancel,
			repo.task.ID,
			time.Now().UTC().Add(90*time.Millisecond),
			&lostLease,
			done,
			openAI5hWakeMonitorTimings{
				heartbeatInterval:  15 * time.Millisecond,
				cancelPollInterval: time.Second,
				leaseDuration:      90 * time.Millisecond,
				heartbeatTimeout:   10 * time.Millisecond,
				cancelPollTimeout:  10 * time.Millisecond,
			},
		)
	}()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("monitor did not cancel work after the confirmed lease expired")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop after lease expiry")
	}
	require.True(t, lostLease.Load())

	repo.mu.Lock()
	codes := make([]string, 0, len(repo.events))
	for _, event := range repo.events {
		codes = append(codes, event.Code)
	}
	repo.mu.Unlock()
	require.Contains(t, codes, "heartbeat_failed")
	require.Contains(t, codes, "lease_lost")
}

func TestOpenAI5hWakeMonitorExtendsDeadlineOnlyAfterSuccessfulHeartbeat(t *testing.T) {
	firstHeartbeat := make(chan struct{})
	var firstHeartbeatOnce sync.Once
	var heartbeatCalls atomic.Int32
	repo := &openAI5hWakeWorkerRepo{
		task: &OpenAI5hWakeTask{ID: 82, Status: OpenAI5hWakeTaskStatusRunning},
		heartbeatFn: func(context.Context, int64, string, time.Time, time.Time) (bool, error) {
			if heartbeatCalls.Add(1) == 1 {
				firstHeartbeatOnce.Do(func() { close(firstHeartbeat) })
				return true, nil
			}
			return false, errors.New("database unavailable")
		},
	}
	service := NewOpenAI5hWakeService(repo, nil, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var lostLease atomic.Bool
	stopped := make(chan struct{})
	initialLease := time.Now().UTC().Add(300 * time.Millisecond)
	go func() {
		defer close(stopped)
		service.monitorTaskWithTimings(
			ctx,
			cancel,
			repo.task.ID,
			initialLease,
			&lostLease,
			done,
			openAI5hWakeMonitorTimings{
				heartbeatInterval:  20 * time.Millisecond,
				cancelPollInterval: time.Second,
				leaseDuration:      700 * time.Millisecond,
				heartbeatTimeout:   10 * time.Millisecond,
				cancelPollTimeout:  10 * time.Millisecond,
			},
		)
	}()

	select {
	case <-firstHeartbeat:
	case <-time.After(time.Second):
		t.Fatal("monitor did not perform its first heartbeat")
	}
	time.Sleep(time.Until(initialLease.Add(75 * time.Millisecond)))
	select {
	case <-ctx.Done():
		t.Fatal("successful heartbeat did not extend the confirmed lease deadline")
	default:
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop after the extended lease eventually expired")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("monitor did not exit after the extended lease expired")
	}
	require.True(t, lostLease.Load())
}

func TestOpenAI5hWakeMonitorAcceptsSuccessfulHeartbeatAcrossOldDeadline(t *testing.T) {
	var heartbeatCalls atomic.Int32
	firstHeartbeatReturned := make(chan struct{})
	repo := &openAI5hWakeWorkerRepo{
		task: &OpenAI5hWakeTask{ID: 84, Status: OpenAI5hWakeTaskStatusRunning},
		heartbeatFn: func(context.Context, int64, string, time.Time, time.Time) (bool, error) {
			if heartbeatCalls.Add(1) == 1 {
				// Model a database CAS that committed before the old deadline while
				// delivery of its successful result crossed that deadline.
				time.Sleep(85 * time.Millisecond)
				close(firstHeartbeatReturned)
				return true, nil
			}
			return false, errors.New("database unavailable")
		},
	}
	service := NewOpenAI5hWakeService(repo, nil, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var lostLease atomic.Bool
	stopped := make(chan struct{})
	initialLease := time.Now().UTC().Add(70 * time.Millisecond)
	go func() {
		defer close(stopped)
		service.monitorTaskWithTimings(
			ctx,
			cancel,
			repo.task.ID,
			initialLease,
			&lostLease,
			done,
			openAI5hWakeMonitorTimings{
				heartbeatInterval:  5 * time.Millisecond,
				cancelPollInterval: time.Second,
				leaseDuration:      300 * time.Millisecond,
				heartbeatTimeout:   100 * time.Millisecond,
				cancelPollTimeout:  10 * time.Millisecond,
			},
		)
	}()

	select {
	case <-firstHeartbeatReturned:
	case <-time.After(time.Second):
		t.Fatal("successful heartbeat did not return")
	}
	select {
	case <-ctx.Done():
		t.Fatal("worker discarded a database-confirmed renewed lease")
	default:
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop")
	}
	require.False(t, lostLease.Load())
}

func TestOpenAI5hWakeMonitorCancelsWorkBeforeWritingCancellationEvent(t *testing.T) {
	eventWriteStarted := make(chan struct{})
	releaseEventWrite := make(chan struct{})
	var eventWriteOnce sync.Once
	repo := &openAI5hWakeWorkerRepo{
		task:            &OpenAI5hWakeTask{ID: 83, Status: OpenAI5hWakeTaskStatusRunning},
		cancelRequested: true,
		appendEventFn: func(params OpenAI5hWakeTaskEventParams) {
			if params.Code != "cancel_observed" {
				return
			}
			eventWriteOnce.Do(func() { close(eventWriteStarted) })
			<-releaseEventWrite
		},
	}
	service := NewOpenAI5hWakeService(repo, nil, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var lostLease atomic.Bool
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		service.monitorTaskWithTimings(
			ctx,
			cancel,
			repo.task.ID,
			time.Now().UTC().Add(time.Second),
			&lostLease,
			done,
			openAI5hWakeMonitorTimings{
				heartbeatInterval:  time.Second,
				cancelPollInterval: 10 * time.Millisecond,
				leaseDuration:      time.Second,
				heartbeatTimeout:   10 * time.Millisecond,
				cancelPollTimeout:  10 * time.Millisecond,
			},
		)
	}()

	select {
	case <-eventWriteStarted:
	case <-time.After(time.Second):
		t.Fatal("monitor did not observe the cancellation request")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("work context remained active while cancellation event persistence was blocked")
	}
	close(releaseEventWrite)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop after the cancellation event was released")
	}
	require.False(t, lostLease.Load())
}

func TestOpenAI5hWakeWorkerPersistsCompletionFailureInTaskEvents(t *testing.T) {
	upstream := &openAI5hWakeObservedUpstream{}
	wake, repo := newOpenAI5hWakeWorkerFixture(1, upstream)
	repo.completeErr = errors.New(`pq: violates check constraint "attempted_ids_array_check"`)

	wake.processTask(repo.task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, 1, repo.items[0].AttemptCount)
	require.Equal(t, OpenAI5hWakeItemStatusRunning, repo.items[0].Status)
	codes := make([]string, 0, len(repo.events))
	for _, event := range repo.events {
		codes = append(codes, event.Code)
		if event.Code == "item_complete_failed" {
			require.Equal(t, OpenAI5hWakeEventLevelError, event.Level)
			require.Contains(t, event.Message, "attempted_ids_array_check")
		}
	}
	require.Contains(t, codes, "item_started")
	require.Contains(t, codes, "item_complete_failed")
	require.Contains(t, codes, "task_processing_failed")
}

func TestOpenAI5hWakeWorkerFailsRecoveredItemsAfterRetryLimit(t *testing.T) {
	upstream := &openAI5hWakeObservedUpstream{}
	wake, repo := newOpenAI5hWakeWorkerFixture(1, upstream)
	repo.items[0].Status = OpenAI5hWakeItemStatusRunning
	repo.items[0].AttemptCount = openAI5hWakeMaxItemAttempts

	wake.processTask(repo.task)

	require.Equal(t, int32(0), upstream.requestCount.Load())
	require.Equal(t, OpenAI5hWakeTaskStatusFailed, repo.task.Status)
	require.Equal(t, 1, repo.task.ProcessedItems)
	require.Equal(t, 1, repo.task.FailedCount)
	require.Equal(t, OpenAI5hWakeItemStatusFailed, repo.items[0].Status)
	require.Equal(t, "worker_retry_exhausted", repo.items[0].ErrorCode)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	codes := make([]string, 0, len(repo.events))
	for _, event := range repo.events {
		codes = append(codes, event.Code)
	}
	require.Contains(t, codes, "items_retry_exhausted")
	require.Contains(t, codes, "task_finished")
}

func TestOpenAI5hWakeWorkerPersistsWokenItemWhenCancellationFollowsSnapshot(t *testing.T) {
	account := newOpenAI5hWakeAccount(1, "cancel-after-wake")
	group := buildOpenAI5hWakeQuotaGroups([]*Account{account})[0]
	task := &OpenAI5hWakeTask{ID: 71, Status: OpenAI5hWakeTaskStatusRunning, TotalItems: 1}
	taskRepo := &openAI5hWakeWorkerRepo{
		task: task,
		items: []*OpenAI5hWakeTaskItem{{
			ID: 1, TaskID: task.ID, IdentityHash: group.identityHash,
			MemberAccountIDs: []int64{account.ID}, Status: OpenAI5hWakeItemStatusPending,
		}},
	}
	accountRepo := &openAI5hWakeAccountRepoStub{accounts: map[int64]*Account{account.ID: account}}
	upstream := &openAI5hWakeObservedUpstream{}
	wake := NewOpenAI5hWakeService(
		taskRepo, accountRepo, nil, NewOpenAITokenProvider(accountRepo, nil, nil),
		upstream, nil, nil, nil,
	)
	cancelResult := make(chan error, 1)
	var cancelOnce sync.Once
	accountRepo.afterUpdate = func(int64, map[string]any) {
		cancelOnce.Do(func() {
			_, err := wake.CancelTask(context.Background(), task.ID)
			cancelResult <- err
		})
	}

	wake.processTask(task)

	select {
	case err := <-cancelResult:
		require.NoError(t, err)
	default:
		t.Fatal("snapshot persistence did not trigger the cancellation race")
	}
	require.Equal(t, OpenAI5hWakeItemStatusWoken, taskRepo.items[0].Status)
	require.Equal(t, 1, task.ProcessedItems)
	require.Equal(t, 1, task.WokenCount)
	require.Zero(t, task.CancelledCount)
	require.Equal(t, OpenAI5hWakeTaskStatusCancelled, task.Status)

	taskRepo.mu.Lock()
	codes := make([]string, 0, len(taskRepo.events))
	for _, event := range taskRepo.events {
		codes = append(codes, event.Code)
	}
	taskRepo.mu.Unlock()
	require.Contains(t, codes, "account_attempt_started")
	require.Contains(t, codes, "wake_request_started")
	require.Contains(t, codes, "wake_request_accepted")
	require.Contains(t, codes, "wake_request_succeeded")
	require.Contains(t, codes, "item_woken")
}

func TestOpenAI5hWakeCancellationStopsDispatchAndCancelsInFlightRequests(t *testing.T) {
	upstream := &openAI5hWakeObservedUpstream{block: true, started: make(chan struct{}, openAI5hWakeConcurrency)}
	wake, repo := newOpenAI5hWakeWorkerFixture(24, upstream)
	done := make(chan struct{})
	go func() {
		wake.processTask(repo.task)
		close(done)
	}()

	for index := 0; index < openAI5hWakeConcurrency; index++ {
		select {
		case <-upstream.started:
		case <-time.After(2 * time.Second):
			t.Fatal("worker requests did not start")
		}
	}
	_, err := wake.CancelTask(context.Background(), repo.task.ID)
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled worker did not stop")
	}

	require.Equal(t, int32(openAI5hWakeConcurrency), upstream.requestCount.Load())
	require.Equal(t, int32(openAI5hWakeConcurrency), upstream.cancelled.Load())
	require.Equal(t, OpenAI5hWakeTaskStatusCancelled, repo.task.Status)
	require.Equal(t, repo.task.TotalItems, repo.task.CancelledCount)
}

func TestOpenAI5hWakeCancelTaskRecordsOnlyTheFirstRequest(t *testing.T) {
	wake, repo := newOpenAI5hWakeWorkerFixture(1, &openAI5hWakeObservedUpstream{})

	first, err := wake.CancelTask(context.Background(), repo.task.ID)
	require.NoError(t, err)
	require.NotNil(t, first.CancelRequestedAt)
	second, err := wake.CancelTask(context.Background(), repo.task.ID)
	require.NoError(t, err)
	require.Equal(t, first.CancelRequestedAt, second.CancelRequestedAt)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	cancelEvents := 0
	for _, event := range repo.events {
		if event.Code == "cancel_requested" {
			cancelEvents++
		}
	}
	require.Equal(t, 1, cancelEvents)
}

func TestOpenAI5hWakeCancelTaskDoesNotAddEventForTerminalTask(t *testing.T) {
	wake, repo := newOpenAI5hWakeWorkerFixture(1, &openAI5hWakeObservedUpstream{})
	repo.task.Status = OpenAI5hWakeTaskStatusSucceeded

	task, err := wake.CancelTask(context.Background(), repo.task.ID)
	require.NoError(t, err)
	require.Equal(t, OpenAI5hWakeTaskStatusSucceeded, task.Status)
	require.Empty(t, repo.events)
}

func sortedMapKeys(values map[string]any) []string {
	keys := reflect.ValueOf(values).MapKeys()
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key.String())
	}
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j] < result[i] {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}
