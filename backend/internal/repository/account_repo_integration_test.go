//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type AccountRepoSuite struct {
	suite.Suite
	ctx    context.Context
	client *dbent.Client
	repo   *accountRepository
}

type schedulerCacheRecorder struct {
	setAccounts []*service.Account
	deleteIDs   []int64
	accounts    map[int64]*service.Account
	setCtxErr   error
}

func (s *schedulerCacheRecorder) GetSnapshot(ctx context.Context, bucket service.SchedulerBucket) ([]*service.Account, bool, error) {
	return nil, false, nil
}

func (s *schedulerCacheRecorder) CaptureBucketWriteToken(ctx context.Context, bucket service.SchedulerBucket) (service.SchedulerBucketWriteToken, error) {
	return service.SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (s *schedulerCacheRecorder) SetSnapshot(ctx context.Context, bucket service.SchedulerBucket, token service.SchedulerBucketWriteToken, accounts []service.Account) error {
	return nil
}

func (s *schedulerCacheRecorder) RetireBucket(ctx context.Context, bucket service.SchedulerBucket) error {
	return nil
}

func (s *schedulerCacheRecorder) ReopenBucket(ctx context.Context, bucket service.SchedulerBucket) (service.SchedulerBucketWriteToken, error) {
	return service.SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (s *schedulerCacheRecorder) TryAcquireGroupLifecycleLease(_ context.Context, groupID int64, _ time.Duration) (service.SchedulerGroupLifecycleLease, bool, error) {
	return service.SchedulerGroupLifecycleLease{GroupID: groupID, OwnerToken: "scheduler-cache-recorder"}, true, nil
}

func (s *schedulerCacheRecorder) ReleaseGroupLifecycleLease(context.Context, service.SchedulerGroupLifecycleLease) error {
	return nil
}

func (s *schedulerCacheRecorder) GetAccount(ctx context.Context, accountID int64) (*service.Account, error) {
	if s.accounts == nil {
		return nil, nil
	}
	return s.accounts[accountID], nil
}

func (s *schedulerCacheRecorder) SetAccount(ctx context.Context, account *service.Account) error {
	s.setCtxErr = ctx.Err()
	s.setAccounts = append(s.setAccounts, account)
	if s.accounts == nil {
		s.accounts = make(map[int64]*service.Account)
	}
	if account != nil {
		s.accounts[account.ID] = account
	}
	return nil
}

type failAtomicSchedulerOutboxSQLExecutor struct {
	sqlExecutor
}

func (e *failAtomicSchedulerOutboxSQLExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "WITH updated AS") && strings.Contains(query, "INSERT INTO scheduler_outbox") && len(args) > 0 {
		args = append([]any(nil), args...)
		args[len(args)-1] = nil // event_type is NOT NULL; the whole statement must roll back.
	}
	return e.sqlExecutor.ExecContext(ctx, query, args...)
}

type cancelAfterAtomicMutationSQLExecutor struct {
	sqlExecutor
	cancel context.CancelFunc
}

func (e *cancelAfterAtomicMutationSQLExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := e.sqlExecutor.ExecContext(ctx, query, args...)
	if err == nil && strings.Contains(query, "WITH updated AS") && strings.Contains(query, "INSERT INTO scheduler_outbox") {
		e.cancel()
	}
	return result, err
}

type signalBeforeExecSQLExecutor struct {
	sqlExecutor
	started chan<- struct{}
}

func (e *signalBeforeExecSQLExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	e.started <- struct{}{}
	return e.sqlExecutor.ExecContext(ctx, query, args...)
}

func (s *schedulerCacheRecorder) DeleteAccount(ctx context.Context, accountID int64) error {
	s.deleteIDs = append(s.deleteIDs, accountID)
	if s.accounts != nil {
		delete(s.accounts, accountID)
	}
	return nil
}

func (s *schedulerCacheRecorder) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	return nil
}

func (s *schedulerCacheRecorder) TryLockBucket(ctx context.Context, bucket service.SchedulerBucket, ttl time.Duration) (bool, error) {
	return true, nil
}

func (s *schedulerCacheRecorder) UnlockBucket(ctx context.Context, bucket service.SchedulerBucket) error {
	return nil
}

func (s *schedulerCacheRecorder) ListBuckets(ctx context.Context) ([]service.SchedulerBucket, error) {
	return nil, nil
}

func (s *schedulerCacheRecorder) GetOutboxWatermark(ctx context.Context) (int64, error) {
	return 0, nil
}

func (s *schedulerCacheRecorder) SetOutboxWatermark(ctx context.Context, id int64) error {
	return nil
}

func (s *AccountRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.client = tx.Client()
	s.repo = newAccountRepositoryWithSQL(s.client, tx, nil)
}

func TestAccountRepoSuite(t *testing.T) {
	suite.Run(t, new(AccountRepoSuite))
}

func TestOpenAICodexSnapshotConcurrentWritesKeepNewestObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	account := mustCreateAccount(t, integrationEntClient, &service.Account{
		Name:     fmt.Sprintf("codex-snapshot-concurrency-%d", time.Now().UnixNano()),
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "concurrent-account",
			"organization_id":    "concurrent-org",
			"chatgpt_user_id":    "concurrent-user",
		},
		Extra: map[string]any{"operator_note": "preserve"},
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
	})

	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	identity, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)

	const writeCount = 32
	const observedBase int64 = 1775582400000000000
	start := make(chan struct{})
	errCh := make(chan error, writeCount)
	var workers sync.WaitGroup
	for i := 0; i < writeCount; i++ {
		i := i
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			observedAt := fmt.Sprintf("%020d", observedBase+int64(i))
			_, updateErr := repo.UpdateOpenAICodexSnapshot(ctx, account.ID, identity, nil, map[string]any{
				"codex_5h_used_percent":                       float64(i),
				"codex_usage_updated_at":                      observedAt,
				service.OpenAICodexSnapshotObservedAtExtraKey: observedAt,
			})
			errCh <- updateErr
		}()
	}
	close(start)
	workers.Wait()
	close(errCh)
	for updateErr := range errCh {
		require.NoError(t, updateErr)
	}

	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	expectedObservedAt := fmt.Sprintf("%020d", observedBase+writeCount-1)
	require.Equal(t, float64(writeCount-1), got.Extra["codex_5h_used_percent"])
	require.Equal(t, expectedObservedAt, got.Extra[service.OpenAICodexSnapshotObservedAtExtraKey])
	require.Equal(t, "preserve", got.Extra["operator_note"])

	// Equal sequence values cannot be ordered safely. Keep the first accepted
	// snapshot rather than letting a duplicate asynchronous write replace it.
	_, err = repo.UpdateOpenAICodexSnapshot(ctx, account.ID, identity, nil, map[string]any{
		"codex_5h_used_percent":                       -1.0,
		service.OpenAICodexSnapshotObservedAtExtraKey: expectedObservedAt,
	})
	require.NoError(t, err)
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, float64(writeCount-1), got.Extra["codex_5h_used_percent"])
}

func TestOpenAICodexSnapshotSparkShadowSerializesWithParentReauthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	parent := mustCreateAccount(t, integrationEntClient, &service.Account{
		Name:     fmt.Sprintf("spark-parent-lock-%d", time.Now().UnixNano()),
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "parent-before",
			"organization_id":    "org-before",
			"chatgpt_user_id":    "user-before",
		},
	})
	shadow := mustCreateAccount(t, integrationEntClient, &service.Account{
		Name:            fmt.Sprintf("spark-shadow-lock-%d", time.Now().UnixNano()),
		Platform:        service.PlatformOpenAI,
		Type:            service.AccountTypeOAuth,
		ParentAccountID: &parent.ID,
		QuotaDimension:  service.QuotaDimensionSpark,
		Extra:           map[string]any{"operator_note": "preserve"},
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = ANY($1)", pq.Array([]int64{parent.ID, shadow.ID}))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id = ANY($1)", pq.Array([]int64{parent.ID, shadow.ID}))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", shadow.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", parent.ID)
	})

	reauthorization, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = reauthorization.Rollback() }()
	_, err = reauthorization.ExecContext(ctx, `
		UPDATE accounts
		SET credentials = $1::jsonb
		WHERE id = $2
	`, `{"chatgpt_account_id":"parent-after","organization_id":"org-after","chatgpt_user_id":"user-after"}`, parent.ID)
	require.NoError(t, err)

	started := make(chan struct{}, 1)
	repo := newAccountRepositoryWithSQL(nil, &signalBeforeExecSQLExecutor{
		sqlExecutor: integrationDB,
		started:     started,
	}, nil)
	identity := &service.Account{
		ID:              shadow.ID,
		Platform:        service.PlatformOpenAI,
		Type:            service.AccountTypeOAuth,
		ParentAccountID: &parent.ID,
		QuotaDimension:  service.QuotaDimensionSpark,
		Credentials: map[string]any{
			"chatgpt_account_id": "parent-before",
			"organization_id":    "org-before",
			"chatgpt_user_id":    "user-before",
		},
	}
	type snapshotResult struct {
		applied bool
		err     error
	}
	resultCh := make(chan snapshotResult, 1)
	go func() {
		applied, updateErr := repo.UpdateOpenAICodexSnapshot(ctx, shadow.ID, identity, nil, map[string]any{
			"codex_5h_used_percent":                       99.0,
			service.OpenAICodexSnapshotObservedAtExtraKey: "01775582400999999999",
		})
		resultCh <- snapshotResult{applied: applied, err: updateErr}
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case result := <-resultCh:
		t.Fatalf("stale shadow write bypassed the parent lock: applied=%v err=%v", result.applied, result.err)
	case <-time.After(200 * time.Millisecond):
	}
	require.NoError(t, reauthorization.Commit())

	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.False(t, result.applied)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var extra []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT extra FROM accounts WHERE id = $1", shadow.ID).Scan(&extra))
	require.NotContains(t, string(extra), "codex_5h_used_percent")
}

// --- Create / GetByID / Update / Delete ---

