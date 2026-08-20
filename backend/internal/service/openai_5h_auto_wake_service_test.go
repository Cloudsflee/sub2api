package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAI5hAutoWakeAccountRepoStub struct {
	AccountRepository
	accountsByGroup map[int64][]Account
	err             error
	calls           []struct {
		groupID  int64
		platform string
	}
}

func (r *openAI5hAutoWakeAccountRepoStub) ListSchedulableByGroupIDAndPlatform(
	_ context.Context,
	groupID int64,
	platform string,
) ([]Account, error) {
	r.calls = append(r.calls, struct {
		groupID  int64
		platform string
	}{groupID: groupID, platform: platform})
	if r.err != nil {
		return nil, r.err
	}
	return append([]Account(nil), r.accountsByGroup[groupID]...), nil
}

type openAI5hAutoWakeTaskRepoStub struct {
	OpenAI5hWakeTaskRepository
	group        *OpenAI5hAutoWakeGroup
	groups       []OpenAI5hAutoWakeGroup
	listCalls    int
	manual       *OpenAI5hWakeTask
	active       *OpenAI5hWakeTask
	activeResets map[string]time.Time
	resetQueries []time.Time
	createFn     func(OpenAI5hWakeCreateParams) (*OpenAI5hWakeTask, bool, error)
	createCalls  []OpenAI5hWakeCreateParams
	updates      []OpenAI5hAutoWakeCheckUpdate
	events       []OpenAI5hWakeTaskEventParams
}

func (r *openAI5hAutoWakeTaskRepoStub) CreateOrGetActive(
	_ context.Context,
	params OpenAI5hWakeCreateParams,
) (*OpenAI5hWakeTask, bool, error) {
	params.Items = append([]OpenAI5hWakeTaskItemSeed(nil), params.Items...)
	r.createCalls = append(r.createCalls, params)
	if r.createFn != nil {
		return r.createFn(params)
	}
	groupID := *params.GroupID
	return &OpenAI5hWakeTask{
		ID: 77, TriggerType: params.TriggerType, GroupID: &groupID,
		Status:                OpenAI5hWakeTaskStatusPending,
		EligibleAccountCount:  params.EligibleAccountCount,
		ActiveWindowCount:     params.ActiveWindowCount,
		EstimatedRequestCount: params.EstimatedRequestCount,
		TotalItems:            len(params.Items),
	}, true, nil
}

func (r *openAI5hAutoWakeTaskRepoStub) AppendTaskEvent(_ context.Context, event OpenAI5hWakeTaskEventParams) error {
	r.events = append(r.events, event)
	return nil
}

func (r *openAI5hAutoWakeTaskRepoStub) GetActiveManualTask(context.Context) (*OpenAI5hWakeTask, error) {
	return r.manual, nil
}

func (r *openAI5hAutoWakeTaskRepoStub) GetActiveGroupTask(context.Context, int64) (*OpenAI5hWakeTask, error) {
	return r.active, nil
}

func (r *openAI5hAutoWakeTaskRepoStub) ListActiveWakePoolResets(
	_ context.Context,
	_ []string,
	now time.Time,
) (map[string]time.Time, error) {
	r.resetQueries = append(r.resetQueries, now)
	return r.activeResets, nil
}

func (r *openAI5hAutoWakeTaskRepoStub) ListAutoWakeGroups(context.Context) ([]OpenAI5hAutoWakeGroup, error) {
	r.listCalls++
	return append([]OpenAI5hAutoWakeGroup(nil), r.groups...), nil
}

func (r *openAI5hAutoWakeTaskRepoStub) GetAutoWakeGroup(context.Context, int64) (*OpenAI5hAutoWakeGroup, error) {
	return r.group, nil
}

func (r *openAI5hAutoWakeTaskRepoStub) UpdateAutoWakeGroupCheck(
	_ context.Context,
	update OpenAI5hAutoWakeCheckUpdate,
) (bool, error) {
	r.updates = append(r.updates, update)
	return true, nil
}

func (*openAI5hAutoWakeTaskRepoStub) UpdateAutoWakeTaskStatus(context.Context, int64, string) error {
	return nil
}

