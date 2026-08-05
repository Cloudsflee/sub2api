package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAI5hWakeIdentityUpdateRepo struct {
	AccountRepository
	account       *Account
	updated       *Account
	bulkUpdates   AccountBulkUpdate
	bulkUpdateIDs []int64
}

func (r *openAI5hWakeIdentityUpdateRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	return r.account, nil
}

func (r *openAI5hWakeIdentityUpdateRepo) Update(_ context.Context, account *Account) error {
	r.updated = account
	r.account = account
	return nil
}

func (r *openAI5hWakeIdentityUpdateRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	if r.account == nil {
		return nil, nil
	}
	for _, id := range ids {
		if id == r.account.ID {
			return []*Account{r.account}, nil
		}
	}
	return nil, nil
}

func (r *openAI5hWakeIdentityUpdateRepo) BulkUpdate(_ context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	r.bulkUpdateIDs = append([]int64(nil), ids...)
	r.bulkUpdates = updates
	return int64(len(ids)), nil
}

func newMarkedOpenAI5hWakeIdentityAccount() *Account {
	account := newOpenAI5hWakeAccount(1, "account-before")
	account.Extra[openAI5hWakeSnapshotIdentityKey] = openAI5hWakeIdentityHash(account)
	return account
}

func TestUpdateAccountClearsWakeMarkerWhenIdentityChanges(t *testing.T) {
	repo := &openAI5hWakeIdentityUpdateRepo{account: newMarkedOpenAI5hWakeIdentityAccount()}
	service := &adminServiceImpl{accountRepo: repo}

	_, err := service.UpdateAccount(context.Background(), repo.account.ID, &UpdateAccountInput{
		Credentials: map[string]any{"chatgpt_account_id": "account-after"},
	})

	require.NoError(t, err)
	require.NotContains(t, repo.updated.Extra, openAI5hWakeSnapshotIdentityKey)
}

func TestUpdateAccountKeepsWakeMarkerWhenIdentityIsUnchanged(t *testing.T) {
	repo := &openAI5hWakeIdentityUpdateRepo{account: newMarkedOpenAI5hWakeIdentityAccount()}
	service := &adminServiceImpl{accountRepo: repo}

	_, err := service.UpdateAccount(context.Background(), repo.account.ID, &UpdateAccountInput{
		Credentials: map[string]any{
			"access_token":       "rotated-token",
			"chatgpt_account_id": "account-before",
		},
	})

	require.NoError(t, err)
	require.Equal(t, openAI5hWakeIdentityHash(repo.updated), repo.updated.Extra[openAI5hWakeSnapshotIdentityKey])
}

func TestBulkUpdateAccountsClearsWakeMarkerForIdentityCredential(t *testing.T) {
	repo := &openAI5hWakeIdentityUpdateRepo{account: newMarkedOpenAI5hWakeIdentityAccount()}
	service := &adminServiceImpl{accountRepo: repo}

	result, err := service.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs:  []int64{repo.account.ID},
		Credentials: map[string]any{"organization_id": "org-after"},
		Extra:       map[string]any{openAI5hWakeSnapshotIdentityKey: "forged"},
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Success)
	require.Equal(t, []int64{repo.account.ID}, repo.bulkUpdateIDs)
	require.Contains(t, repo.bulkUpdates.Extra, openAI5hWakeSnapshotIdentityKey)
	require.Nil(t, repo.bulkUpdates.Extra[openAI5hWakeSnapshotIdentityKey])
}

func TestAccountServiceUpdateClearsWakeMarkerWhenIdentityChanges(t *testing.T) {
	repo := &openAI5hWakeIdentityUpdateRepo{account: newMarkedOpenAI5hWakeIdentityAccount()}
	service := NewAccountService(repo, nil)
	credentials := map[string]any{"chatgpt_account_id": "account-after"}

	_, err := service.Update(context.Background(), repo.account.ID, UpdateAccountRequest{Credentials: &credentials})

	require.NoError(t, err)
	require.NotContains(t, repo.updated.Extra, openAI5hWakeSnapshotIdentityKey)
}

func TestAccountServiceUpdateRejectsForgedMarkerAndPreservesTrustedMarker(t *testing.T) {
	repo := &openAI5hWakeIdentityUpdateRepo{account: newMarkedOpenAI5hWakeIdentityAccount()}
	service := NewAccountService(repo, nil)
	extra := map[string]any{
		openAI5hWakeSnapshotIdentityKey: "forged",
		"operator_note":                 "kept",
	}

	_, err := service.Update(context.Background(), repo.account.ID, UpdateAccountRequest{Extra: &extra})

	require.NoError(t, err)
	require.Equal(t, "kept", repo.updated.Extra["operator_note"])
	require.Equal(t, openAI5hWakeIdentityHash(repo.updated), repo.updated.Extra[openAI5hWakeSnapshotIdentityKey])
}
