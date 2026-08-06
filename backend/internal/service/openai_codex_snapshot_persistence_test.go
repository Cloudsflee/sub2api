package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAICodexSnapshotCASCall struct {
	account  *Account
	ordinary map[string]any
	managed  map[string]any
	updates  map[string]any
}

type openAICodexSnapshotCASRepo struct {
	AccountRepository
	mu               sync.Mutex
	applied          bool
	casCalls         int
	updateExtraCalls int
	callCh           chan openAICodexSnapshotCASCall
	accountCh        chan *Account
	releaseCh        chan struct{}
}

func (r *openAICodexSnapshotCASRepo) UpdateOpenAICodexSnapshot(
	_ context.Context,
	_ int64,
	account *Account,
	ordinaryUpdates map[string]any,
	managedUpdates map[string]any,
) (bool, error) {
	r.mu.Lock()
	r.casCalls++
	r.mu.Unlock()
	if r.accountCh != nil {
		r.accountCh <- account
		<-r.releaseCh
	}
	call := openAICodexSnapshotCASCall{
		account:  cloneOpenAICodexSnapshotIdentity(account),
		ordinary: make(map[string]any, len(ordinaryUpdates)),
		managed:  make(map[string]any, len(managedUpdates)),
		updates:  make(map[string]any, len(ordinaryUpdates)+len(managedUpdates)),
	}
	for key, value := range ordinaryUpdates {
		call.ordinary[key] = value
		call.updates[key] = value
	}
	for key, value := range managedUpdates {
		call.managed[key] = value
		call.updates[key] = value
	}
	if r.callCh != nil {
		r.callCh <- call
	}
	return r.applied, nil
}

func (r *openAICodexSnapshotCASRepo) UpdateExtra(_ context.Context, _ int64, _ map[string]any) error {
	r.mu.Lock()
	r.updateExtraCalls++
	r.mu.Unlock()
	return nil
}

func (r *openAICodexSnapshotCASRepo) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.casCalls, r.updateExtraCalls
}

func openAICodexSnapshotTestAccount(id int64) *Account {
	return &Account{
		ID:             id,
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		QuotaDimension: QuotaDimensionGlobal,
		Credentials: map[string]any{
			"chatgpt_account_id": "workspace-old",
			"organization_id":    "org-old",
			"chatgpt_user_id":    "user-old",
		},
	}
}

func TestPersistOpenAICodexSnapshotForAccountUsesIdentityCAS(t *testing.T) {
	t.Parallel()
	repo := &openAICodexSnapshotCASRepo{applied: true}
	account := openAICodexSnapshotTestAccount(71)

	err := persistOpenAICodexSnapshotForAccount(context.Background(), repo, account, map[string]any{
		"codex_5h_used_percent": 12.0,
	})
	require.NoError(t, err)
	casCalls, updateExtraCalls := repo.counts()
	require.Equal(t, 1, casCalls)
	require.Zero(t, updateExtraCalls)
}

func TestPersistOpenAICodexSnapshotForAccountRejectsIdentityConflict(t *testing.T) {
	t.Parallel()
	repo := &openAICodexSnapshotCASRepo{applied: false}
	account := openAICodexSnapshotTestAccount(72)

	err := persistOpenAICodexSnapshotForAccount(context.Background(), repo, account, map[string]any{
		"codex_5h_used_percent": 99.0,
	})
	require.ErrorIs(t, err, errOpenAICodexSnapshotIdentityChanged)
	casCalls, updateExtraCalls := repo.counts()
	require.Equal(t, 1, casCalls)
	require.Zero(t, updateExtraCalls)
}

func TestPersistOpenAICodexSnapshotForAccountAtomicallySeparatesOrdinaryAndManagedUpdates(t *testing.T) {
	repo := &openAICodexSnapshotCASRepo{
		applied: true,
		callCh:  make(chan openAICodexSnapshotCASCall, 2),
	}
	account := openAICodexSnapshotTestAccount(77)
	observedAt := "01775582400999999999"

	err := persistOpenAICodexSnapshotForAccount(context.Background(), repo, account, map[string]any{
		"openai_compact_supported":            true,
		"openai_compact_last_status":          200,
		"codex_5h_used_percent":               19.0,
		OpenAICodexSnapshotObservedAtExtraKey: observedAt,
	})
	require.NoError(t, err)

	call := <-repo.callCh
	require.Equal(t, map[string]any{
		"openai_compact_supported":   true,
		"openai_compact_last_status": 200,
	}, call.ordinary)
	require.Equal(t, map[string]any{
		"codex_5h_used_percent":               19.0,
		OpenAICodexSnapshotObservedAtExtraKey: observedAt,
	}, call.managed)
	casCalls, updateExtraCalls := repo.counts()
	require.Equal(t, 1, casCalls)
	require.Zero(t, updateExtraCalls)
}

