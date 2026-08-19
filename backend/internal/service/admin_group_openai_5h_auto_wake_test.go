package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type adminOpenAI5hGroupRepoStub struct {
	AdminGroupRepository
	current *Group
	created *Group
	updated *Group
}

func (r *adminOpenAI5hGroupRepoStub) Create(_ context.Context, group *Group) error {
	group.ID = 41
	copyGroup := *group
	r.created = &copyGroup
	return nil
}

func (r *adminOpenAI5hGroupRepoStub) GetByID(_ context.Context, _ int64) (*Group, error) {
	copyGroup := *r.current
	return &copyGroup, nil
}

func (r *adminOpenAI5hGroupRepoStub) Update(_ context.Context, group *Group) error {
	copyGroup := *group
	r.updated = &copyGroup
	return nil
}

type openAI5hAutoWakeCheckerSpy struct {
	groupIDs []int64
}

func (s *openAI5hAutoWakeCheckerSpy) TriggerGroupCheck(groupID int64) {
	s.groupIDs = append(s.groupIDs, groupID)
}

func TestCreateGroupOpenAI5hAutoWakeDefaultsOffAndIsOpenAIOnly(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		requested   bool
		wantEnabled bool
		wantChecks  []int64
	}{
		{name: "OpenAI default is off", platform: PlatformOpenAI},
		{name: "OpenAI enabled queues immediate check", platform: PlatformOpenAI, requested: true, wantEnabled: true, wantChecks: []int64{41}},
		{name: "non-OpenAI request is forced off", platform: PlatformAnthropic, requested: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &adminOpenAI5hGroupRepoStub{}
			checker := &openAI5hAutoWakeCheckerSpy{}
			svc := &adminServiceImpl{groupRepo: repo, openAI5hAutoWakeChecker: checker}

			group, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
				Name: "wake-group", Platform: tt.platform, RateMultiplier: 1,
				OpenAI5hAutoWakeEnabled: tt.requested,
			})

			require.NoError(t, err)
			require.Equal(t, tt.wantEnabled, group.OpenAI5hAutoWakeEnabled)
			require.Equal(t, tt.wantEnabled, repo.created.OpenAI5hAutoWakeEnabled)
			require.Equal(t, tt.wantChecks, checker.groupIDs)
		})
	}
}

func TestUpdateGroupOpenAI5hAutoWakeCoercionAndImmediateCheck(t *testing.T) {
	checkedAt := time.Now().UTC().Add(-time.Minute)
	candidateCount := 2
	taskID := int64(91)
	baseGroup := func() *Group {
		return &Group{
			ID: 7, Name: "wake-group", Platform: PlatformOpenAI, Status: StatusActive,
			RateMultiplier: 1, SubscriptionType: SubscriptionTypeStandard,
			OpenAI5hAutoWakeLastCheckedAt:          &checkedAt,
			OpenAI5hAutoWakeLastCandidatePoolCount: &candidateCount,
			OpenAI5hAutoWakeLastReason:             OpenAI5hAutoWakeReasonTaskCreated,
			OpenAI5hAutoWakeLastTaskID:             &taskID,
			OpenAI5hAutoWakeLastTaskStatus:         OpenAI5hWakeTaskStatusSucceeded,
		}
	}
	enabled := true

	t.Run("active OpenAI save queues immediate check", func(t *testing.T) {
		repo := &adminOpenAI5hGroupRepoStub{current: baseGroup()}
		checker := &openAI5hAutoWakeCheckerSpy{}
		svc := &adminServiceImpl{groupRepo: repo, openAI5hAutoWakeChecker: checker}

		group, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{OpenAI5hAutoWakeEnabled: &enabled})

		require.NoError(t, err)
		require.True(t, group.OpenAI5hAutoWakeEnabled)
		require.Equal(t, []int64{7}, checker.groupIDs)
		require.Equal(t, &checkedAt, repo.updated.OpenAI5hAutoWakeLastCheckedAt)
		require.Equal(t, &taskID, repo.updated.OpenAI5hAutoWakeLastTaskID)
	})

	t.Run("inactive group preserves switch but pauses checks", func(t *testing.T) {
		repo := &adminOpenAI5hGroupRepoStub{current: baseGroup()}
		checker := &openAI5hAutoWakeCheckerSpy{}
		svc := &adminServiceImpl{groupRepo: repo, openAI5hAutoWakeChecker: checker}

		group, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{
			Status: "inactive", OpenAI5hAutoWakeEnabled: &enabled,
		})

		require.NoError(t, err)
		require.True(t, group.OpenAI5hAutoWakeEnabled)
		require.Empty(t, checker.groupIDs)
	})

	t.Run("platform change forces switch off", func(t *testing.T) {
		group := baseGroup()
		group.OpenAI5hAutoWakeEnabled = true
		repo := &adminOpenAI5hGroupRepoStub{current: group}
		checker := &openAI5hAutoWakeCheckerSpy{}
		svc := &adminServiceImpl{groupRepo: repo, openAI5hAutoWakeChecker: checker}

		updated, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{
			Platform: PlatformAnthropic, OpenAI5hAutoWakeEnabled: &enabled,
		})

		require.NoError(t, err)
		require.False(t, updated.OpenAI5hAutoWakeEnabled)
		require.False(t, repo.updated.OpenAI5hAutoWakeEnabled)
		require.Empty(t, checker.groupIDs)
	})
}