func (s *AccountRepoSuite) TestCreate() {
	account := &service.Account{
		Name:        "test-create",
		Platform:    service.PlatformAnthropic,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: map[string]any{},
		Extra:       map[string]any{},
		Concurrency: 3,
		Priority:    50,
		Schedulable: true,
	}

	err := s.repo.Create(s.ctx, account)
	s.Require().NoError(err, "Create")
	s.Require().NotZero(account.ID, "expected ID to be set")

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal("test-create", got.Name)
}

func (s *AccountRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
}

func (s *AccountRepoSuite) TestUpdate() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "original"})

	account.Name = "updated"
	err := s.repo.Update(s.ctx, account)
	s.Require().NoError(err, "Update")

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("updated", got.Name)
}

func (s *AccountRepoSuite) TestUpdate_PreservesConcurrentOpenAIWakeSnapshotForSameIdentity() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "wake-snapshot-concurrent-edit",
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "old-token",
			"chatgpt_account_id": "account-a",
			"organization_id":    "org-a",
			"chatgpt_user_id":    "user-a",
		},
		Extra: map[string]any{
			"operator_note":               "old",
			"codex_5h_used_percent":       91.0,
			"codex_usage_updated_at":      "stale",
			"codex_5h_wake_identity_hash": "stale-marker",
		},
	})
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)

	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{
		"codex_5h_used_percent":                      12.5,
		"codex_5h_reset_at":                          "2026-08-05T12:00:00Z",
		"codex_7d_used_percent":                      34.5,
		"codex_usage_updated_at":                     "2026-08-05T07:00:00Z",
		service.OpenAI5hWakeSnapshotIdentityExtraKey: "fresh-marker",
	}))
	stale.Name = "wake-snapshot-concurrent-edit-renamed"
	stale.Extra["operator_note"] = "edited"
	s.Require().NoError(s.repo.Update(s.ctx, stale))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("wake-snapshot-concurrent-edit-renamed", got.Name)
	s.Require().Equal("edited", got.Extra["operator_note"])
	s.Require().Equal(12.5, got.Extra["codex_5h_used_percent"])
	s.Require().Equal("2026-08-05T12:00:00Z", got.Extra["codex_5h_reset_at"])
	s.Require().Equal(34.5, got.Extra["codex_7d_used_percent"])
	s.Require().Equal("2026-08-05T07:00:00Z", got.Extra["codex_usage_updated_at"])
	s.Require().Equal("fresh-marker", got.Extra[service.OpenAI5hWakeSnapshotIdentityExtraKey])
}

func (s *AccountRepoSuite) TestUpdate_ClearsOpenAIWakeSnapshotWhenIdentityChanges() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "wake-snapshot-identity-change",
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "token-before",
			"chatgpt_account_id": "account-before",
			"organization_id":    "org-a",
			"chatgpt_user_id":    "user-a",
		},
		Extra: map[string]any{
			"operator_note":                              "kept",
			"codex_primary_used_percent":                 11.0,
			"codex_5h_used_percent":                      12.0,
			"codex_5h_reset_at":                          "2026-08-05T12:00:00Z",
			"codex_7d_used_percent":                      34.0,
			"codex_usage_updated_at":                     "2026-08-05T07:00:00Z",
			service.OpenAI5hWakeSnapshotIdentityExtraKey: "old-marker",
		},
	})
	shadow := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:            "wake-snapshot-identity-change-shadow",
		Platform:        service.PlatformOpenAI,
		Type:            service.AccountTypeOAuth,
		ParentAccountID: &account.ID,
		QuotaDimension:  service.QuotaDimensionSpark,
		Extra: map[string]any{
			"operator_note":                              "shadow-kept",
			"codex_5h_used_percent":                      44.0,
			"codex_usage_updated_at":                     "2026-08-05T07:00:00Z",
			service.OpenAI5hWakeSnapshotIdentityExtraKey: "shadow-marker",
		},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	_, err := s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	loaded, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	loaded.Credentials["access_token"] = "token-rotated"
	s.Require().NoError(s.repo.Update(s.ctx, loaded))
	afterTokenRotation, err := s.repo.GetByID(s.ctx, shadow.ID)
	s.Require().NoError(err)
	s.Require().Equal(44.0, afterTokenRotation.Extra["codex_5h_used_percent"])
	s.Require().Equal("shadow-marker", afterTokenRotation.Extra[service.OpenAI5hWakeSnapshotIdentityExtraKey])

	loaded, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	loaded.Credentials["chatgpt_account_id"] = "account-after"
	s.Require().NoError(s.repo.Update(s.ctx, loaded))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("kept", got.Extra["operator_note"])
	for _, key := range openAIWakeManagedExtraKeys {
		s.Require().NotContains(got.Extra, key)
	}
	gotShadow, err := s.repo.GetByID(s.ctx, shadow.ID)
	s.Require().NoError(err)
	s.Require().Equal("shadow-kept", gotShadow.Extra["operator_note"])
	for _, key := range openAIWakeManagedExtraKeys {
		s.Require().NotContains(gotShadow.Extra, key)
	}
	s.Require().NotNil(cacheRecorder.accounts[shadow.ID])
	for _, key := range openAIWakeManagedExtraKeys {
		s.Require().NotContains(cacheRecorder.accounts[shadow.ID].Extra, key)
	}
	var bulkOutboxCount int
	err = scanSingleRow(
		s.ctx,
		s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1",
		[]any{service.SchedulerOutboxEventAccountBulkChanged},
		&bulkOutboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, bulkOutboxCount)
}

func (s *AccountRepoSuite) TestUpdate_SyncSchedulerSnapshotOnDisabled() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "sync-update", Status: service.StatusActive, Schedulable: true})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	account.Status = service.StatusDisabled
	err := s.repo.Update(s.ctx, account)
	s.Require().NoError(err, "Update")

	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
	s.Require().Equal(service.StatusDisabled, cacheRecorder.setAccounts[0].Status)
}

func (s *AccountRepoSuite) TestUpdate_SyncSchedulerSnapshotOnCredentialsChange() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "sync-credentials-update",
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5": "gpt-5.1",
			},
		},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	account.Credentials = map[string]any{
		"model_mapping": map[string]any{
			"gpt-5": "gpt-5.2",
		},
	}
	err := s.repo.Update(s.ctx, account)
	s.Require().NoError(err, "Update")

	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
	mapping, ok := cacheRecorder.setAccounts[0].Credentials["model_mapping"].(map[string]any)
	s.Require().True(ok)
	s.Require().Equal("gpt-5.2", mapping["gpt-5"])
}

func (s *AccountRepoSuite) TestUpdateCredentials_SyncsSnapshotAndDurableOutbox() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "sync-refresh-credentials",
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "old-token"},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	_, err := s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{"access_token": "new-token"}))

	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal("new-token", cacheRecorder.setAccounts[0].GetCredential("access_token"))
	var outboxCount int
	err = scanSingleRow(
		s.ctx,
		s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
		[]any{service.SchedulerOutboxEventAccountChanged, account.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, outboxCount)
}

func (s *AccountRepoSuite) TestUpdateCredentials_InvalidatesWakeMarkerOnlyWhenTypedIdentityChanges() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "wake-identity-credentials",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":       "old-token",
			"chatgpt_account_id": "account-before",
			"organization_id":    "org",
		},
		Extra: map[string]any{
			"codex_primary_used_percent":                 1.0,
			"codex_5h_used_percent":                      2.0,
			"codex_5h_reset_at":                          "2026-08-05T12:00:00Z",
			"codex_7d_used_percent":                      3.0,
			"codex_usage_updated_at":                     "2026-08-05T07:00:00Z",
			service.OpenAI5hWakeSnapshotIdentityExtraKey: "trusted-marker",
		},
	})

	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{
		"access_token":       "rotated-token",
		"chatgpt_account_id": "account-before",
		"organization_id":    "org",
	}))
	afterTokenRotation, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("trusted-marker", afterTokenRotation.Extra[service.OpenAI5hWakeSnapshotIdentityExtraKey])

	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{
		"access_token":       "reauthorized-token",
		"chatgpt_account_id": "account-after",
		"organization_id":    "org",
	}))
	afterIdentityChange, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	for _, key := range openAIWakeManagedExtraKeys {
		s.Require().NotContains(afterIdentityChange.Extra, key)
	}
}

func (s *AccountRepoSuite) TestOpenAIWakeSnapshotCASRejectsReauthorizedIdentity() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "wake-cas", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "account-before", "organization_id": "org", "chatgpt_user_id": "user",
		},
		Extra: map[string]any{"operator_note": "preserve"},
	})
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{
		"chatgpt_account_id": "account-after", "organization_id": "org", "chatgpt_user_id": "user",
	}))

	applied, err := s.repo.UpdateOpenAICodexSnapshot(s.ctx, stale.ID, stale, nil, map[string]any{
		"codex_5h_used_percent":                      99.0,
		service.OpenAI5hWakeSnapshotIdentityExtraKey: "old-marker",
	})
	s.Require().NoError(err)
	s.Require().False(applied)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("preserve", got.Extra["operator_note"])
	s.Require().NotContains(got.Extra, "codex_5h_used_percent")
	s.Require().NotContains(got.Extra, service.OpenAI5hWakeSnapshotIdentityExtraKey)
}

func (s *AccountRepoSuite) TestOpenAICodexSnapshotDoesNotRegressForLateResponse() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "wake-monotonic", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "account-a", "organization_id": "org", "chatgpt_user_id": "user",
		},
		Extra: map[string]any{"operator_note": "preserve"},
	})
	identity, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	newerObservedAt := "01775582400999999999"
	olderObservedAt := "01775582400111111111"

	applied, err := s.repo.UpdateOpenAICodexSnapshot(s.ctx, account.ID, identity, nil, map[string]any{
		"codex_5h_used_percent":                       73.0,
		"codex_usage_updated_at":                      "2026-04-07T10:00:00Z",
		service.OpenAICodexSnapshotObservedAtExtraKey: newerObservedAt,
	})
	s.Require().NoError(err)
	s.Require().True(applied)

	// Simulate an older response whose asynchronous persistence finishes later.
	applied, err = s.repo.UpdateOpenAICodexSnapshot(s.ctx, account.ID, identity, nil, map[string]any{
		"codex_5h_used_percent":                       12.0,
		"codex_usage_updated_at":                      "2026-04-07T09:59:59Z",
		service.OpenAICodexSnapshotObservedAtExtraKey: olderObservedAt,
	})
	s.Require().NoError(err)
	s.Require().True(applied, "the account identity still matches even though the stale snapshot is ignored")

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(73.0, got.Extra["codex_5h_used_percent"])
	s.Require().Equal("2026-04-07T10:00:00Z", got.Extra["codex_usage_updated_at"])
	s.Require().Equal(newerObservedAt, got.Extra[service.OpenAICodexSnapshotObservedAtExtraKey])
	s.Require().Equal("preserve", got.Extra["operator_note"])
}