func trustedOpenAI5hWakeAccount(id int64, identity string, resetAt time.Time) Account {
	account := *newOpenAI5hWakeAccount(id, identity)
	account.Extra["codex_5h_reset_at"] = resetAt.UTC().Format(time.RFC3339)
	account.Extra["codex_5h_used_percent"] = float64(1)
	account.Extra[openAI5hWakeSnapshotIdentityKey] = openAI5hWakeIdentityHash(&account)
	return account
}

func TestOpenAI5hAutoWakeCheckUsesOnlyGroupCandidatesAndExcludesTrustedWindows(t *testing.T) {
	now := time.Now().UTC()
	candidate := *newOpenAI5hWakeAccount(1, "candidate-pool")
	active := trustedOpenAI5hWakeAccount(2, "active-pool", now.Add(4*time.Hour))
	apiKey := *newOpenAI5hWakeAccount(3, "api-key-pool")
	apiKey.Type = AccountTypeAPIKey
	nonGlobal := *newOpenAI5hWakeAccount(4, "spark-pool")
	nonGlobal.QuotaDimension = QuotaDimensionSpark

	accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{
		9: {candidate, active, apiKey, nonGlobal},
	}}
	taskRepo := &openAI5hAutoWakeTaskRepoStub{group: &OpenAI5hAutoWakeGroup{
		ID: 9, Enabled: true, Status: StatusActive,
	}}
	wake := &OpenAI5hWakeService{repo: taskRepo, accountRepo: accountRepo}

	err := wake.CheckGroupNow(context.Background(), 9)

	require.NoError(t, err)
	require.Equal(t, 1, len(accountRepo.calls))
	require.Equal(t, int64(9), accountRepo.calls[0].groupID)
	require.Equal(t, PlatformOpenAI, accountRepo.calls[0].platform)
	require.Len(t, taskRepo.createCalls, 1)
	created := taskRepo.createCalls[0]
	require.Equal(t, OpenAI5hWakeTriggerGroupAuto, created.TriggerType)
	require.Equal(t, int64(9), *created.GroupID)
	require.Equal(t, 1, created.EligibleAccountCount)
	require.Equal(t, 1, created.ActiveWindowCount)
	require.Equal(t, 1, created.EstimatedRequestCount)
	require.Len(t, created.Items, 1)
	require.Equal(t, []int64{1}, created.Items[0].MemberAccountIDs)
	require.Len(t, taskRepo.updates, 1)
	require.Equal(t, OpenAI5hAutoWakeReasonTaskCreated, taskRepo.updates[0].Reason)
	require.Equal(t, 1, taskRepo.updates[0].CandidatePoolCount)
	require.Equal(t, int64(77), *taskRepo.updates[0].TaskID)
	require.Equal(t, OpenAI5hWakeTaskStatusPending, taskRepo.updates[0].TaskStatus)
	require.Equal(t, openAI5hAutoWakeRetryInterval, taskRepo.updates[0].NextCheckAt.Sub(taskRepo.updates[0].CheckedAt))
	require.Len(t, taskRepo.events, 1)
	require.Equal(t, "task_created", taskRepo.events[0].Code)
}

func TestOpenAI5hAutoWakeCheckDoesNotCreateEmptyTask(t *testing.T) {
	now := time.Now().UTC()
	active := trustedOpenAI5hWakeAccount(2, "active-pool", now.Add(4*time.Hour))
	accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{9: {active}}}
	taskRepo := &openAI5hAutoWakeTaskRepoStub{group: &OpenAI5hAutoWakeGroup{
		ID: 9, Enabled: true, Status: StatusActive,
	}}
	wake := &OpenAI5hWakeService{repo: taskRepo, accountRepo: accountRepo}

	err := wake.CheckGroupNow(context.Background(), 9)

	require.NoError(t, err)
	require.Empty(t, taskRepo.createCalls)
	require.Len(t, taskRepo.updates, 1)
	require.Equal(t, OpenAI5hAutoWakeReasonNoCandidate, taskRepo.updates[0].Reason)
	require.Zero(t, taskRepo.updates[0].CandidatePoolCount)
	require.Equal(t, now.Add(4*time.Hour).Truncate(time.Second).Add(openAI5hAutoWakeResetGrace), *taskRepo.updates[0].NextCheckAt)
}

