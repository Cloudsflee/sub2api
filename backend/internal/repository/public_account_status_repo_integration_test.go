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

	createGroup := func(name, status string, public bool, sortOrder int) *dbent.Group {
		group, err := client.Group.Create().
			SetName(name).
			SetPlatform(service.PlatformGemini).
			SetStatus(status).
			SetPublicStatusEnabled(public).
			SetSortOrder(sortOrder).
			Save(ctx)
		require.NoError(t, err)
		return group
	}

	active := createGroup("public-status-active", service.StatusActive, true, 10)
	inactive := createGroup("public-status-inactive", "inactive", true, 20)
	private := createGroup("public-status-private", service.StatusActive, false, 5)

	account, err := client.Account.Create().
		SetName("gemini-public-status@example.com").
		SetPlatform(service.PlatformGemini).
		SetType(service.AccountTypeOAuth).
		SetCredentials(map[string]any{
			"oauth_type":   "google_one",
			"tier_id":      "google_ai_pro",
			"project_id":   "secret-project-id",
			"access_token": "secret-access-token",
		}).
		Save(ctx)
	require.NoError(t, err)
	for _, groupID := range []int64{active.ID, inactive.ID, private.ID} {
		_, err = client.AccountGroup.Create().
			SetAccountID(account.ID).
			SetGroupID(groupID).
			Save(ctx)
		require.NoError(t, err)
	}

	groups, err := repo.ListPublicStatusGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{active.ID, inactive.ID}, []int64{groups[0].ID, groups[1].ID})
	require.Equal(t, "inactive", groups[1].Status, "an inactive group remains visible while explicitly public")

	accounts, total, err := repo.ListPublicStatusAccounts(ctx, inactive.ID, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, accounts, 1)
	require.Nil(t, accounts[0].Credentials, "the public repository must not load credential JSON")
	require.Equal(t, "google_one", accounts[0].GeminiOAuthTypeHint)
	require.Equal(t, "google_ai_pro", accounts[0].GeminiTierIDHint)
	require.True(t, accounts[0].GeminiProjectIDPresent)

	_, _, err = repo.ListPublicStatusAccounts(ctx, private.ID, 0, 20)
	require.ErrorIs(t, err, service.ErrPublicAccountStatusGroupNotFound)

	_, err = client.Group.UpdateOneID(inactive.ID).SetPublicStatusEnabled(false).Save(ctx)
	require.NoError(t, err)
	_, _, err = repo.ListPublicStatusAccounts(ctx, inactive.ID, 0, 20)
	require.ErrorIs(t, err, service.ErrPublicAccountStatusGroupNotFound)

	err = client.Group.DeleteOneID(active.ID).Exec(ctx)
	require.NoError(t, err)
	groups, err = repo.ListPublicStatusGroups(ctx)
	require.NoError(t, err)
	require.Empty(t, groups, "closed and soft-deleted groups disappear from public queries")
	_, _, err = repo.ListPublicStatusAccounts(ctx, active.ID, 0, 20)
	require.ErrorIs(t, err, service.ErrPublicAccountStatusGroupNotFound)
}