func (s *AccountRepoSuite) TestOpenAICodexSnapshotSparkShadowRejectsReauthorizedParent() {
	parent := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "spark-cas-parent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "spark-token-before",
			"chatgpt_account_id": "spark-parent-before",
			"organization_id":    "spark-org-before",
			"chatgpt_user_id":    "spark-user-before",
		},
	})
	shadow := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "spark-cas-shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials:     map[string]any{},
		ParentAccountID: &parent.ID,
		QuotaDimension:  service.QuotaDimensionSpark,
		Extra:           map[string]any{"operator_note": "shadow-preserve"},
	})
	identity, err := s.repo.GetByID(s.ctx, shadow.ID)
	s.Require().NoError(err)
	// A real usage probe captures the parent's typed identity before querying
	// /wham/usage. Reproduce that immutable request snapshot here; credentials
	// remain absent from the persisted shadow row itself.
	identity.Credentials = map[string]any{
		"chatgpt_account_id": "spark-parent-before",
		"organization_id":    "spark-org-before",
		"chatgpt_user_id":    "spark-user-before",
	}

	applied, err := s.repo.UpdateOpenAICodexSnapshot(s.ctx, shadow.ID, identity, nil, map[string]any{
		"codex_5h_used_percent":                       31.0,
		service.OpenAICodexSnapshotObservedAtExtraKey: "01775582400111111111",
	})
	s.Require().NoError(err)
	s.Require().True(applied)

	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, parent.ID, map[string]any{
		"access_token":       "spark-token-rotated",
		"chatgpt_account_id": "spark-parent-before",
		"organization_id":    "spark-org-before",
		"chatgpt_user_id":    "spark-user-before",
	}))
	afterTokenRotation, err := s.repo.GetByID(s.ctx, shadow.ID)
	s.Require().NoError(err)
	s.Require().Equal(31.0, afterTokenRotation.Extra["codex_5h_used_percent"])

	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, parent.ID, map[string]any{
		"access_token":       "spark-token-reauthorized",
		"chatgpt_account_id": "spark-parent-after",
		"organization_id":    "spark-org-after",
		"chatgpt_user_id":    "spark-user-after",
	}))
	afterReauthorization, err := s.repo.GetByID(s.ctx, shadow.ID)
	s.Require().NoError(err)
	s.Require().Equal("shadow-preserve", afterReauthorization.Extra["operator_note"])
	for _, key := range openAIWakeManagedExtraKeys {
		s.Require().NotContains(afterReauthorization.Extra, key)
	}

	applied, err = s.repo.UpdateOpenAICodexSnapshot(s.ctx, shadow.ID, identity, nil, map[string]any{
		"codex_5h_used_percent":                       99.0,
		service.OpenAICodexSnapshotObservedAtExtraKey: "01775582400999999999",
	})
	s.Require().NoError(err)
	s.Require().False(applied, "a late response from the old parent identity must not update the shadow")

	got, err := s.repo.GetByID(s.ctx, shadow.ID)
	s.Require().NoError(err)
	s.Require().Equal("shadow-preserve", got.Extra["operator_note"])
	s.Require().NotContains(got.Extra, "codex_5h_used_percent")
}

func (s *AccountRepoSuite) TestOpenAICodexSnapshotAppliesOrdinaryFieldsWhileRejectingStaleManagedFields() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "wake-mixed-atomic", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "account-a", "organization_id": "org", "chatgpt_user_id": "user",
		},
	})
	identity, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	newerObservedAt := "01775582400999999999"
	olderObservedAt := "01775582400111111111"

	applied, err := s.repo.UpdateOpenAICodexSnapshot(s.ctx, account.ID, identity, nil, map[string]any{
		"codex_5h_used_percent":                       73.0,
		service.OpenAICodexSnapshotObservedAtExtraKey: newerObservedAt,
	})
	s.Require().NoError(err)
	s.Require().True(applied)

	applied, err = s.repo.UpdateOpenAICodexSnapshot(
		s.ctx,
		account.ID,
		identity,
		map[string]any{
			"openai_compact_supported":   true,
			"openai_compact_last_status": 200,
		},
		map[string]any{
			"codex_5h_used_percent":                       12.0,
			service.OpenAICodexSnapshotObservedAtExtraKey: olderObservedAt,
		},
	)
	s.Require().NoError(err)
	s.Require().True(applied)

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(73.0, got.Extra["codex_5h_used_percent"])
	s.Require().Equal(newerObservedAt, got.Extra[service.OpenAICodexSnapshotObservedAtExtraKey])
	s.Require().Equal(true, got.Extra["openai_compact_supported"])
	s.Require().Equal(float64(200), got.Extra["openai_compact_last_status"])
}

func (s *AccountRepoSuite) TestOpenAICodexSnapshotDetachedCacheSyncIgnoresCallerCancellation() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "wake-detached-cache", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "account-detached"},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	ctx, cancel := context.WithCancel(s.ctx)
	cancel()

	s.repo.syncSchedulerAccountSnapshotDetached(ctx, account.ID)

	s.Require().ErrorIs(ctx.Err(), context.Canceled)
	s.Require().NoError(cacheRecorder.setCtxErr)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
}

func (s *AccountRepoSuite) TestBulkUpdate_ClearsOpenAIWakeSnapshotOnIdentityEdit() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "bulk-wake-identity", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "bulk-token-before",
			"chatgpt_account_id": "account-before", "organization_id": "org", "chatgpt_user_id": "user",
		},
		Extra: map[string]any{
			"codex_5h_used_percent":                      2.0,
			"codex_7d_used_percent":                      3.0,
			"codex_usage_updated_at":                     "2026-08-05T07:00:00Z",
			service.OpenAI5hWakeSnapshotIdentityExtraKey: "trusted-marker",
		},
	})
	shadow := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:            "bulk-wake-identity-shadow",
		Platform:        service.PlatformOpenAI,
		Type:            service.AccountTypeOAuth,
		ParentAccountID: &account.ID,
		QuotaDimension:  service.QuotaDimensionSpark,
		Extra: map[string]any{
			"operator_note":          "shadow-kept",
			"codex_5h_used_percent":  27.0,
			"codex_usage_updated_at": "2026-08-05T07:00:00Z",
		},
	})
	_, err := s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{
		Credentials: map[string]any{"access_token": "bulk-token-rotated"},
	})
	s.Require().NoError(err)
	afterTokenRotation, err := s.repo.GetByID(s.ctx, shadow.ID)
	s.Require().NoError(err)
	s.Require().Equal(27.0, afterTokenRotation.Extra["codex_5h_used_percent"])

	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{
		Credentials: map[string]any{"chatgpt_account_id": "account-after"},
	})
	s.Require().NoError(err)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	for _, key := range openAIWakeManagedExtraKeys {
		s.Require().NotContains(got.Extra, key)
	}
	gotShadow, err := s.repo.GetByID(s.ctx, shadow.ID)
	s.Require().NoError(err)
	s.Require().Equal("shadow-kept", gotShadow.Extra["operator_note"])
	for _, key := range openAIWakeManagedExtraKeys {
		s.Require().NotContains(gotShadow.Extra, key)
	}
}

func (s *AccountRepoSuite) TestDelete() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "to-delete"})

	err := s.repo.Delete(s.ctx, account.ID)
	s.Require().NoError(err, "Delete")

	_, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().Error(err, "expected error after delete")
}

func (s *AccountRepoSuite) TestDelete_RemovesSchedulerAccountSnapshot() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "to-delete-cache"})
	cacheRecorder := &schedulerCacheRecorder{
		accounts: map[int64]*service.Account{
			account.ID: {
				ID:          account.ID,
				Name:        account.Name,
				Status:      service.StatusActive,
				Schedulable: true,
			},
		},
	}
	s.repo.schedulerCache = cacheRecorder

	err := s.repo.Delete(s.ctx, account.ID)
	s.Require().NoError(err, "Delete")

	s.Require().Equal([]int64{account.ID}, cacheRecorder.deleteIDs)
	s.Require().NotContains(cacheRecorder.accounts, account.ID)
}

func (s *AccountRepoSuite) TestDelete_WithGroupBindings() {
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-del"})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-del"})
	mustBindAccountToGroup(s.T(), s.client, account.ID, group.ID, 1)

	err := s.repo.Delete(s.ctx, account.ID)
	s.Require().NoError(err, "Delete should cascade remove bindings")

	count, err := s.client.AccountGroup.Query().Where(accountgroup.AccountIDEQ(account.ID)).Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Zero(count, "expected bindings to be removed")
}

// --- List / ListWithFilters ---

func (s *AccountRepoSuite) TestList() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc1"})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc2"})

	accounts, page, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "List")
	s.Require().Len(accounts, 2)
	s.Require().Equal(int64(2), page.Total)
}