func TestOpenAI5hAutoWakeCheckWithoutAccountsUsesSixHourFallback(t *testing.T) {
	accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{}}
	taskRepo := &openAI5hAutoWakeTaskRepoStub{group: &OpenAI5hAutoWakeGroup{
		ID: 9, Enabled: true, Status: StatusActive,
	}}
	wake := &OpenAI5hWakeService{repo: taskRepo, accountRepo: accountRepo}

	err := wake.CheckGroupNow(context.Background(), 9)

	require.NoError(t, err)
	require.Empty(t, taskRepo.createCalls)
	require.Len(t, taskRepo.updates, 1)
	require.Equal(t, OpenAI5hAutoWakeReasonNoCandidate, taskRepo.updates[0].Reason)
	require.Equal(t, openAI5hAutoWakeNoDataInterval, taskRepo.updates[0].NextCheckAt.Sub(taskRepo.updates[0].CheckedAt))
}

func TestOpenAI5hAutoWakeCheckHonorsRecentTaskReset(t *testing.T) {
	now := time.Now().UTC()
	candidate := *newOpenAI5hWakeAccount(1, "recently-woken-pool")
	identityHash := openAI5hWakeIdentityHash(&candidate)
	accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{
		9: {candidate},
	}}
	taskRepo := &openAI5hAutoWakeTaskRepoStub{
		group:        &OpenAI5hAutoWakeGroup{ID: 9, Enabled: true, Status: StatusActive},
		activeResets: map[string]time.Time{identityHash: now.Add(4 * time.Hour)},
	}
	wake := &OpenAI5hWakeService{repo: taskRepo, accountRepo: accountRepo}

	err := wake.CheckGroupNow(context.Background(), 9)

	require.NoError(t, err)
	require.Empty(t, taskRepo.createCalls)
	require.Len(t, taskRepo.updates, 1)
	require.Equal(t, OpenAI5hAutoWakeReasonNoCandidate, taskRepo.updates[0].Reason)
	require.Equal(t, taskRepo.activeResets[identityHash].Add(openAI5hAutoWakeResetGrace), *taskRepo.updates[0].NextCheckAt)
	require.Equal(t, taskRepo.updates[0].CheckedAt.Add(-openAI5hAutoWakeResetGrace), taskRepo.resetQueries[0])
}

func TestOpenAI5hAutoWakeCheckStopsWhenGroupIsNoLongerEligible(t *testing.T) {
	accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{
		9: {*newOpenAI5hWakeAccount(1, "candidate-pool")},
	}}
	taskRepo := &openAI5hAutoWakeTaskRepoStub{group: &OpenAI5hAutoWakeGroup{
		ID: 9, Enabled: false, Status: StatusActive,
	}}
	wake := &OpenAI5hWakeService{repo: taskRepo, accountRepo: accountRepo}

	err := wake.CheckGroupNow(context.Background(), 9)

	require.NoError(t, err)
	require.Empty(t, accountRepo.calls)
	require.Empty(t, taskRepo.createCalls)
	require.Empty(t, taskRepo.updates)
}

