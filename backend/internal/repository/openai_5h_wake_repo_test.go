package repository

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

var openAI5hWakeTaskColumns = []string{
	"id", "status", "eligible_account_count", "active_window_count", "estimated_request_count",
	"total_items", "processed_items", "woken_count", "skipped_active_count", "failed_count", "cancelled_count",
	"requested_by_user_id", "requested_by_email", "lease_owner", "lease_expires_at", "heartbeat_at",
	"earliest_reset_at", "latest_reset_at", "cancel_requested_at", "started_at", "finished_at", "created_at", "updated_at",
	"trigger_type", "group_id",
}

func openAI5hWakeTaskRow(id int64, status string, now time.Time) *sqlmock.Rows {
	return openAI5hWakeTaskRowForTrigger(id, status, now, service.OpenAI5hWakeTriggerManual, nil)
}

func openAI5hWakeTaskRowForTrigger(id int64, status string, now time.Time, triggerType string, groupID *int64) *sqlmock.Rows {
	return sqlmock.NewRows(openAI5hWakeTaskColumns).AddRow(
		id, status, 4, 1, 3,
		3, 0, 0, 0, 0, 0,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, now, now,
		triggerType, groupID,
	)
}

func openAI5hWakeTaskRowWithCancel(id int64, status string, now, cancelAt time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(openAI5hWakeTaskColumns).AddRow(
		id, status, 4, 1, 3,
		3, 0, 0, 0, 0, 0,
		nil, nil, nil, nil, nil,
		nil, nil, cancelAt, nil, nil, now, now,
		service.OpenAI5hWakeTriggerManual, nil,
	)
}