func (s *AccountRepoSuite) TestListOAuthRefreshCandidatePage_GrokCursorAndExclusions() {
	now := time.Now().UTC()
	valid1 := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "grok-oauth-page-1",
		Platform: service.PlatformGrok,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"expires_at":    now.Add(30 * time.Minute).Format(time.RFC3339),
		},
	})
	unschedulable := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-oauth-unschedulable-excluded",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-unschedulable"},
	})
	s.Require().NoError(s.client.Account.UpdateOneID(unschedulable.ID).SetSchedulable(false).Exec(s.ctx))
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "grok-api-key-excluded",
		Platform: service.PlatformGrok,
		Type:     service.AccountTypeAPIKey,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"api_key":       "api-key",
			"refresh_token": "must-not-make-api-key-eligible",
		},
	})
	valid2 := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-oauth-page-2",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-2"},
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-oauth-blank-refresh-excluded",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: map[string]any{"refresh_token": "   "},
	})
	valid3 := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-oauth-page-3",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-3"},
	})
	cooldown := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-oauth-retry-cooldown-excluded",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-cooldown"},
	})
	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, cooldown.ID, now.Add(10*time.Minute), "token refresh retry exhausted: timeout"))
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "openai-oauth-excluded",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: map[string]any{"refresh_token": "refresh-openai"},
	})

	options := service.OAuthRefreshPageOptions{
		Platforms:            []string{service.PlatformGrok},
		Limit:                2,
		ActiveOnly:           true,
		RequireRefreshToken:  true,
		ExcludeRetryCooldown: true,
	}
	firstPage, err := s.repo.ListOAuthRefreshCandidatePage(s.ctx, options)
	s.Require().NoError(err)
	first := firstPage.Accounts
	s.Require().Len(first, 2)
	s.Require().Equal([]int64{valid1.ID, valid2.ID}, []int64{first[0].ID, first[1].ID})
	s.Require().NotContains([]int64{first[0].ID, first[1].ID}, unschedulable.ID)

	options.AfterID = first[len(first)-1].ID
	secondPage, err := s.repo.ListOAuthRefreshCandidatePage(s.ctx, options)
	s.Require().NoError(err)
	second := secondPage.Accounts
	s.Require().Len(second, 1)
	s.Require().Equal(valid3.ID, second[0].ID)
	s.Require().NotContains([]int64{first[0].ID, first[1].ID}, second[0].ID)
}

func (s *AccountRepoSuite) TestListWithFilters() {
	tests := []struct {
		name        string
		setup       func(client *dbent.Client)
		platform    string
		accType     string
		status      string
		search      string
		groupID     int64
		privacyMode string
		wantCount   int
		validate    func(accounts []service.Account)
	}{
		{
			name: "filter_by_platform",
			setup: func(client *dbent.Client) {
				mustCreateAccount(s.T(), client, &service.Account{Name: "a1", Platform: service.PlatformAnthropic})
				mustCreateAccount(s.T(), client, &service.Account{Name: "a2", Platform: service.PlatformOpenAI})
			},
			platform:  service.PlatformOpenAI,
			wantCount: 1,
			validate: func(accounts []service.Account) {
				s.Require().Equal(service.PlatformOpenAI, accounts[0].Platform)
			},
		},
		{
			name: "filter_by_type",
			setup: func(client *dbent.Client) {
				mustCreateAccount(s.T(), client, &service.Account{Name: "t1", Type: service.AccountTypeOAuth})
				mustCreateAccount(s.T(), client, &service.Account{Name: "t2", Type: service.AccountTypeAPIKey})
			},
			accType:   service.AccountTypeAPIKey,
			wantCount: 1,
			validate: func(accounts []service.Account) {
				s.Require().Equal(service.AccountTypeAPIKey, accounts[0].Type)
			},
		},
		{
			name: "filter_by_status",
			setup: func(client *dbent.Client) {
				mustCreateAccount(s.T(), client, &service.Account{Name: "s1", Status: service.StatusActive})
				mustCreateAccount(s.T(), client, &service.Account{Name: "s2", Status: service.StatusDisabled})
			},
			status:    service.StatusDisabled,
			wantCount: 1,
			validate: func(accounts []service.Account) {
				s.Require().Equal(service.StatusDisabled, accounts[0].Status)
			},
		},
		{
			name: "filter_by_status_active_excludes_runtime_blocked_accounts",
			setup: func(client *dbent.Client) {
				mustCreateAccount(s.T(), client, &service.Account{Name: "active-normal", Status: service.StatusActive})
				rateLimited := mustCreateAccount(s.T(), client, &service.Account{Name: "active-rate-limited", Status: service.StatusActive})
				err := client.Account.UpdateOneID(rateLimited.ID).
					SetRateLimitResetAt(time.Now().Add(10 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				tempUnsched := mustCreateAccount(s.T(), client, &service.Account{Name: "active-temp-unsched", Status: service.StatusActive})
				err = client.Account.UpdateOneID(tempUnsched.ID).
					SetTempUnschedulableUntil(time.Now().Add(15 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				unsched := mustCreateAccount(s.T(), client, &service.Account{Name: "active-unsched", Status: service.StatusActive})
				err = client.Account.UpdateOneID(unsched.ID).
					SetSchedulable(false).
					Exec(context.Background())
				s.Require().NoError(err)
			},
			status:    service.StatusActive,
			wantCount: 1,
			validate: func(accounts []service.Account) {
				s.Require().Equal("active-normal", accounts[0].Name)
			},
		},
		{
			name: "filter_by_status_unschedulable_excludes_rate_limited_and_temp_unschedulable",
			setup: func(client *dbent.Client) {
				mustCreateAccount(s.T(), client, &service.Account{Name: "active-normal", Status: service.StatusActive, Schedulable: true})
				unsched := mustCreateAccount(s.T(), client, &service.Account{Name: "active-unsched", Status: service.StatusActive})
				err := client.Account.UpdateOneID(unsched.ID).
					SetSchedulable(false).
					Exec(context.Background())
				s.Require().NoError(err)
				rateLimited := mustCreateAccount(s.T(), client, &service.Account{Name: "active-rate-limited", Status: service.StatusActive})
				err = client.Account.UpdateOneID(rateLimited.ID).
					SetSchedulable(false).
					SetRateLimitResetAt(time.Now().Add(10 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				tempUnsched := mustCreateAccount(s.T(), client, &service.Account{Name: "active-temp-unsched", Status: service.StatusActive})
				err = client.Account.UpdateOneID(tempUnsched.ID).
					SetSchedulable(false).
					SetTempUnschedulableUntil(time.Now().Add(15 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
			},
			status:    "unschedulable",
			wantCount: 1,
			validate: func(accounts []service.Account) {
				s.Require().Equal("active-unsched", accounts[0].Name)
			},
		},
		{
			name: "filter_by_status_rate_limited_excludes_temp_unschedulable",
			setup: func(client *dbent.Client) {
				rateLimited := mustCreateAccount(s.T(), client, &service.Account{Name: "active-rate-limited", Status: service.StatusActive})
				err := client.Account.UpdateOneID(rateLimited.ID).
					SetRateLimitResetAt(time.Now().Add(10 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				tempUnsched := mustCreateAccount(s.T(), client, &service.Account{Name: "active-temp-unsched", Status: service.StatusActive})
				err = client.Account.UpdateOneID(tempUnsched.ID).
					SetRateLimitResetAt(time.Now().Add(20 * time.Minute)).
					SetTempUnschedulableUntil(time.Now().Add(15 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
			},
			status:    "rate_limited",
			wantCount: 1,
			validate: func(accounts []service.Account) {
				s.Require().Equal("active-rate-limited", accounts[0].Name)
			},
		},
		{
			name: "filter_by_status_temp_unschedulable_excludes_manually_unschedulable",
			setup: func(client *dbent.Client) {
				tempUnsched := mustCreateAccount(s.T(), client, &service.Account{Name: "active-temp-unsched", Status: service.StatusActive, Schedulable: true})
				err := client.Account.UpdateOneID(tempUnsched.ID).
					SetTempUnschedulableUntil(time.Now().Add(15 * time.Minute)).
					Exec(context.Background())
				s.Require().NoError(err)
				unsched := mustCreateAccount(s.T(), client, &service.Account{Name: "active-unsched", Status: service.StatusActive})
				err = client.Account.UpdateOneID(unsched.ID).
					SetSchedulable(false).
					Exec(context.Background())
				s.Require().NoError(err)
			},
			status:    "temp_unschedulable",
			wantCount: 1,
			validate: func(accounts []service.Account) {
				s.Require().Equal("active-temp-unsched", accounts[0].Name)
			},
		},
		{
			name: "filter_by_search",
			setup: func(client *dbent.Client) {
				mustCreateAccount(s.T(), client, &service.Account{Name: "alpha-account"})
				mustCreateAccount(s.T(), client, &service.Account{Name: "beta-account"})
			},
			search:    "alpha",
			wantCount: 1,
			validate: func(accounts []service.Account) {
				s.Require().Contains(accounts[0].Name, "alpha")
			},
		},
		{
			name: "filter_by_ungrouped",
			setup: func(client *dbent.Client) {
				group := mustCreateGroup(s.T(), client, &service.Group{Name: "g-ungrouped"})
				grouped := mustCreateAccount(s.T(), client, &service.Account{Name: "grouped-account"})
				mustCreateAccount(s.T(), client, &service.Account{Name: "ungrouped-account"})
				mustBindAccountToGroup(s.T(), client, grouped.ID, group.ID, 1)
			},
			groupID:   service.AccountListGroupUngrouped,
			wantCount: 1,
			validate: func(accounts []service.Account) {
				s.Require().Equal("ungrouped-account", accounts[0].Name)
				s.Require().Empty(accounts[0].GroupIDs)
			},
		},
		{
			name: "filter_by_privacy_mode",
			setup: func(client *dbent.Client) {
				mustCreateAccount(s.T(), client, &service.Account{Name: "privacy-ok", Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff}})
				mustCreateAccount(s.T(), client, &service.Account{Name: "privacy-fail", Extra: map[string]any{"privacy_mode": service.PrivacyModeFailed}})
			},
			privacyMode: service.PrivacyModeTrainingOff,
			wantCount:   1,
			validate: func(accounts []service.Account) {
				s.Require().Equal("privacy-ok", accounts[0].Name)
			},
		},
		{
			name: "filter_by_privacy_mode_unset",
			setup: func(client *dbent.Client) {
				mustCreateAccount(s.T(), client, &service.Account{Name: "privacy-unset", Extra: nil})
				mustCreateAccount(s.T(), client, &service.Account{Name: "privacy-empty", Extra: map[string]any{"privacy_mode": ""}})
				mustCreateAccount(s.T(), client, &service.Account{Name: "privacy-set", Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff}})
			},
			privacyMode: service.AccountPrivacyModeUnsetFilter,
			wantCount:   2,
			validate: func(accounts []service.Account) {
				names := []string{accounts[0].Name, accounts[1].Name}
				s.ElementsMatch([]string{"privacy-unset", "privacy-empty"}, names)
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			// 每个 case 重新获取隔离资源
			tx := testEntTx(s.T())
			client := tx.Client()
			repo := newAccountRepositoryWithSQL(client, tx, nil)
			ctx := context.Background()

			tt.setup(client)

			accounts, page, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, tt.platform, tt.accType, tt.status, tt.search, tt.groupID, tt.privacyMode)
			s.Require().NoError(err)
			s.Require().Len(accounts, tt.wantCount)
			// Regression guard for issue #3601: when the whole result set fits on a single page,
			// pagination.Total must match len(items). A mismatch means the Count query was applied
			// against different predicates than the list query — the exact symptom reported.
			s.Require().NotNil(page)
			s.Require().Equal(int64(tt.wantCount), page.Total, "total must match items on single page")
			if tt.validate != nil {
				tt.validate(accounts)
			}
		})
	}
}

// --- ListByGroup / ListActive / ListByPlatform ---

func (s *AccountRepoSuite) TestListByGroup() {
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-list"})
	acc1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "a1", Status: service.StatusActive})
	acc2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "a2", Status: service.StatusActive})
	mustBindAccountToGroup(s.T(), s.client, acc1.ID, group.ID, 2)
	mustBindAccountToGroup(s.T(), s.client, acc2.ID, group.ID, 1)

	accounts, err := s.repo.ListByGroup(s.ctx, group.ID)
	s.Require().NoError(err, "ListByGroup")
	s.Require().Len(accounts, 2)
	// Should be ordered by priority
	s.Require().Equal(acc2.ID, accounts[0].ID, "expected acc2 first (priority=1)")
}

func (s *AccountRepoSuite) TestListActive() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "active1", Status: service.StatusActive})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "inactive1", Status: service.StatusDisabled})

	accounts, err := s.repo.ListActive(s.ctx)
	s.Require().NoError(err, "ListActive")
	s.Require().Len(accounts, 1)
	s.Require().Equal("active1", accounts[0].Name)
}

