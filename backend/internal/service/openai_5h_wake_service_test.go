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
	mu            sync.Mutex
	listed        []Account
	accounts      map[int64]*Account
	updates       map[int64][]map[string]any
	updateErrByID map[int64]error
}

func (r *openAI5hWakeAccountRepoStub) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
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
	defer r.mu.Unlock()
	if err := r.updateErrByID[id]; err != nil {
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
}

func (r *openAI5hWakeWorkerRepo) AppendTaskEvent(_ context.Context, params OpenAI5hWakeTaskEventParams) error {
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

func (r *openAI5hWakeWorkerRepo) RecoverTaskItems(_ context.Context, _ int64, _ string, maxAttempts int) (int, error) {
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

func (r *openAI5hWakeWorkerRepo) HeartbeatTask(context.Context, int64, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

func (r *openAI5hWakeWorkerRepo) IsCancelRequested(context.Context, int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	if cancelled {
		for _, item := range r.items {
			if item.Status == OpenAI5hWakeItemStatusPending || item.Status == OpenAI5hWakeItemStatusRunning {
				item.Status = OpenAI5hWakeItemStatusCancelled
				r.task.CancelledCount++
				r.task.ProcessedItems++
			}
		}
		r.task.Status = OpenAI5hWakeTaskStatusCancelled
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

	repo := &openAI5hWakeAccountRepoStub{listed: []Account{
		sharedA, sharedB, active, apiKey, nonOAuth, shadow, nonGlobal, disabled,
		unschedulable, expiredAccount, rateLimited, cooling, missingIdentity,
	}}
	service := &OpenAI5hWakeService{accountRepo: repo}

	plan, err := service.buildPlan(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, 13, plan.preview.TotalOpenAIAccounts)
	require.Equal(t, 3, plan.preview.EligibleAccounts)
	require.Equal(t, 3, plan.preview.UniqueQuotaPools)
	require.Equal(t, 1, plan.preview.ActiveWindows)
	require.Equal(t, 2, plan.preview.EstimatedRequests)
	require.Equal(t, OpenAI5hWakeExclusions{
		APIKey: 1, NonOAuth: 1, SparkShadow: 1, NonGlobal: 1, Disabled: 1,
		Unschedulable: 1, Expired: 1, RateLimited: 1, CoolingDown: 1, MissingIdentity: 1,
	}, plan.preview.Excluded)
	require.Equal(t, int64(1), plan.groups[0].accounts[0].ID)
	require.Equal(t, int64(2), plan.groups[1].accounts[0].ID)
	require.Len(t, plan.groups[0].identityHash, 64)
	require.NotContains(t, plan.groups[0].identityHash, "shared-pool")
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
