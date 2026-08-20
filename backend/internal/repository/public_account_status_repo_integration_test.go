//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPublicAccountStatusRepositoryVisibilityLifecycle(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := &publicAccountStatusRepository{client: client}

	createGroup := func(name, platform, status string, public bool, sortOrder int) *dbent.Group {
		group, err := client.Group.Create().
			SetName(name).
			SetPlatform(platform).
			SetStatus(status).
			SetPublicStatusEnabled(public).
			SetSortOrder(sortOrder).
			Save(ctx)
		require.NoError(t, err)
		return group
	}

	active := createGroup("public-status-active", service.PlatformOpenAI, service.StatusActive, true, 10)
	inactive := createGroup("public-status-inactive", service.PlatformOpenAI, "inactive", true, 20)
	private := createGroup("public-status-private", service.PlatformOpenAI, service.StatusActive, false, 5)
	wrongPlatform := createGroup("public-status-wrong-platform", service.PlatformGemini, service.StatusActive, true, 30)
	all := createGroup("ALL", service.PlatformOpenAI, service.StatusActive, true, 40)

	account, err := client.Account.Create().
		SetName("openai-public-status@example.com").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeOAuth).
		SetCredentials(map[string]any{
			"access_token": "secret-access-token",
		}).
		Save(ctx)
	require.NoError(t, err)
	allGroupIDs := []int64{active.ID, inactive.ID, private.ID, wrongPlatform.ID, all.ID}
	for _, groupID := range allGroupIDs {
		_, err = client.AccountGroup.Create().
			SetAccountID(account.ID).
			SetGroupID(groupID).
			Save(ctx)
		require.NoError(t, err)
	}

	groups, err := repo.ListPublicStatusGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{active.ID}, publicStatusGroupRecordIDs(groups))

	memberships, err := repo.ListPublicStatusGroupAccounts(ctx, allGroupIDs)
	require.NoError(t, err)
	require.Equal(t, []int64{active.ID}, publicStatusMembershipGroupIDs(memberships))

	accounts, total, err := repo.ListPublicStatusAccounts(ctx, active.ID, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, accounts, 1)
	require.Nil(t, accounts[0].Credentials, "the public repository must not load credential JSON")

	for _, hiddenID := range []int64{inactive.ID, private.ID, wrongPlatform.ID, all.ID} {
		_, _, err = repo.ListPublicStatusAccounts(ctx, hiddenID, 0, 20)
		require.ErrorIs(t, err, service.ErrPublicAccountStatusGroupNotFound)
	}

	_, err = client.Group.UpdateOneID(active.ID).SetStatus("inactive").Save(ctx)
	require.NoError(t, err)
	_, _, err = repo.ListPublicStatusAccounts(ctx, active.ID, 0, 20)
	require.ErrorIs(t, err, service.ErrPublicAccountStatusGroupNotFound)

	_, err = client.Group.UpdateOneID(active.ID).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	groups, err = repo.ListPublicStatusGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{active.ID}, publicStatusGroupRecordIDs(groups), "reactivation restores the retained public switch")

	_, err = client.Group.UpdateOneID(active.ID).SetPublicStatusEnabled(false).Save(ctx)
	require.NoError(t, err)
	_, _, err = repo.ListPublicStatusAccounts(ctx, active.ID, 0, 20)
	require.ErrorIs(t, err, service.ErrPublicAccountStatusGroupNotFound)
	_, err = client.Group.UpdateOneID(active.ID).SetPublicStatusEnabled(true).Save(ctx)
	require.NoError(t, err)

	err = client.Group.DeleteOneID(active.ID).Exec(ctx)
	require.NoError(t, err)
	groups, err = repo.ListPublicStatusGroups(ctx)
	require.NoError(t, err)
	require.Empty(t, groups, "closed and soft-deleted groups disappear from public queries")
	_, _, err = repo.ListPublicStatusAccounts(ctx, active.ID, 0, 20)
	require.ErrorIs(t, err, service.ErrPublicAccountStatusGroupNotFound)
}

func publicStatusGroupRecordIDs(groups []service.PublicStatusGroupRecord) []int64 {
	ids := make([]int64, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	return ids
}

func publicStatusMembershipGroupIDs(rows []service.PublicStatusGroupAccountRecord) []int64 {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.GroupID)
	}
	return ids
}