func (s *AccountRepoSuite) TestListByPlatform() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "p1", Platform: service.PlatformAnthropic, Status: service.StatusActive})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "p2", Platform: service.PlatformOpenAI, Status: service.StatusActive})

	accounts, err := s.repo.ListByPlatform(s.ctx, service.PlatformAnthropic)
	s.Require().NoError(err, "ListByPlatform")
	s.Require().Len(accounts, 1)
	s.Require().Equal(service.PlatformAnthropic, accounts[0].Platform)
}

func (s *AccountRepoSuite) TestListByPlatformAllStatusesIncludesDisabled() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "all-active", Platform: service.PlatformOpenAI, Status: service.StatusActive})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "all-disabled", Platform: service.PlatformOpenAI, Status: service.StatusDisabled})

	accounts, err := s.repo.ListByPlatformAllStatuses(s.ctx, service.PlatformOpenAI)
	s.Require().NoError(err, "ListByPlatformAllStatuses")
	s.Require().Len(accounts, 2)
	s.Require().ElementsMatch([]string{service.StatusActive, service.StatusDisabled}, []string{accounts[0].Status, accounts[1].Status})
}

// --- Preload and VirtualFields ---

func (s *AccountRepoSuite) TestPreload_And_VirtualFields() {
	proxy := mustCreateProxy(s.T(), s.client, &service.Proxy{Name: "p1"})
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g1"})

	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:    "acc1",
		ProxyID: &proxy.ID,
	})
	mustBindAccountToGroup(s.T(), s.client, account.ID, group.ID, 1)

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().NotNil(got.Proxy, "expected Proxy preload")
	s.Require().Equal(proxy.ID, got.Proxy.ID)
	s.Require().Len(got.GroupIDs, 1, "expected GroupIDs to be populated")
	s.Require().Equal(group.ID, got.GroupIDs[0])
	s.Require().Len(got.Groups, 1, "expected Groups to be populated")
	s.Require().Equal(group.ID, got.Groups[0].ID)

	accounts, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", "acc", 0, "")
	s.Require().NoError(err, "ListWithFilters")
	s.Require().Equal(int64(1), page.Total)
	s.Require().Len(accounts, 1)
	s.Require().NotNil(accounts[0].Proxy, "expected Proxy preload in list")
	s.Require().Equal(proxy.ID, accounts[0].Proxy.ID)
	s.Require().Len(accounts[0].GroupIDs, 1, "expected GroupIDs in list")
	s.Require().Equal(group.ID, accounts[0].GroupIDs[0])
}

// --- GroupBinding / AddToGroup / RemoveFromGroup / BindGroups / GetGroups ---

func (s *AccountRepoSuite) TestGroupBinding_And_BindGroups() {
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g1"})
	g2 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g2"})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc"})

	s.Require().NoError(s.repo.AddToGroup(s.ctx, account.ID, g1.ID, 10), "AddToGroup")
	groups, err := s.repo.GetGroups(s.ctx, account.ID)
	s.Require().NoError(err, "GetGroups")
	s.Require().Len(groups, 1, "expected 1 group")
	s.Require().Equal(g1.ID, groups[0].ID)

	s.Require().NoError(s.repo.RemoveFromGroup(s.ctx, account.ID, g1.ID), "RemoveFromGroup")
	groups, err = s.repo.GetGroups(s.ctx, account.ID)
	s.Require().NoError(err, "GetGroups after remove")
	s.Require().Empty(groups, "expected 0 groups after remove")

	s.Require().NoError(s.repo.BindGroups(s.ctx, account.ID, []int64{g1.ID, g2.ID}), "BindGroups")
	groups, err = s.repo.GetGroups(s.ctx, account.ID)
	s.Require().NoError(err, "GetGroups after bind")
	s.Require().Len(groups, 2, "expected 2 groups after bind")
}

func (s *AccountRepoSuite) TestBindGroups_EmptyList() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-empty"})
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-empty"})
	mustBindAccountToGroup(s.T(), s.client, account.ID, group.ID, 1)

	s.Require().NoError(s.repo.BindGroups(s.ctx, account.ID, []int64{}), "BindGroups empty")

	groups, err := s.repo.GetGroups(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Empty(groups, "expected 0 groups after binding empty list")
}

// --- Schedulable ---

func (s *AccountRepoSuite) TestListSchedulable() {
	now := time.Now()
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-sched"})

	okAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "ok", Schedulable: true})
	mustBindAccountToGroup(s.T(), s.client, okAcc.ID, group.ID, 1)

	future := now.Add(10 * time.Minute)
	overloaded := mustCreateAccount(s.T(), s.client, &service.Account{Name: "over", Schedulable: true, OverloadUntil: &future})
	mustBindAccountToGroup(s.T(), s.client, overloaded.ID, group.ID, 1)

	sched, err := s.repo.ListSchedulable(s.ctx)
	s.Require().NoError(err, "ListSchedulable")
	ids := idsOfAccounts(sched)
	s.Require().Contains(ids, okAcc.ID)
	s.Require().NotContains(ids, overloaded.ID)
}

func (s *AccountRepoSuite) TestListSchedulableByGroupID_TimeBoundaries_And_StatusUpdates() {
	now := time.Now()
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-sched"})

	okAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "ok", Schedulable: true})
	mustBindAccountToGroup(s.T(), s.client, okAcc.ID, group.ID, 1)

	future := now.Add(10 * time.Minute)
	overloaded := mustCreateAccount(s.T(), s.client, &service.Account{Name: "over", Schedulable: true, OverloadUntil: &future})
	mustBindAccountToGroup(s.T(), s.client, overloaded.ID, group.ID, 1)

	rateLimited := mustCreateAccount(s.T(), s.client, &service.Account{Name: "rl", Schedulable: true})
	mustBindAccountToGroup(s.T(), s.client, rateLimited.ID, group.ID, 1)
	s.Require().NoError(s.repo.SetRateLimited(s.ctx, rateLimited.ID, now.Add(10*time.Minute)), "SetRateLimited")

	s.Require().NoError(s.repo.SetError(s.ctx, overloaded.ID, "boom"), "SetError")

	sched, err := s.repo.ListSchedulableByGroupID(s.ctx, group.ID)
	s.Require().NoError(err, "ListSchedulableByGroupID")
	s.Require().Len(sched, 1, "expected only ok account schedulable")
	s.Require().Equal(okAcc.ID, sched[0].ID)

	s.Require().NoError(s.repo.ClearRateLimit(s.ctx, rateLimited.ID), "ClearRateLimit")
	sched2, err := s.repo.ListSchedulableByGroupID(s.ctx, group.ID)
	s.Require().NoError(err, "ListSchedulableByGroupID after ClearRateLimit")
	s.Require().Len(sched2, 2, "expected 2 schedulable accounts after ClearRateLimit")
}

func (s *AccountRepoSuite) TestListSchedulableByPlatform() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "a1", Platform: service.PlatformAnthropic, Schedulable: true})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "a2", Platform: service.PlatformOpenAI, Schedulable: true})

	accounts, err := s.repo.ListSchedulableByPlatform(s.ctx, service.PlatformAnthropic)
	s.Require().NoError(err)
	s.Require().Len(accounts, 1)
	s.Require().Equal(service.PlatformAnthropic, accounts[0].Platform)
}

func (s *AccountRepoSuite) TestListSchedulableByGroupIDAndPlatform() {
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-sp"})
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "a1", Platform: service.PlatformAnthropic, Schedulable: true})
	a2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "a2", Platform: service.PlatformOpenAI, Schedulable: true})
	mustBindAccountToGroup(s.T(), s.client, a1.ID, group.ID, 1)
	mustBindAccountToGroup(s.T(), s.client, a2.ID, group.ID, 2)

	accounts, err := s.repo.ListSchedulableByGroupIDAndPlatform(s.ctx, group.ID, service.PlatformAnthropic)
	s.Require().NoError(err)
	s.Require().Len(accounts, 1)
	s.Require().Equal(a1.ID, accounts[0].ID)
}