func TestOpenAI5hAutoWakeCheckSkipsActiveTasks(t *testing.T) {
	tests := []struct {
		name       string
		manual     *OpenAI5hWakeTask
		auto       *OpenAI5hWakeTask
		wantReason string
		wantTaskID *int64
	}{
		{
			name: "manual task", manual: &OpenAI5hWakeTask{ID: 10, Status: OpenAI5hWakeTaskStatusRunning},
			wantReason: OpenAI5hAutoWakeReasonSkippedManualActive,
		},
		{
			name: "same group automatic task", auto: &OpenAI5hWakeTask{ID: 11, Status: OpenAI5hWakeTaskStatusPending},
			wantReason: OpenAI5hAutoWakeReasonSkippedAutoActive, wantTaskID: int64Pointer(11),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accountRepo := &openAI5hAutoWakeAccountRepoStub{}
			taskRepo := &openAI5hAutoWakeTaskRepoStub{
				group:  &OpenAI5hAutoWakeGroup{ID: 9, Enabled: true, Status: StatusActive},
				manual: tt.manual, active: tt.auto,
			}
			wake := &OpenAI5hWakeService{repo: taskRepo, accountRepo: accountRepo}

			err := wake.CheckGroupNow(context.Background(), 9)

			require.NoError(t, err)
			require.Empty(t, accountRepo.calls)
			require.Empty(t, taskRepo.createCalls)
			require.Len(t, taskRepo.updates, 1)
			require.Equal(t, tt.wantReason, taskRepo.updates[0].Reason)
			require.Equal(t, tt.wantTaskID, taskRepo.updates[0].TaskID)
			require.Equal(t, openAI5hAutoWakeRetryInterval, taskRepo.updates[0].NextCheckAt.Sub(taskRepo.updates[0].CheckedAt))
		})
	}
}

func TestOpenAI5hAutoWakeCheckHandlesCreationRacesAndErrors(t *testing.T) {
	candidate := *newOpenAI5hWakeAccount(1, "candidate-pool")

	t.Run("manual task wins creation race", func(t *testing.T) {
		accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{9: {candidate}}}
		taskRepo := &openAI5hAutoWakeTaskRepoStub{
			group: &OpenAI5hAutoWakeGroup{ID: 9, Enabled: true, Status: StatusActive},
			createFn: func(OpenAI5hWakeCreateParams) (*OpenAI5hWakeTask, bool, error) {
				return nil, false, ErrOpenAI5hWakeManualTaskActive
			},
		}
		wake := &OpenAI5hWakeService{repo: taskRepo, accountRepo: accountRepo}

		err := wake.CheckGroupNow(context.Background(), 9)

		require.NoError(t, err)
		require.Len(t, taskRepo.updates, 1)
		require.Equal(t, OpenAI5hAutoWakeReasonSkippedManualActive, taskRepo.updates[0].Reason)
		require.Equal(t, 1, taskRepo.updates[0].CandidatePoolCount)
		require.Equal(t, openAI5hAutoWakeRetryInterval, taskRepo.updates[0].NextCheckAt.Sub(taskRepo.updates[0].CheckedAt))
	})

	t.Run("same group task wins creation race", func(t *testing.T) {
		accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{9: {candidate}}}
		taskRepo := &openAI5hAutoWakeTaskRepoStub{
			group: &OpenAI5hAutoWakeGroup{ID: 9, Enabled: true, Status: StatusActive},
			createFn: func(params OpenAI5hWakeCreateParams) (*OpenAI5hWakeTask, bool, error) {
				return &OpenAI5hWakeTask{
					ID: 12, TriggerType: params.TriggerType, GroupID: params.GroupID,
					Status: OpenAI5hWakeTaskStatusRunning,
				}, false, nil
			},
		}
		wake := &OpenAI5hWakeService{repo: taskRepo, accountRepo: accountRepo}

		err := wake.CheckGroupNow(context.Background(), 9)

		require.NoError(t, err)
		require.Len(t, taskRepo.updates, 1)
		require.Equal(t, OpenAI5hAutoWakeReasonSkippedAutoActive, taskRepo.updates[0].Reason)
		require.Equal(t, int64(12), *taskRepo.updates[0].TaskID)
		require.Equal(t, openAI5hAutoWakeRetryInterval, taskRepo.updates[0].NextCheckAt.Sub(taskRepo.updates[0].CheckedAt))
	})

	t.Run("account query failure records short reason code", func(t *testing.T) {
		accountRepo := &openAI5hAutoWakeAccountRepoStub{err: errors.New("query failed")}
		taskRepo := &openAI5hAutoWakeTaskRepoStub{group: &OpenAI5hAutoWakeGroup{
			ID: 9, Enabled: true, Status: StatusActive,
		}}
		wake := &OpenAI5hWakeService{repo: taskRepo, accountRepo: accountRepo}

		err := wake.CheckGroupNow(context.Background(), 9)

		require.EqualError(t, err, "query failed")
		require.Len(t, taskRepo.updates, 1)
		require.Equal(t, OpenAI5hAutoWakeReasonCheckError, taskRepo.updates[0].Reason)
		require.Empty(t, taskRepo.updates[0].TaskStatus)
		require.Equal(t, openAI5hAutoWakeRetryInterval, taskRepo.updates[0].NextCheckAt.Sub(taskRepo.updates[0].CheckedAt))
	})
}