func TestOpenAI5hWakeCreateOrGetActiveReturnsConcurrentTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC()

	mock.ExpectQuery(`(?s)FROM openai_5h_wake_tasks.*status IN \('pending', 'running'\)`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(openAI5hWakeCreateLockID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO openai_5h_wake_tasks`).
		WillReturnError(&pq.Error{Code: "23505", Constraint: "openai_5h_wake_tasks_one_active_idx"})
	mock.ExpectRollback()
	mock.ExpectQuery(`(?s)FROM openai_5h_wake_tasks.*status IN \('pending', 'running'\)`).
		WillReturnRows(openAI5hWakeTaskRow(22, service.OpenAI5hWakeTaskStatusRunning, now))

	task, created, err := repo.CreateOrGetActive(context.Background(), service.OpenAI5hWakeCreateParams{
		Items: []service.OpenAI5hWakeTaskItemSeed{{IdentityHash: "pool", MemberAccountIDs: []int64{1}}},
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, int64(22), task.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeCreateOrGetActiveDoesNotHideOtherUniqueViolations(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	constraintErr := &pq.Error{Code: "23505", Constraint: "openai_5h_wake_task_items_task_identity_key"}

	mock.ExpectQuery(`(?s)FROM openai_5h_wake_tasks.*status IN \('pending', 'running'\)`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(openAI5hWakeCreateLockID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO openai_5h_wake_tasks`).WillReturnError(constraintErr)
	mock.ExpectRollback()

	task, created, err := repo.CreateOrGetActive(context.Background(), service.OpenAI5hWakeCreateParams{
		Items: []service.OpenAI5hWakeTaskItemSeed{{IdentityHash: "pool", MemberAccountIDs: []int64{1}}},
	})
	require.Nil(t, task)
	require.False(t, created)
	require.ErrorIs(t, err, constraintErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeCreateOrGetActiveRejectsEmptyTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	mock.ExpectQuery(`(?s)FROM openai_5h_wake_tasks.*status IN \('pending', 'running'\)`).
		WillReturnError(sql.ErrNoRows)

	task, created, err := repo.CreateOrGetActive(context.Background(), service.OpenAI5hWakeCreateParams{})

	require.ErrorIs(t, err, service.ErrOpenAI5hWakeNoEligiblePools)
	require.Nil(t, task)
	require.False(t, created)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeCreateOrGetActiveReturnsActiveTaskForEmptyPlan(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC()

	mock.ExpectQuery(`(?s)FROM openai_5h_wake_tasks.*status IN \('pending', 'running'\)`).
		WillReturnRows(openAI5hWakeTaskRow(24, service.OpenAI5hWakeTaskStatusRunning, now))

	task, created, err := repo.CreateOrGetActive(context.Background(), service.OpenAI5hWakeCreateParams{})

	require.NoError(t, err)
	require.False(t, created)
	require.NotNil(t, task)
	require.Equal(t, int64(24), task.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeClaimTaskIncludesExpiredLeaseTakeover(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	leaseUntil := now.Add(60 * time.Second)

	mock.ExpectQuery(`(?s)UPDATE openai_5h_wake_tasks.*status = 'running'.*lease_expires_at = NOW\(\) \+ \(\$3::timestamptz - \$2::timestamptz\).*heartbeat_at = NOW\(\).*WHERE id = \(.*status = 'pending'.*lease_expires_at < NOW\(\).*FOR UPDATE SKIP LOCKED`).
		WithArgs("worker-a", now, leaseUntil).
		WillReturnRows(openAI5hWakeTaskRow(23, service.OpenAI5hWakeTaskStatusRunning, now))

	task, err := repo.ClaimTask(context.Background(), "worker-a", now, leaseUntil)
	require.NoError(t, err)
	require.Equal(t, int64(23), task.ID)
	require.Equal(t, service.OpenAI5hWakeTaskStatusRunning, task.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeHeartbeatCannotReviveExpiredLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	leaseUntil := now.Add(time.Minute)

	mock.ExpectQuery(`(?s)WITH owned_task AS.*UPDATE openai_5h_wake_tasks.*heartbeat_at = NOW\(\).*lease_expires_at = NOW\(\) \+ \(\$4::timestamptz - \$3::timestamptz\).*renewed_leases.*SELECT EXISTS`).
		WithArgs(int64(23), "worker-a", now, leaseUntil).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	owned, err := repo.HeartbeatTask(context.Background(), 23, "worker-a", now, leaseUntil)
	require.NoError(t, err)
	require.False(t, owned)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeCompleteItemIsAtomicAndIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	resetAt := time.Now().UTC().Add(5 * time.Hour)
	successfulID := int64(7)
	params := service.OpenAI5hWakeCompleteItemParams{
		ItemID:              12,
		Status:              service.OpenAI5hWakeItemStatusWoken,
		AttemptedAccountIDs: []int64{6, 7},
		SuccessfulAccountID: &successfulID,
		ResetAt:             &resetAt,
	}
	query := `(?s)WITH owned_task AS MATERIALIZED \(.*lease_owner = \$2.*FOR UPDATE.*\), completed AS \(.*status = 'running'.*EXISTS \(SELECT 1 FROM owned_task\).*\), released AS \(.*DELETE FROM openai_5h_wake_pool_leases.*EXISTS \(SELECT 1 FROM completed\).*\).*processed_items = processed_items \+ 1.*WHERE id IN \(SELECT id FROM owned_task\).*EXISTS \(SELECT 1 FROM completed\).*EXISTS \(SELECT 1 FROM released\)`
	mock.ExpectExec(query).
		WithArgs(int64(5), "worker-a", int64(12), service.OpenAI5hWakeItemStatusWoken, sqlmock.AnyArg(), &successfulID, &resetAt, "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(query).
		WithArgs(int64(5), "worker-a", int64(12), service.OpenAI5hWakeItemStatusWoken, sqlmock.AnyArg(), &successfulID, &resetAt, "").
		WillReturnResult(sqlmock.NewResult(0, 0))

	completed, err := repo.CompleteItem(context.Background(), 5, "worker-a", params)
	require.NoError(t, err)
	require.True(t, completed)
	completed, err = repo.CompleteItem(context.Background(), 5, "worker-a", params)
	require.NoError(t, err)
	require.False(t, completed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeCompleteItemPersistsNilAttemptsAsJSONArray(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	resetAt := time.Now().UTC().Add(5 * time.Hour)
	successfulID := int64(7)
	query := `(?s)WITH owned_task AS MATERIALIZED \(.*lease_owner = \$2.*FOR UPDATE.*\), completed AS \(.*status = 'running'.*EXISTS \(SELECT 1 FROM owned_task\).*\), released AS \(.*DELETE FROM openai_5h_wake_pool_leases.*EXISTS \(SELECT 1 FROM completed\).*\).*processed_items = processed_items \+ 1.*WHERE id IN \(SELECT id FROM owned_task\).*EXISTS \(SELECT 1 FROM completed\).*EXISTS \(SELECT 1 FROM released\)`
	mock.ExpectExec(query).
		WithArgs(
			int64(5),
			"worker-a",
			int64(12),
			service.OpenAI5hWakeItemStatusSkippedActive,
			[]byte(`[]`),
			&successfulID,
			&resetAt,
			"",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	completed, err := repo.CompleteItem(context.Background(), 5, "worker-a", service.OpenAI5hWakeCompleteItemParams{
		ItemID:              12,
		Status:              service.OpenAI5hWakeItemStatusSkippedActive,
		SuccessfulAccountID: &successfulID,
		ResetAt:             &resetAt,
	})
	require.NoError(t, err)
	require.True(t, completed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeClaimItemRequiresCurrentUnexpiredLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC()
	itemColumns := []string{
		"id", "task_id", "identity_hash", "member_account_ids", "attempted_account_ids",
		"successful_account_id", "status", "attempt_count", "error_code", "reset_at",
		"started_at", "finished_at", "created_at", "updated_at",
	}
	mock.ExpectQuery(`(?s)WITH owned_task AS MATERIALIZED \(.*lease_owner = \$2.*lease_expires_at > NOW\(\).*cancel_requested_at IS NULL.*FOR UPDATE.*\), next_item AS \(.*FOR UPDATE OF item SKIP LOCKED.*\).*UPDATE openai_5h_wake_task_items.*SET status = 'running'.*attempted_account_ids = '\[\]'::jsonb.*successful_account_id = NULL.*error_code = NULL.*reset_at = NULL.*finished_at = NULL`).
		WithArgs(int64(5), "worker-a").
		WillReturnRows(sqlmock.NewRows(itemColumns).AddRow(
			12, 5, "0123456789abcdef", []byte(`[7]`), []byte(`[]`),
			nil, service.OpenAI5hWakeItemStatusRunning, 1, nil, nil,
			now, nil, now, now,
		))

	item, err := repo.ClaimNextItem(context.Background(), 5, "worker-a")
	require.NoError(t, err)
	require.Equal(t, int64(12), item.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeClaimItemWithLeaseWaitsWhenPoolIsOccupied(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo, ok := NewOpenAI5hWakeTaskRepository(db).(*openAI5hWakeRepository)
	require.True(t, ok)
	now := time.Now().UTC().Truncate(time.Second)
	leaseUntil := now.Add(time.Minute)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id FROM openai_5h_wake_tasks.*lease_owner = \$2.*lease_expires_at > NOW\(\).*cancel_requested_at IS NULL.*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))
	mock.ExpectQuery(`(?s)SELECT item.id, item.identity_hash.*NOT EXISTS \(.*openai_5h_wake_pool_leases.*lease_expires_at > NOW\(\).*\).*FOR UPDATE OF item SKIP LOCKED`).
		WithArgs(int64(5)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM openai_5h_wake_task_items.*task_id = \$1 AND status = 'pending'`).
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectCommit()

	item, err := repo.ClaimNextItemWithLease(context.Background(), 5, "worker-a", now, leaseUntil)

	require.Nil(t, item)
	require.ErrorIs(t, err, service.ErrOpenAI5hWakePoolLeaseContended)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeClaimItemWithLeaseAcquiresPoolBeforeRunningItem(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo, ok := NewOpenAI5hWakeTaskRepository(db).(*openAI5hWakeRepository)
	require.True(t, ok)
	now := time.Now().UTC().Truncate(time.Second)
	leaseUntil := now.Add(time.Minute)
	const identityHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	itemColumns := []string{
		"id", "task_id", "identity_hash", "member_account_ids", "attempted_account_ids",
		"successful_account_id", "status", "attempt_count", "error_code", "reset_at",
		"started_at", "finished_at", "created_at", "updated_at",
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id FROM openai_5h_wake_tasks.*lease_owner = \$2.*lease_expires_at > NOW\(\).*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))
	mock.ExpectQuery(`(?s)SELECT item.id, item.identity_hash.*NOT EXISTS \(.*openai_5h_wake_pool_leases.*\).*FOR UPDATE OF item SKIP LOCKED`).
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "identity_hash"}).AddRow(12, identityHash))
	mock.ExpectQuery(`(?s)INSERT INTO openai_5h_wake_pool_leases.*ON CONFLICT \(identity_hash\) DO UPDATE.*lease_expires_at <= NOW\(\).*RETURNING item_id`).
		WithArgs(identityHash, int64(5), int64(12), "worker-a", now, leaseUntil).
		WillReturnRows(sqlmock.NewRows([]string{"item_id"}).AddRow(12))
	mock.ExpectQuery(`(?s)UPDATE openai_5h_wake_task_items.*SET status = 'running'.*WHERE id = \$1 AND task_id = \$2 AND status = 'pending'.*RETURNING`).
		WithArgs(int64(12), int64(5)).
		WillReturnRows(sqlmock.NewRows(itemColumns).AddRow(
			12, 5, identityHash, []byte(`[7]`), []byte(`[]`), nil,
			service.OpenAI5hWakeItemStatusRunning, 1, nil, nil, now, nil, now, now,
		))
	mock.ExpectCommit()

	item, err := repo.ClaimNextItemWithLease(context.Background(), 5, "worker-a", now, leaseUntil)

	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, int64(12), item.ID)
	require.Equal(t, identityHash, item.IdentityHash)
	require.Equal(t, service.OpenAI5hWakeItemStatusRunning, item.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeRecoversItemsAndRebuildsTaskCounters(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT cancel_requested_at IS NOT NULL.*lease_owner = \$2.*lease_expires_at > NOW\(\).*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnRows(sqlmock.NewRows([]string{"cancel_requested"}).AddRow(false))
	mock.ExpectExec(`DELETE FROM openai_5h_wake_pool_leases WHERE task_id = \$1`).
		WithArgs(int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_task_items.*SET status = 'pending'.*attempted_account_ids = '\[\]'::jsonb.*successful_account_id = NULL.*error_code = NULL.*reset_at = NULL.*finished_at = NULL.*task_id = \$1 AND status = 'running'`).
		WithArgs(int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_task_items.*SET status = 'failed'.*error_code = 'worker_retry_exhausted'.*attempt_count >= \$2`).
		WithArgs(int64(5), 3).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_tasks AS task.*processed_items = progress.processed_items.*COUNT\(\*\) FILTER.*FROM openai_5h_wake_task_items.*task.lease_owner = \$2`).
		WithArgs(int64(5), "worker-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	count, err := repo.RecoverTaskItems(context.Background(), 5, "worker-a", 3)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeRecoverySkipsExhaustionAfterCancellation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT cancel_requested_at IS NOT NULL.*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnRows(sqlmock.NewRows([]string{"cancel_requested"}).AddRow(true))
	mock.ExpectExec(`DELETE FROM openai_5h_wake_pool_leases WHERE task_id = \$1`).
		WithArgs(int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_task_items.*SET status = 'pending'`).
		WithArgs(int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_tasks AS task.*processed_items = progress.processed_items`).
		WithArgs(int64(5), "worker-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	count, err := repo.RecoverTaskItems(context.Background(), 5, "worker-a", 3)
	require.NoError(t, err)
	require.Zero(t, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeRecoveryRejectsLostLeaseWithoutChangingItems(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT cancel_requested_at IS NOT NULL.*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = repo.RecoverTaskItems(context.Background(), 5, "worker-a", 3)
	require.ErrorContains(t, err, "lease is no longer owned")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeRejectsInvalidRetryLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)

	_, err = repo.RecoverTaskItems(context.Background(), 5, "worker-a", 0)
	require.ErrorContains(t, err, "must be positive")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeRequestCancelIsIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC().Truncate(time.Second)

	updateQuery := `(?s)UPDATE openai_5h_wake_tasks.*cancel_requested_at = \$2.*status IN \('pending', 'running'\).*cancel_requested_at IS NULL.*RETURNING`
	mock.ExpectQuery(updateQuery).
		WithArgs(int64(5), now).
		WillReturnRows(openAI5hWakeTaskRowWithCancel(5, service.OpenAI5hWakeTaskStatusRunning, now, now))

	task, requested, err := repo.RequestCancel(context.Background(), 5, now)
	require.NoError(t, err)
	require.True(t, requested)
	require.Equal(t, now, task.CancelRequestedAt.UTC())

	mock.ExpectQuery(updateQuery).
		WithArgs(int64(5), now.Add(time.Second)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`(?s)FROM openai_5h_wake_tasks WHERE id = \$1`).
		WithArgs(int64(5)).
		WillReturnRows(openAI5hWakeTaskRowWithCancel(5, service.OpenAI5hWakeTaskStatusRunning, now, now))

	task, requested, err = repo.RequestCancel(context.Background(), 5, now.Add(time.Second))
	require.NoError(t, err)
	require.False(t, requested)
	require.Equal(t, now, task.CancelRequestedAt.UTC())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeRequestCancelLeavesTerminalTaskUntouched(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery(`(?s)UPDATE openai_5h_wake_tasks.*cancel_requested_at IS NULL.*RETURNING`).
		WithArgs(int64(5), now).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`(?s)FROM openai_5h_wake_tasks WHERE id = \$1`).
		WithArgs(int64(5)).
		WillReturnRows(openAI5hWakeTaskRow(5, service.OpenAI5hWakeTaskStatusSucceeded, now))

	task, requested, err := repo.RequestCancel(context.Background(), 5, now)
	require.NoError(t, err)
	require.False(t, requested)
	require.Equal(t, service.OpenAI5hWakeTaskStatusSucceeded, task.Status)
	require.Nil(t, task.CancelRequestedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeFinalizeTaskLocksLeaseBeforeCancellingItems(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, cancel_requested_at IS NOT NULL FROM openai_5h_wake_tasks.*status = 'running'.*lease_owner = \$2.*lease_expires_at > NOW\(\).*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "cancel_requested"}).AddRow(5, false))
	mock.ExpectQuery(`(?s)WITH cancelled AS.*UPDATE openai_5h_wake_task_items.*status = 'cancelled'.*attempted_account_ids = '\[\]'::jsonb.*successful_account_id = NULL.*error_code = NULL.*reset_at = NULL.*finished_at = NOW\(\).*status IN \('pending', 'running'\).*RETURNING id`).
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(`DELETE FROM openai_5h_wake_pool_leases WHERE task_id = \$1`).
		WithArgs(int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_tasks.*status = 'cancelled'.*processed_items = processed_items \+ \$3.*cancelled_count = cancelled_count \+ \$3.*finished_at = NOW\(\).*lease_expires_at > NOW\(\)`).
		WithArgs(int64(5), "worker-a", 2).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT id, status, eligible_account_count.*FROM openai_5h_wake_tasks WHERE id = \$1`).
		WithArgs(int64(5)).
		WillReturnRows(openAI5hWakeTaskRow(5, service.OpenAI5hWakeTaskStatusCancelled, now))
	mock.ExpectCommit()

	task, err := repo.FinalizeTask(context.Background(), 5, "worker-a", true, now)
	require.NoError(t, err)
	require.Equal(t, service.OpenAI5hWakeTaskStatusCancelled, task.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeFinalizeTaskRejectsLostLeaseBeforeChangingItems(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, cancel_requested_at IS NOT NULL FROM openai_5h_wake_tasks.*lease_expires_at > NOW\(\).*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = repo.FinalizeTask(context.Background(), 5, "worker-a", true, now)
	require.ErrorContains(t, err, "lease is no longer owned")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeFinalizeTaskHonorsCancellationCommittedBeforeLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, cancel_requested_at IS NOT NULL FROM openai_5h_wake_tasks.*lease_expires_at > NOW\(\).*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "cancel_requested"}).AddRow(5, true))
	mock.ExpectQuery(`(?s)WITH cancelled AS.*UPDATE openai_5h_wake_task_items.*status = 'cancelled'.*attempted_account_ids = '\[\]'::jsonb.*successful_account_id = NULL.*error_code = NULL.*reset_at = NULL.*finished_at = NOW\(\).*status IN \('pending', 'running'\).*RETURNING id`).
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`DELETE FROM openai_5h_wake_pool_leases WHERE task_id = \$1`).
		WithArgs(int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_tasks.*status = 'cancelled'.*processed_items = processed_items \+ \$3.*cancelled_count = cancelled_count \+ \$3.*finished_at = NOW\(\).*lease_expires_at > NOW\(\)`).
		WithArgs(int64(5), "worker-a", 1).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT id, status, eligible_account_count.*FROM openai_5h_wake_tasks WHERE id = \$1`).
		WithArgs(int64(5)).
		WillReturnRows(openAI5hWakeTaskRowWithCancel(5, service.OpenAI5hWakeTaskStatusCancelled, now, now))
	mock.ExpectCommit()

	task, err := repo.FinalizeTask(context.Background(), 5, "worker-a", false, now)
	require.NoError(t, err)
	require.Equal(t, service.OpenAI5hWakeTaskStatusCancelled, task.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeFinalizeAutomaticTaskReleasesLeasesAndUpdatesGroupStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	groupID := int64(8)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, cancel_requested_at IS NOT NULL FROM openai_5h_wake_tasks.*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "cancel_requested"}).AddRow(5, false))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_tasks.*WHEN failed_count = 0 THEN 'succeeded'.*processed_items >= total_items`).
		WithArgs(int64(5), "worker-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM openai_5h_wake_pool_leases WHERE task_id = \$1`).
		WithArgs(int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT id, status, eligible_account_count.*FROM openai_5h_wake_tasks WHERE id = \$1`).
		WithArgs(int64(5)).
		WillReturnRows(openAI5hWakeTaskRowForTrigger(
			5, service.OpenAI5hWakeTaskStatusSucceeded, now, service.OpenAI5hWakeTriggerGroupAuto, &groupID,
		))
	mock.ExpectExec(`(?s)UPDATE groups.*openai_5h_auto_wake_last_task_status = \$2.*openai_5h_auto_wake_last_task_id = \$1`).
		WithArgs(int64(5), service.OpenAI5hWakeTaskStatusSucceeded).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	task, err := repo.FinalizeTask(context.Background(), 5, "worker-a", false, now)

	require.NoError(t, err)
	require.Equal(t, service.OpenAI5hWakeTriggerGroupAuto, task.TriggerType)
	require.Equal(t, &groupID, task.GroupID)
	require.Equal(t, service.OpenAI5hWakeTaskStatusSucceeded, task.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeAppendsAndListsTaskEvents(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	itemID := int64(12)
	now := time.Now().UTC()

	mock.ExpectExec(`INSERT INTO openai_5h_wake_task_events`).
		WithArgs(int64(5), &itemID, service.OpenAI5hWakeEventLevelError, "item_complete_failed", "constraint violation").
		WillReturnResult(sqlmock.NewResult(1, 1))
	require.NoError(t, repo.AppendTaskEvent(context.Background(), service.OpenAI5hWakeTaskEventParams{
		TaskID: 5, ItemID: &itemID, Level: service.OpenAI5hWakeEventLevelError,
		Code: "item_complete_failed", Message: "constraint violation",
	}))

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM openai_5h_wake_task_events WHERE task_id = \$1`).
		WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT id, task_id, item_id, level, code, message, created_at.*ORDER BY id DESC LIMIT \$2 OFFSET \$3`).
		WithArgs(int64(5), 50, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "task_id", "item_id", "level", "code", "message", "created_at"}).
			AddRow(9, 5, itemID, service.OpenAI5hWakeEventLevelError, "item_complete_failed", "constraint violation", now))

	events, total, err := repo.ListTaskEvents(context.Background(), 5, 1, 50)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, events, 1)
	require.Equal(t, int64(9), events[0].ID)
	require.Equal(t, &itemID, events[0].ItemID)
	require.Equal(t, "constraint violation", events[0].Message)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeCountsRunningTaskItems(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM openai_5h_wake_task_items WHERE task_id = \$1 AND status = 'running'`).
		WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	count, err := repo.CountRunningTaskItems(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeDeletesOnlyOldTerminalTasks(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)

	mock.ExpectExec(`(?s)DELETE FROM openai_5h_wake_tasks.*status IN \('succeeded', 'partial_succeeded', 'failed', 'cancelled'\).*finished_at < \$1`).
		WithArgs(cutoff).
		WillReturnResult(sqlmock.NewResult(0, 3))

	deleted, err := repo.DeleteTerminalBefore(context.Background(), cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(3), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeMigrationEnforcesDurabilityConstraints(t *testing.T) {
	content, err := os.ReadFile("../../migrations/188_openai_5h_wake_tasks.sql")
	require.NoError(t, err)
	sqlText := string(content)
	require.Contains(t, sqlText, "openai_5h_wake_tasks_one_active_idx")
	require.Contains(t, sqlText, "WHERE status IN ('pending', 'running')")
	require.Contains(t, sqlText, "ON DELETE CASCADE")
	require.Contains(t, sqlText, "member_account_ids JSONB")
	require.Contains(t, sqlText, "identity_hash VARCHAR(64)")
	require.NotContains(t, sqlText, "chatgpt_account_id")
	require.NotContains(t, sqlText, "organization_id")

	eventMigration, err := os.ReadFile("../../migrations/194_openai_5h_wake_task_events.sql")
	require.NoError(t, err)
	eventSQL := string(eventMigration)
	require.Contains(t, eventSQL, "openai_5h_wake_task_events")
	require.Contains(t, eventSQL, "ON DELETE CASCADE")
	require.Contains(t, eventSQL, "level IN ('info', 'warn', 'error')")
	require.Contains(t, eventSQL, "task_id, id DESC")

	autoWakeMigration, err := os.ReadFile("../../migrations/228_openai_5h_group_auto_wake.sql")
	require.NoError(t, err)
	autoWakeSQL := string(autoWakeMigration)
	require.Contains(t, autoWakeSQL, "openai_5h_auto_wake_enabled BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, autoWakeSQL, "ADD COLUMN IF NOT EXISTS trigger_type VARCHAR(32) NOT NULL DEFAULT 'manual'")
	require.Contains(t, autoWakeSQL, "DROP INDEX IF EXISTS openai_5h_wake_tasks_one_active_idx")
	require.Contains(t, autoWakeSQL, "openai_5h_wake_tasks_one_manual_active_idx")
	require.Contains(t, autoWakeSQL, "openai_5h_wake_tasks_one_group_auto_active_idx")
	require.Contains(t, autoWakeSQL, "CREATE TABLE IF NOT EXISTS openai_5h_wake_pool_leases")
	require.Contains(t, autoWakeSQL, "id BIGSERIAL PRIMARY KEY")
	require.Contains(t, autoWakeSQL, "identity_hash VARCHAR(64) NOT NULL UNIQUE")
	require.Contains(t, autoWakeSQL, "task_id BIGINT NOT NULL REFERENCES openai_5h_wake_tasks(id) ON DELETE CASCADE")
	require.Contains(t, autoWakeSQL, "item_id BIGINT NOT NULL REFERENCES openai_5h_wake_task_items(id) ON DELETE CASCADE")

	activeHistoryMigration, err := os.ReadFile("../../migrations/229_openai_5h_wake_active_pool_history_idx.sql")
	require.NoError(t, err)
	activeHistorySQL := string(activeHistoryMigration)
	require.Contains(t, activeHistorySQL, "openai_5h_wake_task_items_active_pool_history_idx")
	require.Contains(t, activeHistorySQL, "status IN ('woken', 'skipped_active')")

	dueScheduleMigration, err := os.ReadFile("../../migrations/230_openai_5h_auto_wake_due_schedule.sql")
	require.NoError(t, err)
	dueScheduleSQL := string(dueScheduleMigration)
	require.Contains(t, dueScheduleSQL, "openai_5h_auto_wake_next_check_at TIMESTAMPTZ")
	require.Contains(t, dueScheduleSQL, "groups_openai_5h_auto_wake_due_idx")
	require.Contains(t, dueScheduleSQL, "openai_5h_auto_wake_enabled = TRUE")
	require.Contains(t, dueScheduleSQL, "public_status_enabled = FALSE")
}

func TestOpenAI5hWakeListDueGroupsUsesPersistedDeadline(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewOpenAI5hWakeTaskRepository(db)
	wakeRepo, ok := repo.(*openAI5hWakeRepository)
	require.True(t, ok)
	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(-time.Minute)

	mock.ExpectQuery(`(?s)SELECT id, openai_5h_auto_wake_enabled, status, openai_5h_auto_wake_next_check_at.*openai_5h_auto_wake_next_check_at IS NULL OR openai_5h_auto_wake_next_check_at <= \$1.*ORDER BY openai_5h_auto_wake_next_check_at NULLS FIRST`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"id", "openai_5h_auto_wake_enabled", "status", "openai_5h_auto_wake_next_check_at"}).
			AddRow(int64(7), true, service.StatusActive, next))

	groups, err := wakeRepo.ListDueAutoWakeGroups(context.Background(), now)
	if err != nil {
		t.Fatalf("ListDueAutoWakeGroups() error = %v", err)
	}
	require.Len(t, groups, 1)
	require.Equal(t, int64(7), groups[0].ID)
	require.Equal(t, next, *groups[0].NextCheckAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeNextDeadlineDoesNotLoadAccountRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewOpenAI5hWakeTaskRepository(db)
	wakeRepo, ok := repo.(*openAI5hWakeRepository)
	require.True(t, ok)
	next := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	mock.ExpectQuery(`(?s)SELECT CASE.*openai_5h_auto_wake_next_check_at IS NULL.*MIN\(openai_5h_auto_wake_next_check_at\).*FROM groups`).
		WillReturnRows(sqlmock.NewRows([]string{"case"}).AddRow(next))

	got, err := wakeRepo.GetNextAutoWakeCheckAt(context.Background())
	if err != nil {
		t.Fatalf("GetNextAutoWakeCheckAt() error = %v", err)
	}
	require.Equal(t, next, *got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeGetAutoWakeGroupMapsNextDeadline(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewOpenAI5hWakeTaskRepository(db)
	wakeRepo, ok := repo.(*openAI5hWakeRepository)
	require.True(t, ok)
	next := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	mock.ExpectQuery(`(?s)SELECT id, openai_5h_auto_wake_enabled, status, openai_5h_auto_wake_next_check_at.*WHERE id = \$1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "openai_5h_auto_wake_enabled", "status", "openai_5h_auto_wake_next_check_at"}).
			AddRow(int64(7), true, service.StatusActive, next))

	group, err := wakeRepo.GetAutoWakeGroup(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetAutoWakeGroup() error = %v", err)
	}
	require.NotNil(t, group)
	require.Equal(t, next, *group.NextCheckAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeGetLatestTaskOnlyReturnsManualTasks(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC()

	mock.ExpectQuery(`(?s)FROM openai_5h_wake_tasks.*WHERE trigger_type = 'manual' ORDER BY id DESC LIMIT 1`).
		WillReturnRows(openAI5hWakeTaskRow(31, service.OpenAI5hWakeTaskStatusSucceeded, now))

	task, err := repo.GetLatestTask(context.Background())

	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, int64(31), task.ID)
	require.Equal(t, service.OpenAI5hWakeTriggerManual, task.TriggerType)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeListsRecentlyConfirmedPoolResets(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo, ok := NewOpenAI5hWakeTaskRepository(db).(*openAI5hWakeRepository)
	require.True(t, ok)
	now := time.Now().UTC().Truncate(time.Second)
	resetAt := now.Add(2 * time.Hour)

	mock.ExpectQuery(`(?s)FROM openai_5h_wake_task_items.*status IN \('woken', 'skipped_active'\).*reset_at > \$2.*GROUP BY identity_hash`).
		WithArgs(pq.Array([]string{"pool-a", "pool-b"}), now).
		WillReturnRows(sqlmock.NewRows([]string{"identity_hash", "reset_at"}).
			AddRow("pool-a", resetAt))

	resets, err := repo.ListActiveWakePoolResets(context.Background(), []string{"pool-a", "pool-b"}, now)
	require.NoError(t, err)
	require.Equal(t, resetAt, resets["pool-a"])
	require.NotContains(t, resets, "pool-b")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeUpdateAutoWakeGroupCheckPreservesLastTaskWithoutNewTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo, ok := NewOpenAI5hWakeTaskRepository(db).(*openAI5hWakeRepository)
	require.True(t, ok)
	checkedAt := time.Now().UTC().Truncate(time.Second)

	mock.ExpectExec(`(?s)UPDATE groups.*last_task_id = COALESCE\(\$5, openai_5h_auto_wake_last_task_id\).*WHEN \$5::bigint IS NULL THEN openai_5h_auto_wake_last_task_status.*status = 'active'.*openai_5h_auto_wake_enabled = TRUE`).
		WithArgs(int64(7), checkedAt, 0, service.OpenAI5hAutoWakeReasonNoCandidate, nil, "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	updated, err := repo.UpdateAutoWakeGroupCheck(context.Background(), service.OpenAI5hAutoWakeCheckUpdate{
		GroupID: 7, CheckedAt: checkedAt, Reason: service.OpenAI5hAutoWakeReasonNoCandidate,
	})

	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeUpdateAutoWakeGroupCheckPersistsNextDeadline(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo, ok := NewOpenAI5hWakeTaskRepository(db).(*openAI5hWakeRepository)
	require.True(t, ok)
	checkedAt := time.Now().UTC().Truncate(time.Second)
	next := checkedAt.Add(15 * time.Minute)

	mock.ExpectExec(`(?s)UPDATE groups.*openai_5h_auto_wake_next_check_at = \$7.*status = 'active'.*openai_5h_auto_wake_enabled = TRUE`).
		WithArgs(int64(7), checkedAt, 0, service.OpenAI5hAutoWakeReasonCheckError, nil, "", next).
		WillReturnResult(sqlmock.NewResult(0, 1))

	updated, err := repo.UpdateAutoWakeGroupCheck(context.Background(), service.OpenAI5hAutoWakeCheckUpdate{
		GroupID: 7, CheckedAt: checkedAt, Reason: service.OpenAI5hAutoWakeReasonCheckError, NextCheckAt: &next,
	})
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeGetTaskMapsMissingRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)

	mock.ExpectQuery(`FROM openai_5h_wake_tasks WHERE id = \$1`).WithArgs(int64(404)).WillReturnError(sql.ErrNoRows)
	_, err = repo.GetTask(context.Background(), 404)
	require.ErrorIs(t, err, service.ErrOpenAI5hWakeTaskNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