func (s *AccountRepoSuite) TestSetSchedulable() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-sched", Schedulable: true})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	s.Require().NoError(s.repo.SetSchedulable(s.ctx, account.ID, false))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(got.Schedulable)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
}

func (s *AccountRepoSuite) TestBulkUpdate_SyncSchedulerSnapshotOnDisabled() {
	account1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bulk-1", Status: service.StatusActive, Schedulable: true})
	account2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bulk-2", Status: service.StatusActive, Schedulable: true})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	disabled := service.StatusDisabled
	rows, err := s.repo.BulkUpdate(s.ctx, []int64{account1.ID, account2.ID}, service.AccountBulkUpdate{
		Status: &disabled,
	})
	s.Require().NoError(err)
	s.Require().Equal(int64(2), rows)

	s.Require().Len(cacheRecorder.setAccounts, 2)
	ids := map[int64]struct{}{}
	for _, acc := range cacheRecorder.setAccounts {
		ids[acc.ID] = struct{}{}
	}
	s.Require().Contains(ids, account1.ID)
	s.Require().Contains(ids, account2.ID)
}

// --- SetOverloaded / SetRateLimited / ClearRateLimit ---

func (s *AccountRepoSuite) TestSetOverloaded() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-over"})
	until := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	s.Require().NoError(s.repo.SetOverloaded(s.ctx, account.ID, until))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.OverloadUntil)
	s.Require().WithinDuration(until, *got.OverloadUntil, time.Second)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
	s.Require().NotNil(cacheRecorder.setAccounts[0].OverloadUntil)
	s.Require().WithinDuration(until, *cacheRecorder.setAccounts[0].OverloadUntil, time.Second)
}

func (s *AccountRepoSuite) TestSetRateLimited() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-rl"})
	resetAt := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)

	s.Require().NoError(s.repo.SetRateLimited(s.ctx, account.ID, resetAt))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.RateLimitedAt)
	s.Require().NotNil(got.RateLimitResetAt)
	s.Require().WithinDuration(resetAt, *got.RateLimitResetAt, time.Second)
}

func (s *AccountRepoSuite) TestSetRateLimitedIfLaterDoesNotShortenReset() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-rl-monotonic"})
	later := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	earlier := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	s.Require().NoError(s.repo.SetRateLimitedIfLater(s.ctx, account.ID, later))
	s.Require().NoError(s.repo.SetRateLimitedIfLater(s.ctx, account.ID, earlier))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.RateLimitResetAt)
	s.Require().WithinDuration(later, *got.RateLimitResetAt, time.Second)
	s.Require().Len(cacheRecorder.setAccounts, 2)
	s.Require().NotNil(cacheRecorder.setAccounts[1].RateLimitResetAt)
	s.Require().WithinDuration(later, *cacheRecorder.setAccounts[1].RateLimitResetAt, time.Second)
}

func (s *AccountRepoSuite) TestClearRateLimitIfObservedProtectsRearmed429Generation() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "acc-rl-conditional-clear",
		Platform: service.PlatformGrok,
		Type:     service.AccountTypeOAuth,
	})
	firstReset := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	rearmedReset := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)

	s.Require().NoError(s.repo.SetRateLimitedIfLater(s.ctx, account.ID, firstReset))
	staleGeneration, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotNil(staleGeneration.RateLimitedAt)
	s.Require().NotNil(staleGeneration.RateLimitResetAt)
	cleared, err := s.repo.ClearRateLimitIfObserved(s.ctx, account.ID, *staleGeneration.RateLimitedAt, *staleGeneration.RateLimitResetAt)
	s.Require().NoError(err)
	s.Require().True(cleared)

	// A newer generation may legitimately re-arm a shorter boundary after the
	// first generation was cleared. The stale success must not erase it.
	s.Require().NoError(s.repo.SetRateLimitedIfLater(s.ctx, account.ID, rearmedReset))
	cleared, err = s.repo.ClearRateLimitIfObserved(s.ctx, account.ID, *staleGeneration.RateLimitedAt, *staleGeneration.RateLimitResetAt)
	s.Require().NoError(err)
	s.Require().False(cleared)

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.RateLimitedAt)
	s.Require().NotNil(got.RateLimitResetAt)
	s.Require().WithinDuration(rearmedReset, *got.RateLimitResetAt, time.Second)

	// An admin can retype the row while the successful OAuth request is still
	// in flight. The stale OAuth recovery must not cross into API-key state even
	// when both observed timestamps still match.
	_, err = s.client.Account.UpdateOneID(account.ID).
		SetType(service.AccountTypeAPIKey).
		Save(s.ctx)
	s.Require().NoError(err)
	cleared, err = s.repo.ClearRateLimitIfObserved(s.ctx, account.ID, *got.RateLimitedAt, *got.RateLimitResetAt)
	s.Require().NoError(err)
	s.Require().False(cleared)

	retyped, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.AccountTypeAPIKey, retyped.Type)
	s.Require().NotNil(retyped.RateLimitedAt)
	s.Require().NotNil(retyped.RateLimitResetAt)
	s.Require().WithinDuration(rearmedReset, *retyped.RateLimitResetAt, time.Second)
}

func (s *AccountRepoSuite) TestClearRateLimit() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-clear"})
	until := time.Now().Add(1 * time.Hour)
	s.Require().NoError(s.repo.SetOverloaded(s.ctx, account.ID, until))
	s.Require().NoError(s.repo.SetRateLimited(s.ctx, account.ID, until))

	s.Require().NoError(s.repo.ClearRateLimit(s.ctx, account.ID))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Nil(got.RateLimitedAt)
	s.Require().Nil(got.RateLimitResetAt)
	s.Require().Nil(got.OverloadUntil)
}

func (s *AccountRepoSuite) TestResetQuotaUsedAndClearRateLimitCooldownPreservesOtherRuntimeState() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "acc-reset-quota-cooldown",
		Extra: map[string]any{
			"quota_used":        12.5,
			"quota_daily_used":  5.0,
			"quota_weekly_used": 9.0,
			"model_rate_limits": map[string]any{
				"claude-sonnet-4-5": map[string]any{"rate_limit_reset_at": "2026-09-01T10:00:00Z"},
			},
		},
	})
	until := time.Now().Add(1 * time.Hour)
	s.Require().NoError(s.repo.SetOverloaded(s.ctx, account.ID, until))
	s.Require().NoError(s.repo.SetRateLimited(s.ctx, account.ID, until))
	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, account.ID, until, "preserve-me"))

	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	s.Require().NoError(s.repo.ResetQuotaUsedAndClearRateLimitCooldown(s.ctx, account.ID))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Nil(got.RateLimitedAt)
	s.Require().Nil(got.RateLimitResetAt)
	s.Require().NotNil(got.OverloadUntil)
	s.Require().WithinDuration(until, *got.OverloadUntil, time.Second)
	s.Require().NotNil(got.TempUnschedulableUntil)
	s.Require().WithinDuration(until, *got.TempUnschedulableUntil, time.Second)
	s.Require().Equal("preserve-me", got.TempUnschedulableReason)
	s.Require().Contains(got.Extra, "model_rate_limits")
	s.Require().Equal(float64(0), got.Extra["quota_used"])
	s.Require().Equal(float64(0), got.Extra["quota_daily_used"])
	s.Require().Equal(float64(0), got.Extra["quota_weekly_used"])

	var pendingEventExists bool
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql, `
		SELECT EXISTS (
			SELECT 1 FROM scheduler_outbox
			WHERE event_type = $1 AND account_id = $2 AND dedup_key IS NOT NULL
		)`, []any{service.SchedulerOutboxEventAccountChanged, account.ID}, &pendingEventExists))
	s.Require().True(pendingEventExists)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
}

func (s *AccountRepoSuite) TestTempUnschedulableFieldsLoadedByGetByIDAndGetByIDs() {
	acc1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-temp-1"})
	acc2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-temp-2"})

	until := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second)
	reason := `{"rule":"429","matched_keyword":"too many requests"}`
	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, acc1.ID, until, reason))

	gotByID, err := s.repo.GetByID(s.ctx, acc1.ID)
	s.Require().NoError(err)
	s.Require().NotNil(gotByID.TempUnschedulableUntil)
	s.Require().WithinDuration(until, *gotByID.TempUnschedulableUntil, time.Second)
	s.Require().Equal(reason, gotByID.TempUnschedulableReason)

	gotByIDs, err := s.repo.GetByIDs(s.ctx, []int64{acc2.ID, acc1.ID})
	s.Require().NoError(err)
	s.Require().Len(gotByIDs, 2)
	s.Require().Equal(acc2.ID, gotByIDs[0].ID)
	s.Require().Nil(gotByIDs[0].TempUnschedulableUntil)
	s.Require().Equal("", gotByIDs[0].TempUnschedulableReason)
	s.Require().Equal(acc1.ID, gotByIDs[1].ID)
	s.Require().NotNil(gotByIDs[1].TempUnschedulableUntil)
	s.Require().WithinDuration(until, *gotByIDs[1].TempUnschedulableUntil, time.Second)
	s.Require().Equal(reason, gotByIDs[1].TempUnschedulableReason)

	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	s.Require().NoError(s.repo.ClearTempUnschedulable(s.ctx, acc1.ID))
	cleared, err := s.repo.GetByID(s.ctx, acc1.ID)
	s.Require().NoError(err)
	s.Require().Nil(cleared.TempUnschedulableUntil)
	s.Require().Equal("", cleared.TempUnschedulableReason)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(acc1.ID, cacheRecorder.setAccounts[0].ID)
	s.Require().Nil(cacheRecorder.setAccounts[0].TempUnschedulableUntil)
	s.Require().Equal("", cacheRecorder.setAccounts[0].TempUnschedulableReason)
}