func TestOpenAI5hAutoWakeDueCandidatesUseEarliestTrustedPoolDeadline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	due := *newOpenAI5hWakeAccount(1, "due-pool")
	firstReset := now.Add(time.Hour)
	first := trustedOpenAI5hWakeAccount(2, "first-pool", firstReset)
	secondReset := now.Add(2 * time.Hour)
	second := trustedOpenAI5hWakeAccount(3, "second-pool", secondReset)
	groups := buildOpenAI5hWakeQuotaGroups([]*Account{&due, &first, &second})

	candidates, next := openAI5hAutoWakeDueCandidates(groups, nil, now)

	require.Len(t, candidates, 1)
	require.Equal(t, due.ID, candidates[0].accounts[0].ID)
	require.NotNil(t, next)
	require.Equal(t, firstReset.Add(openAI5hAutoWakeResetGrace), *next)

	historyReset := now.Add(30 * time.Minute)
	candidates, next = openAI5hAutoWakeDueCandidates(groups, map[string]time.Time{
		openAI5hWakeIdentityHash(&first): historyReset,
	}, now)
	require.Len(t, candidates, 1)
	require.NotNil(t, next)
	require.Equal(t, historyReset.Add(openAI5hAutoWakeResetGrace), *next)

	stale := trustedOpenAI5hWakeAccount(4, "shared-pool", now.Add(-time.Hour))
	futureReset := now.Add(time.Hour)
	future := trustedOpenAI5hWakeAccount(5, "shared-pool", futureReset)
	sharedGroups := buildOpenAI5hWakeQuotaGroups([]*Account{&stale, &future})
	candidates, next = openAI5hAutoWakeDueCandidates(sharedGroups, nil, now)
	require.Empty(t, candidates, "an expired member snapshot must not override the pool's current reset")
	require.NotNil(t, next)
	require.Equal(t, futureReset.Add(openAI5hAutoWakeResetGrace), *next)
}

func TestOpenAI5hAutoWakeDueCandidatesHonorResetGrace(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	withinGraceReset := now.Add(-10 * time.Second)
	withinGrace := trustedOpenAI5hWakeAccount(1, "snapshot-within-grace", withinGraceReset)
	dueReset := now.Add(-openAI5hAutoWakeResetGrace)
	due := trustedOpenAI5hWakeAccount(2, "snapshot-due", dueReset)
	groups := buildOpenAI5hWakeQuotaGroups([]*Account{&withinGrace, &due})

	candidates, next := openAI5hAutoWakeDueCandidates(groups, nil, now)

	require.Len(t, candidates, 1)
	require.Equal(t, due.ID, candidates[0].accounts[0].ID)
	require.NotNil(t, next)
	require.Equal(t, withinGraceReset.Add(openAI5hAutoWakeResetGrace), *next)

	historyAccount := *newOpenAI5hWakeAccount(3, "history-within-grace")
	historyGroups := buildOpenAI5hWakeQuotaGroups([]*Account{&historyAccount})
	historyReset := now.Add(-5 * time.Second)
	candidates, next = openAI5hAutoWakeDueCandidates(historyGroups, map[string]time.Time{
		openAI5hWakeIdentityHash(&historyAccount): historyReset,
	}, now)
	require.Empty(t, candidates)
	require.NotNil(t, next)
	require.Equal(t, historyReset.Add(openAI5hAutoWakeResetGrace), *next)
}