func TestPersistOpenAICodexSnapshotForAccountKeepsShadowPath(t *testing.T) {
	t.Parallel()
	parentID := int64(1)
	repo := &openAICodexSnapshotCASRepo{applied: true}
	account := openAICodexSnapshotTestAccount(73)
	account.ParentAccountID = &parentID
	account.QuotaDimension = QuotaDimensionSpark

	err := persistOpenAICodexSnapshotForAccount(context.Background(), repo, account, map[string]any{
		"codex_5h_used_percent": 23.0,
	})
	require.NoError(t, err)
	casCalls, updateExtraCalls := repo.counts()
	require.Zero(t, casCalls)
	require.Equal(t, 1, updateExtraCalls)
}

func TestOpenAIGatewayCodexSnapshotCapturesIdentityBeforeAsyncWrite(t *testing.T) {
	t.Parallel()
	repo := &openAICodexSnapshotCASRepo{
		applied: true,
		callCh:  make(chan openAICodexSnapshotCASCall, 1),
	}
	service := &OpenAIGatewayService{accountRepo: repo}
	account := openAICodexSnapshotTestAccount(74)
	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:       ptrFloat64WS(10),
		PrimaryWindowMinutes:     ptrIntWS(300),
		PrimaryResetAfterSeconds: ptrIntWS(600),
	}

	service.updateCodexUsageSnapshot(context.Background(), account, snapshot)
	account.Credentials["chatgpt_account_id"] = "workspace-new"
	account.Credentials["organization_id"] = "org-new"
	account.Credentials["chatgpt_user_id"] = "user-new"

	select {
	case call := <-repo.callCh:
		require.Equal(t, "workspace-old", call.account.GetCredential("chatgpt_account_id"))
		require.Equal(t, "org-old", call.account.GetCredential("organization_id"))
		require.Equal(t, "user-old", call.account.GetCredential("chatgpt_user_id"))
		require.Equal(t, 10.0, call.updates["codex_5h_used_percent"])
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for asynchronous Codex snapshot CAS")
	}
	casCalls, updateExtraCalls := repo.counts()
	require.Equal(t, 1, casCalls)
	require.Zero(t, updateExtraCalls)
}

func TestAccountUsageCodexProbePreservesIdentityConflict(t *testing.T) {
	t.Parallel()
	repo := &openAICodexSnapshotCASRepo{applied: false}
	service := &AccountUsageService{accountRepo: repo}
	account := openAICodexSnapshotTestAccount(75)

	err := service.persistOpenAICodexProbeSnapshot(context.Background(), account, map[string]any{
		"codex_5h_used_percent": 81.0,
	})
	require.True(t, errors.Is(err, errOpenAICodexSnapshotIdentityChanged))
	_, updateExtraCalls := repo.counts()
	require.Zero(t, updateExtraCalls)
}

func TestAccountUsageCodexProbeCapturesIdentityBeforePersistence(t *testing.T) {
	repo := &openAICodexSnapshotCASRepo{
		applied:   true,
		accountCh: make(chan *Account, 1),
		releaseCh: make(chan struct{}),
		callCh:    make(chan openAICodexSnapshotCASCall, 1),
	}
	service := &AccountUsageService{accountRepo: repo}
	account := openAICodexSnapshotTestAccount(751)
	done := make(chan error, 1)
	go func() {
		done <- service.persistOpenAICodexProbeSnapshot(context.Background(), account, map[string]any{
			"codex_5h_used_percent": 81.0,
		})
	}()

	captured := <-repo.accountCh
	account.Credentials["chatgpt_account_id"] = "workspace-new"
	account.Credentials["organization_id"] = "org-new"
	account.Credentials["chatgpt_user_id"] = "user-new"
	close(repo.releaseCh)

	require.NoError(t, <-done)
	require.Equal(t, "workspace-old", captured.GetCredential("chatgpt_account_id"))
	require.Equal(t, "org-old", captured.GetCredential("organization_id"))
	require.Equal(t, "user-old", captured.GetCredential("chatgpt_user_id"))
}

func TestMergeOpenAICodexSnapshotExtraIgnoresLateManagedFields(t *testing.T) {
	account := openAICodexSnapshotTestAccount(76)
	account.Extra = map[string]any{
		"codex_5h_used_percent":               73.0,
		OpenAICodexSnapshotObservedAtExtraKey: "01775582400999999999",
		"operator_note":                       "keep",
	}
	mergeAccountExtra(account, map[string]any{
		"codex_5h_used_percent":               12.0,
		OpenAICodexSnapshotObservedAtExtraKey: "01775582400111111111",
		"operator_note":                       "late-note",
	})

	require.Equal(t, 73.0, account.Extra["codex_5h_used_percent"])
	require.Equal(t, "01775582400999999999", account.Extra[OpenAICodexSnapshotObservedAtExtraKey])
	require.Equal(t, "late-note", account.Extra["operator_note"])
}