func (s *AccountRepoSuite) TestSetTempUnschedulableSkipsOutboxWhenWindowDoesNotExtend() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-temp-noop"})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	_, err := s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	until := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, account.ID, until, "first"))

	var count int
	err = scanSingleRow(s.ctx, s.repo.sql, "SELECT COUNT(*) FROM scheduler_outbox", nil, &count)
	s.Require().NoError(err)
	s.Require().Equal(1, count)
	s.Require().Len(cacheRecorder.setAccounts, 1)

	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, account.ID, until.Add(-5*time.Minute), "older"))

	err = scanSingleRow(s.ctx, s.repo.sql, "SELECT COUNT(*) FROM scheduler_outbox", nil, &count)
	s.Require().NoError(err)
	s.Require().Equal(1, count)
	s.Require().Len(cacheRecorder.setAccounts, 1)

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("first", got.TempUnschedulableReason)
	s.Require().NotNil(got.TempUnschedulableUntil)
	s.Require().WithinDuration(until, *got.TempUnschedulableUntil, time.Second)
}

func (s *AccountRepoSuite) TestClearModelRateLimits_SyncsSchedulerSnapshot() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "acc-clear-model-rate",
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				"claude-sonnet-4-5": map[string]any{
					"rate_limit_reset_at": "2026-06-03T10:00:00Z",
				},
			},
		},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	s.Require().NoError(s.repo.ClearModelRateLimits(s.ctx, account.ID))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotContains(got.Extra, "model_rate_limits")
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
	s.Require().NotContains(cacheRecorder.setAccounts[0].Extra, "model_rate_limits")
}

// --- UpdateLastUsed ---

func (s *AccountRepoSuite) TestUpdateLastUsed() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-used"})
	s.Require().Nil(account.LastUsedAt)

	s.Require().NoError(s.repo.UpdateLastUsed(s.ctx, account.ID))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.LastUsedAt)
}

// --- SetError ---

func (s *AccountRepoSuite) TestSetError() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-err", Status: service.StatusActive, Schedulable: true})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	_, err := s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	s.Require().NoError(s.repo.SetError(s.ctx, account.ID, "something went wrong"))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusError, got.Status)
	s.Require().Equal("something went wrong", got.ErrorMessage)
	s.Require().False(got.Schedulable)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
	s.Require().Equal(service.StatusError, cacheRecorder.setAccounts[0].Status)
	s.Require().False(cacheRecorder.setAccounts[0].Schedulable)

	var outboxCount int
	err = scanSingleRow(
		s.ctx,
		s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
		[]any{service.SchedulerOutboxEventAccountChanged, account.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, outboxCount)
}

func (s *AccountRepoSuite) TestSetGrokOAuthErrorIfCredentialsUnchanged_AppliesAndSyncsSchedulerState() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-conditional-error-applied",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "observed", "_token_version": int64(7)},
	})
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	_, err = s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	applied, err := s.repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		s.ctx,
		account.ID,
		observed.Credentials,
		"missing refresh token",
	)

	s.Require().NoError(err)
	s.Require().True(applied)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusError, got.Status)
	s.Require().False(got.Schedulable)
	s.Require().Equal("missing refresh token", got.ErrorMessage)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(service.StatusError, cacheRecorder.setAccounts[0].Status)

	var outboxCount int
	err = scanSingleRow(
		s.ctx,
		s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
		[]any{service.SchedulerOutboxEventAccountChanged, account.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, outboxCount)
}

func (s *AccountRepoSuite) TestSetGrokOAuthErrorIfCredentialsUnchanged_SkipsConcurrentReauthorization() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-conditional-error-reauthorized",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "observed", "_token_version": int64(7)},
	})
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{
		"access_token":   "fresh-access",
		"refresh_token":  "fresh-refresh",
		"expires_at":     time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339),
		"_token_version": int64(8),
	}))
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	_, err = s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	applied, err := s.repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		s.ctx,
		account.ID,
		observed.Credentials,
		"stale reconciliation",
	)

	s.Require().NoError(err)
	s.Require().False(applied)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusActive, got.Status)
	s.Require().True(got.Schedulable)
	s.Require().Equal("fresh-refresh", got.GetGrokRefreshToken())
	s.Require().Empty(cacheRecorder.setAccounts, "a lost compare-and-set race must not rewrite the scheduler snapshot")

	var outboxCount int
	err = scanSingleRow(
		s.ctx,
		s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
		[]any{service.SchedulerOutboxEventAccountChanged, account.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Zero(outboxCount, "a lost compare-and-set race must not enqueue a stale account change")
}

func (s *AccountRepoSuite) TestUpdateGrokOAuthCredentialsIfUnchanged_AppliesAndPublishesSchedulerState() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-refresh-success-cas-applied",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":   "attempted-access",
			"refresh_token":  "attempted-refresh",
			"_token_version": int64(10),
		},
	})
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	_, err = s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	applied, err := s.repo.UpdateGrokOAuthCredentialsIfUnchanged(
		s.ctx,
		account.ID,
		observed.Credentials,
		observed.ProxyID,
		map[string]any{
			"access_token":   "rotated-access",
			"refresh_token":  "rotated-refresh",
			"_token_version": int64(11),
		},
	)

	s.Require().NoError(err)
	s.Require().True(applied)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("rotated-refresh", got.GetGrokRefreshToken())
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal("rotated-refresh", cacheRecorder.setAccounts[0].GetGrokRefreshToken())
	s.Require().NoError(cacheRecorder.setCtxErr)

	var outboxCount int
	err = scanSingleRow(
		s.ctx,
		s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
		[]any{service.SchedulerOutboxEventAccountChanged, account.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Equal(1, outboxCount)
}

func (s *AccountRepoSuite) TestUpdateGrokOAuthCredentialsIfUnchanged_SkipsConcurrentReauthorization() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-refresh-success-cas-reauthorized",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":   "attempted-access",
			"refresh_token":  "attempted-refresh",
			"_token_version": int64(20),
		},
	})
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{
		"access_token":   "reauthorized-access",
		"refresh_token":  "reauthorized-refresh",
		"_token_version": int64(21),
	}))
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	_, err = s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	applied, err := s.repo.UpdateGrokOAuthCredentialsIfUnchanged(
		s.ctx,
		account.ID,
		observed.Credentials,
		observed.ProxyID,
		map[string]any{
			"access_token":   "provider-access",
			"refresh_token":  "provider-refresh",
			"_token_version": int64(22),
		},
	)

	s.Require().NoError(err)
	s.Require().False(applied)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("reauthorized-refresh", got.GetGrokRefreshToken())
	s.Require().Empty(cacheRecorder.setAccounts)

	var outboxCount int
	err = scanSingleRow(
		s.ctx,
		s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
		[]any{service.SchedulerOutboxEventAccountChanged, account.ID},
		&outboxCount,
	)
	s.Require().NoError(err)
	s.Require().Zero(outboxCount)
}

func (s *AccountRepoSuite) TestGrokOAuthConditionalMutation_DetachesBoundedSnapshotSync() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "grok-conditional-detached-sync",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "observed"},
	})
	observed, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	ctx, cancel := context.WithCancel(context.Background())
	cacheRecorder := &schedulerCacheRecorder{}
	repo := newAccountRepositoryWithSQL(s.client, &cancelAfterAtomicMutationSQLExecutor{
		sqlExecutor: s.repo.sql,
		cancel:      cancel,
	}, cacheRecorder)

	applied, err := repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		ctx,
		account.ID,
		observed.Credentials,
		"missing refresh token",
	)

	s.Require().NoError(err)
	s.Require().True(applied)
	s.Require().ErrorIs(ctx.Err(), context.Canceled)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().NoError(cacheRecorder.setCtxErr, "immediate scheduler propagation must use a bounded detached context")
}

func TestGrokOAuthConditionalMutationRollsBackWhenOutboxInsertFails(t *testing.T) {
	client := testEntClient(t)
	account := mustCreateAccount(t, client, &service.Account{
		Name:        "grok-conditional-atomic-outbox-failure",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "observed"},
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
		_ = client.Account.DeleteOneID(account.ID).Exec(context.Background())
	})
	repo := newAccountRepositoryWithSQL(client, &failAtomicSchedulerOutboxSQLExecutor{sqlExecutor: integrationDB}, nil)

	applied, err := repo.SetGrokOAuthErrorIfCredentialsUnchanged(
		context.Background(),
		account.ID,
		account.Credentials,
		"missing refresh token",
	)

	require.Error(t, err)
	require.False(t, applied)
	got, readErr := repo.GetByID(context.Background(), account.ID)
	require.NoError(t, readErr)
	require.Equal(t, service.StatusActive, got.Status)
	require.True(t, got.Schedulable)
	require.Empty(t, got.ErrorMessage)
	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(
		context.Background(),
		"SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1",
		account.ID,
	).Scan(&outboxCount))
	require.Zero(t, outboxCount)
}

func (s *AccountRepoSuite) TestUpdateErrorStatusUnschedulesAccount() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-update-err", Status: service.StatusActive, Schedulable: true})
	account.Status = service.StatusError
	account.ErrorMessage = "token revoked"
	account.Schedulable = true

	s.Require().NoError(s.repo.Update(s.ctx, account))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusError, got.Status)
	s.Require().Equal("token revoked", got.ErrorMessage)
	s.Require().False(got.Schedulable)
}

func (s *AccountRepoSuite) TestClearError_SyncSchedulerSnapshotOnRecovery() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:         "acc-clear-err",
		Status:       service.StatusError,
		ErrorMessage: "temporary error",
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder

	s.Require().NoError(s.repo.ClearError(s.ctx, account.ID))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusActive, got.Status)
	s.Require().Empty(got.ErrorMessage)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
	s.Require().Equal(service.StatusActive, cacheRecorder.setAccounts[0].Status)
}

// --- UpdateSessionWindow ---

func (s *AccountRepoSuite) TestUpdateSessionWindow() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-win"})
	start := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 15, 15, 0, 0, 0, time.UTC)

	s.Require().NoError(s.repo.UpdateSessionWindow(s.ctx, account.ID, &start, &end, "active"))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.SessionWindowStart)
	s.Require().NotNil(got.SessionWindowEnd)
	s.Require().Equal("active", got.SessionWindowStatus)
}