func TestOpenAI5hAutoWakeDueScanDoesNotLoadAccountsForFutureGroups(t *testing.T) {
	require.Equal(t, 30*time.Minute, openAI5hAutoWakeCalibrationInterval)
	accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{}}
	taskRepo := &openAI5hAutoWakeTaskRepoStub{
		group:  &OpenAI5hAutoWakeGroup{Enabled: true, Status: StatusActive},
		groups: []OpenAI5hAutoWakeGroup{{ID: 4, Enabled: true, Status: StatusActive}, {ID: 8, Enabled: true, Status: StatusActive}},
	}
	dueRepo := &openAI5hDueScheduleRepo{
		autoWakeGroupLookupRepo: &autoWakeGroupLookupRepo{openAI5hAutoWakeTaskRepoStub: taskRepo},
		dueGroups:               []OpenAI5hAutoWakeGroup{{ID: 4, Enabled: true, Status: StatusActive}},
	}
	wake := &OpenAI5hWakeService{repo: dueRepo, accountRepo: accountRepo}

	err := wake.RunAutoWakeScan(context.Background())

	require.NoError(t, err)
	require.Len(t, accountRepo.calls, 1)
	require.Equal(t, int64(4), accountRepo.calls[0].groupID)
	require.Equal(t, 1, dueRepo.dueCalls)
	require.Zero(t, taskRepo.listCalls, "durable scheduling must not enumerate every eligible group")
	require.Len(t, taskRepo.updates, 1)
	require.Equal(t, int64(4), taskRepo.updates[0].GroupID)
}

func TestOpenAI5hAutoWakeRestartCalibrationReadsPersistedDeadlineOnly(t *testing.T) {
	next := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	accountRepo := &openAI5hAutoWakeAccountRepoStub{}
	base := &openAI5hAutoWakeTaskRepoStub{}
	repo := &openAI5hDueScheduleRepo{
		autoWakeGroupLookupRepo: &autoWakeGroupLookupRepo{openAI5hAutoWakeTaskRepoStub: base},
		nextCheckAt:             &next,
	}
	wake := &OpenAI5hWakeService{repo: repo, accountRepo: accountRepo}

	got, supported, err := wake.nextAutoWakeDeadline()

	require.NoError(t, err)
	require.True(t, supported)
	require.Equal(t, &next, got)
	require.Equal(t, 1, repo.nextCalls)
	require.Empty(t, accountRepo.calls, "deadline calibration must not hydrate account rows")
}

func TestOpenAI5hAutoWakeDueScanHonorsLeaderAndPropagatesQueryFailure(t *testing.T) {
	accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{}}
	base := &openAI5hAutoWakeTaskRepoStub{}
	repo := &openAI5hDueScheduleRepo{
		autoWakeGroupLookupRepo: &autoWakeGroupLookupRepo{openAI5hAutoWakeTaskRepoStub: base},
		dueGroups:               []OpenAI5hAutoWakeGroup{{ID: 4, Enabled: true, Status: StatusActive}},
	}
	cache := &fakeLeaderLockCache{}
	_, acquired := tryAcquireSingletonLeaderLock(context.Background(), cache, nil, openAI5hAutoWakeLeaderLockKey, "peer", time.Minute)
	require.True(t, acquired)
	wake := &OpenAI5hWakeService{repo: repo, accountRepo: accountRepo, lockCache: cache, owner: "worker"}

	require.NoError(t, wake.RunAutoWakeScan(context.Background()))
	require.Zero(t, repo.dueCalls)
	require.Empty(t, accountRepo.calls)
	require.NoError(t, cache.ReleaseLeaderLock(context.Background(), openAI5hAutoWakeLeaderLockKey, "peer"))

	repo.dueErr = errors.New("due query failed")
	err := wake.RunAutoWakeScan(context.Background())
	require.EqualError(t, err, "list OpenAI 5h auto-wake groups: due query failed")
	require.Equal(t, 1, repo.dueCalls)
	require.Empty(t, accountRepo.calls)
}

