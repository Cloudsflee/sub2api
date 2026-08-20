package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type adminOpenAI5hAccountRepoSpy struct {
	AccountRepository
	accounts      map[int64]*Account
	created       *Account
	updated       *Account
	bound         map[int64][]int64
	bindErr       error
	forcePlatform string
	getCalls      int
	bulkUpdateIDs []int64
	bulkUpdateErr error
}

func (r *adminOpenAI5hAccountRepoSpy) Create(_ context.Context, account *Account) error {
	if r.created != nil {
		return errors.New("create called twice")
	}
	account.ID = 101
	r.created = account
	if r.accounts == nil {
		r.accounts = make(map[int64]*Account)
	}
	r.accounts[account.ID] = account
	return nil
}

func (r *adminOpenAI5hAccountRepoSpy) BindGroups(_ context.Context, accountID int64, groupIDs []int64) error {
	if r.bindErr != nil {
		return r.bindErr
	}
	if r.bound == nil {
		r.bound = make(map[int64][]int64)
	}
	r.bound[accountID] = append([]int64(nil), groupIDs...)
	if account := r.accounts[accountID]; account != nil {
		account.GroupIDs = append([]int64(nil), groupIDs...)
	}
	return nil
}

func (r *adminOpenAI5hAccountRepoSpy) GetByID(_ context.Context, id int64) (*Account, error) {
	r.getCalls++
	account := r.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	copyAccount := *account
	copyAccount.GroupIDs = append([]int64(nil), account.GroupIDs...)
	if r.forcePlatform != "" && r.getCalls > 1 {
		copyAccount.Platform = r.forcePlatform
	}
	return &copyAccount, nil
}

func (r *adminOpenAI5hAccountRepoSpy) Update(_ context.Context, account *Account) error {
	r.updated = account
	if r.accounts == nil {
		r.accounts = make(map[int64]*Account)
	}
	r.accounts[account.ID] = account
	return nil
}

func (r *adminOpenAI5hAccountRepoSpy) BulkUpdate(_ context.Context, ids []int64, _ AccountBulkUpdate) (int64, error) {
	r.bulkUpdateIDs = append([]int64(nil), ids...)
	if r.bulkUpdateErr != nil {
		return 0, r.bulkUpdateErr
	}
	return int64(len(ids)), nil
}

func (r *adminOpenAI5hAccountRepoSpy) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	accounts := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if account := r.accounts[id]; account != nil {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func TestCreateAccountQueuesOpenAIGroupChecksOnlyAfterBinding(t *testing.T) {
	checker := &openAI5hAutoWakeCheckerSpy{}
	repo := &adminOpenAI5hAccountRepoSpy{}
	svc := &adminServiceImpl{accountRepo: repo, openAI5hAutoWakeChecker: checker}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                  "openai-account",
		Platform:              PlatformOpenAI,
		Type:                  AccountTypeAPIKey,
		GroupIDs:              []int64{4, 4, 9},
		SkipDefaultGroupBind:  true,
		SkipMixedChannelCheck: true,
	})

	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, []int64{4, 4, 9}, repo.bound[account.ID])
	require.Equal(t, []int64{4, 9}, checker.groupIDs)
}

func TestCreateAccountBindingFailureDoesNotQueueOpenAIGroupCheck(t *testing.T) {
	checker := &openAI5hAutoWakeCheckerSpy{}
	repo := &adminOpenAI5hAccountRepoSpy{bindErr: errors.New("bind failed")}
	svc := &adminServiceImpl{accountRepo: repo, openAI5hAutoWakeChecker: checker}

	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                  "openai-account",
		Platform:              PlatformOpenAI,
		Type:                  AccountTypeAPIKey,
		GroupIDs:              []int64{4},
		SkipDefaultGroupBind:  true,
		SkipMixedChannelCheck: true,
	})

	require.EqualError(t, err, "bind failed")
	require.Empty(t, checker.groupIDs)
}

func TestUpdateAccountQueuesOldAndNewOpenAIGroupsWithoutDuplicates(t *testing.T) {
	checker := &openAI5hAutoWakeCheckerSpy{}
	account := &Account{ID: 7, Name: "before", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, GroupIDs: []int64{4, 9}}
	repo := &adminOpenAI5hAccountRepoSpy{accounts: map[int64]*Account{account.ID: account}}
	svc := &adminServiceImpl{accountRepo: repo, openAI5hAutoWakeChecker: checker}

	_, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Name: "after"})

	require.NoError(t, err)
	require.Equal(t, []int64{4, 9}, checker.groupIDs)
}

func TestUpdateAccountPlatformChangeQueuesFormerOpenAIGroups(t *testing.T) {
	checker := &openAI5hAutoWakeCheckerSpy{}
	account := &Account{ID: 8, Name: "before", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, GroupIDs: []int64{12}}
	repo := &adminOpenAI5hAccountRepoSpy{accounts: map[int64]*Account{account.ID: account}, forcePlatform: PlatformAnthropic}
	svc := &adminServiceImpl{accountRepo: repo, openAI5hAutoWakeChecker: checker}

	_, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Name: "after"})

	require.NoError(t, err)
	require.Equal(t, []int64{12}, checker.groupIDs)
}

func TestBulkUpdateAccountsQueuesChecksOnlyForOpenAITargets(t *testing.T) {
	checker := &openAI5hAutoWakeCheckerSpy{}
	openAI := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, GroupIDs: []int64{3, 3}}
	anthropic := &Account{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeOAuth, GroupIDs: []int64{6}}
	repo := &adminOpenAI5hAccountRepoSpy{accounts: map[int64]*Account{
		openAI.ID: openAI, anthropic.ID: anthropic,
	}}
	svc := &adminServiceImpl{accountRepo: repo, openAI5hAutoWakeChecker: checker}

	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{openAI.ID, anthropic.ID}, Status: StatusActive,
	})

	require.NoError(t, err)
	require.Equal(t, 2, result.Success)
	require.Equal(t, []int64{openAI.ID, anthropic.ID}, repo.bulkUpdateIDs)
	require.Equal(t, []int64{3}, checker.groupIDs)
}