func (s *AccountRepoSuite) TestUpdateSessionWindow_SyncsSchedulerSnapshotImmediately() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-win-cache"})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	start := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 15, 15, 0, 0, 0, time.UTC)

	s.Require().NoError(s.repo.UpdateSessionWindow(s.ctx, account.ID, &start, &end, "active"))

	s.Require().Len(cacheRecorder.setAccounts, 1)
	cached := cacheRecorder.setAccounts[0]
	s.Require().NotNil(cached.SessionWindowStart)
	s.Require().NotNil(cached.SessionWindowEnd)
	s.Require().Equal(start, *cached.SessionWindowStart)
	s.Require().Equal(end, *cached.SessionWindowEnd)
	s.Require().Equal("active", cached.SessionWindowStatus)
}

func (s *AccountRepoSuite) TestUpdateSessionWindowEnd_SyncsSchedulerSnapshotImmediately() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-win-end-cache"})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	end := time.Date(2025, 6, 15, 15, 0, 0, 0, time.UTC)

	s.Require().NoError(s.repo.UpdateSessionWindowEnd(s.ctx, account.ID, end))

	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().NotNil(cacheRecorder.setAccounts[0].SessionWindowEnd)
	s.Require().Equal(end, *cacheRecorder.setAccounts[0].SessionWindowEnd)
}

// --- UpdateExtra ---

func (s *AccountRepoSuite) TestUpdateExtra_MergesFields() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:  "acc-extra",
		Extra: map[string]any{"a": "1"},
	})
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{"b": "2"}), "UpdateExtra")

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal("1", got.Extra["a"])
	s.Require().Equal("2", got.Extra["b"])
}

func (s *AccountRepoSuite) TestUpdateExtra_EmptyUpdates() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-extra-empty"})
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{}))
}

func (s *AccountRepoSuite) TestUpdateExtra_NilExtra() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-nil-extra", Extra: nil})
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{"key": "val"}))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("val", got.Extra["key"])
}

func (s *AccountRepoSuite) TestUpdateExtra_SchedulerNeutralSkipsOutboxAndSyncsFreshSnapshot() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "acc-extra-neutral",
		Platform: service.PlatformOpenAI,
		Extra:    map[string]any{"codex_usage_updated_at": "old"},
	})
	cacheRecorder := &schedulerCacheRecorder{
		accounts: map[int64]*service.Account{
			account.ID: {
				ID:       account.ID,
				Platform: account.Platform,
				Status:   service.StatusDisabled,
				Extra: map[string]any{
					"codex_usage_updated_at": "old",
				},
			},
		},
	}
	s.repo.schedulerCache = cacheRecorder

	updates := map[string]any{
		"codex_usage_updated_at":     "2026-03-11T10:00:00Z",
		"codex_5h_used_percent":      88.5,
		"session_window_utilization": 0.42,
	}
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, updates))

	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("2026-03-11T10:00:00Z", got.Extra["codex_usage_updated_at"])
	s.Require().Equal(88.5, got.Extra["codex_5h_used_percent"])
	s.Require().Equal(0.42, got.Extra["session_window_utilization"])

	var outboxCount int
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql, "SELECT COUNT(*) FROM scheduler_outbox", nil, &outboxCount))
	s.Require().Zero(outboxCount)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().NotNil(cacheRecorder.accounts[account.ID])
	s.Require().Equal(service.StatusActive, cacheRecorder.accounts[account.ID].Status)
	s.Require().Equal("2026-03-11T10:00:00Z", cacheRecorder.accounts[account.ID].Extra["codex_usage_updated_at"])
}

func (s *AccountRepoSuite) TestUpdateExtra_ExhaustedCodexSnapshotSyncsSchedulerCache() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "acc-extra-codex-exhausted",
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Extra:    map[string]any{},
	})
	cacheRecorder := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cacheRecorder
	_, err := s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{
		"codex_7d_used_percent":        100.0,
		"codex_7d_reset_at":            "2026-03-12T13:00:00Z",
		"codex_7d_reset_after_seconds": 86400,
	}))

	var count int
	err = scanSingleRow(s.ctx, s.repo.sql, "SELECT COUNT(*) FROM scheduler_outbox", nil, &count)
	s.Require().NoError(err)
	s.Require().Equal(0, count)
	s.Require().Len(cacheRecorder.setAccounts, 1)
	s.Require().Equal(account.ID, cacheRecorder.setAccounts[0].ID)
	s.Require().Equal(service.StatusActive, cacheRecorder.setAccounts[0].Status)
	s.Require().Equal(100.0, cacheRecorder.setAccounts[0].Extra["codex_7d_used_percent"])
}

func (s *AccountRepoSuite) TestUpdateExtra_SchedulerRelevantStillEnqueuesOutbox() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "acc-extra-mixed",
		Platform: service.PlatformAntigravity,
		Extra:    map[string]any{},
	})
	_, err := s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{
		"mixed_scheduling":       true,
		"codex_usage_updated_at": "2026-03-11T10:00:00Z",
	}))

	var count int
	err = scanSingleRow(s.ctx, s.repo.sql, "SELECT COUNT(*) FROM scheduler_outbox", nil, &count)
	s.Require().NoError(err)
	s.Require().Equal(1, count)
}

// --- GetByCRSAccountID ---

func (s *AccountRepoSuite) TestGetByCRSAccountID() {
	crsID := "crs-12345"
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:  "acc-crs",
		Extra: map[string]any{"crs_account_id": crsID},
	})

	got, err := s.repo.GetByCRSAccountID(s.ctx, crsID)
	s.Require().NoError(err)
	s.Require().NotNil(got)
	s.Require().Equal("acc-crs", got.Name)
}

func (s *AccountRepoSuite) TestGetByCRSAccountID_NotFound() {
	got, err := s.repo.GetByCRSAccountID(s.ctx, "non-existent")
	s.Require().NoError(err)
	s.Require().Nil(got)
}

func (s *AccountRepoSuite) TestGetByCRSAccountID_EmptyString() {
	got, err := s.repo.GetByCRSAccountID(s.ctx, "")
	s.Require().NoError(err)
	s.Require().Nil(got)
}

// TestGetByCRSAccountID_ExcludesSparkShadow 验证外审第7轮 P1:即便 spark 影子的 Extra 被误写入
// crs_account_id,CRS 查询也绝不能命中影子(否则会被当普通账号更新而覆盖 type/credentials/proxy)。
func (s *AccountRepoSuite) TestGetByCRSAccountID_ExcludesSparkShadow() {
	crsID := "crs-shadow-only-99"
	parent := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "crs-mother", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "crs-shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		ParentAccountID: &parent.ID,
		QuotaDimension:  service.QuotaDimensionSpark,
		Extra:           map[string]any{"crs_account_id": crsID},
	})

	got, err := s.repo.GetByCRSAccountID(s.ctx, crsID)
	s.Require().NoError(err)
	s.Require().Nil(got, "spark 影子即便带 crs_account_id 也不应被 CRS 命中")
}

// TestListCRSAccountIDs_ExcludesSparkShadow 验证外审第7轮 P1:影子的 crs_account_id 不应进入
// CRS 同步映射(否则后续 CRS 同步会把影子当普通账号更新)。
func (s *AccountRepoSuite) TestListCRSAccountIDs_ExcludesSparkShadow() {
	parent := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "crs-list-mother", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
	})
	shadowCRSID := "crs-list-shadow-77"
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "crs-list-shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		ParentAccountID: &parent.ID,
		QuotaDimension:  service.QuotaDimensionSpark,
		Extra:           map[string]any{"crs_account_id": shadowCRSID},
	})

	ids, err := s.repo.ListCRSAccountIDs(s.ctx)
	s.Require().NoError(err)
	_, ok := ids[shadowCRSID]
	s.Require().False(ok, "影子的 crs_account_id 不应进入 CRS 映射")
}

// --- BulkUpdate ---

func (s *AccountRepoSuite) TestBulkUpdate() {
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bulk1", Priority: 1})
	a2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bulk2", Priority: 1})

	newPriority := 99
	affected, err := s.repo.BulkUpdate(s.ctx, []int64{a1.ID, a2.ID}, service.AccountBulkUpdate{
		Priority: &newPriority,
	})
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(affected, int64(1), "expected at least one affected row")

	got1, _ := s.repo.GetByID(s.ctx, a1.ID)
	got2, _ := s.repo.GetByID(s.ctx, a2.ID)
	s.Require().Equal(99, got1.Priority)
	s.Require().Equal(99, got2.Priority)
}

func (s *AccountRepoSuite) TestBulkUpdate_MergeCredentials() {
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "bulk-cred",
		Credentials: map[string]any{"existing": "value"},
	})

	_, err := s.repo.BulkUpdate(s.ctx, []int64{a1.ID}, service.AccountBulkUpdate{
		Credentials: map[string]any{"new_key": "new_value"},
	})
	s.Require().NoError(err)

	got, _ := s.repo.GetByID(s.ctx, a1.ID)
	s.Require().Equal("value", got.Credentials["existing"])
	s.Require().Equal("new_value", got.Credentials["new_key"])
}

func (s *AccountRepoSuite) TestBulkUpdate_MergeExtra() {
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:  "bulk-extra",
		Extra: map[string]any{"existing": "val"},
	})

	_, err := s.repo.BulkUpdate(s.ctx, []int64{a1.ID}, service.AccountBulkUpdate{
		Extra: map[string]any{"new_key": "new_val"},
	})
	s.Require().NoError(err)

	got, _ := s.repo.GetByID(s.ctx, a1.ID)
	s.Require().Equal("val", got.Extra["existing"])
	s.Require().Equal("new_val", got.Extra["new_key"])
}

func (s *AccountRepoSuite) TestBulkUpdate_EmptyIDs() {
	affected, err := s.repo.BulkUpdate(s.ctx, []int64{}, service.AccountBulkUpdate{})
	s.Require().NoError(err)
	s.Require().Zero(affected)
}

func (s *AccountRepoSuite) TestBulkUpdate_EmptyUpdates() {
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "bulk-empty"})

	affected, err := s.repo.BulkUpdate(s.ctx, []int64{a1.ID}, service.AccountBulkUpdate{})
	s.Require().NoError(err)
	s.Require().Zero(affected)
}

func idsOfAccounts(accounts []service.Account) []int64 {
	out := make([]int64, 0, len(accounts))
	for i := range accounts {
		out = append(out, accounts[i].ID)
	}
	return out
}