func TestOpenAI5hAutoWakeExplicitCheckRunsWhilePeerOwnsScanLeader(t *testing.T) {
	accountRepo := &openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{}}
	repo := &openAI5hAutoWakeTaskRepoStub{group: &OpenAI5hAutoWakeGroup{
		ID: 4, Enabled: true, Status: StatusActive,
	}}
	cache := &fakeLeaderLockCache{}
	_, acquired := tryAcquireSingletonLeaderLock(context.Background(), cache, nil, openAI5hAutoWakeLeaderLockKey, "peer", time.Minute)
	require.True(t, acquired)
	t.Cleanup(func() {
		require.NoError(t, cache.ReleaseLeaderLock(context.Background(), openAI5hAutoWakeLeaderLockKey, "peer"))
	})
	wake := &OpenAI5hWakeService{repo: repo, accountRepo: accountRepo, lockCache: cache, owner: "worker"}

	require.NoError(t, wake.CheckGroupNow(context.Background(), 4))
	require.Len(t, accountRepo.calls, 1)
	require.Len(t, repo.updates, 1)
	require.Equal(t, int64(4), repo.updates[0].GroupID)
}

func TestOpenAI5hAutoWakeTriggerGroupCheckRunsImmediatelyAndStops(t *testing.T) {
	baseRepo := &openAI5hAutoWakeTaskRepoStub{group: &OpenAI5hAutoWakeGroup{
		ID: 15, Enabled: true, Status: StatusActive,
	}}
	repo := &openAI5hAutoWakeRuntimeRepo{
		openAI5hAutoWakeTaskRepoStub: baseRepo,
		checked:                      make(chan OpenAI5hAutoWakeCheckUpdate, 1),
	}
	wake := NewOpenAI5hWakeService(
		repo,
		&openAI5hAutoWakeAccountRepoStub{accountsByGroup: map[int64][]Account{}},
		nil, nil, nil, nil, nil, nil,
	)
	wake.Start()
	t.Cleanup(wake.Stop)

	wake.TriggerGroupCheck(15)

	select {
	case update := <-repo.checked:
		require.Equal(t, int64(15), update.GroupID)
		require.Equal(t, OpenAI5hAutoWakeReasonNoCandidate, update.Reason)
	case <-time.After(2 * time.Second):
		t.Fatal("immediate OpenAI 5h group check was not processed")
	}
	wake.Stop()
}

type autoWakeGroupLookupRepo struct {
	*openAI5hAutoWakeTaskRepoStub
}

func (r *autoWakeGroupLookupRepo) GetAutoWakeGroup(_ context.Context, groupID int64) (*OpenAI5hAutoWakeGroup, error) {
	return &OpenAI5hAutoWakeGroup{ID: groupID, Enabled: true, Status: StatusActive}, nil
}

type openAI5hDueScheduleRepo struct {
	*autoWakeGroupLookupRepo
	dueGroups   []OpenAI5hAutoWakeGroup
	dueErr      error
	dueCalls    int
	nextCheckAt *time.Time
	nextErr     error
	nextCalls   int
}

func (r *openAI5hDueScheduleRepo) ListDueAutoWakeGroups(context.Context, time.Time) ([]OpenAI5hAutoWakeGroup, error) {
	r.dueCalls++
	return append([]OpenAI5hAutoWakeGroup(nil), r.dueGroups...), r.dueErr
}

func (r *openAI5hDueScheduleRepo) GetNextAutoWakeCheckAt(context.Context) (*time.Time, error) {
	r.nextCalls++
	if r.nextCheckAt == nil {
		return nil, r.nextErr
	}
	next := *r.nextCheckAt
	return &next, r.nextErr
}

type openAI5hAutoWakeRuntimeRepo struct {
	*openAI5hAutoWakeTaskRepoStub
	checked chan OpenAI5hAutoWakeCheckUpdate
}

func (*openAI5hAutoWakeRuntimeRepo) ClaimTask(context.Context, string, time.Time, time.Time) (*OpenAI5hWakeTask, error) {
	return nil, nil
}

func (*openAI5hAutoWakeRuntimeRepo) DeleteTerminalBefore(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (r *openAI5hAutoWakeRuntimeRepo) UpdateAutoWakeGroupCheck(
	_ context.Context,
	update OpenAI5hAutoWakeCheckUpdate,
) (bool, error) {
	select {
	case r.checked <- update:
	default:
	}
	return true, nil
}

func int64Pointer(value int64) *int64 {
	return &value
}
