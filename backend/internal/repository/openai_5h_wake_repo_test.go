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
}

func openAI5hWakeTaskRow(id int64, status string, now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(openAI5hWakeTaskColumns).AddRow(
		id, status, 4, 1, 3,
		3, 0, 0, 0, 0, 0,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, now, now,
	)
}

func openAI5hWakeTaskRowWithCancel(id int64, status string, now, cancelAt time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(openAI5hWakeTaskColumns).AddRow(
		id, status, 4, 1, 3,
		3, 0, 0, 0, 0, 0,
		nil, nil, nil, nil, nil,
		nil, nil, cancelAt, nil, nil, now, now,
	)
}

func TestOpenAI5hWakeCreateOrGetActiveReturnsConcurrentTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO openai_5h_wake_tasks`).
		WillReturnError(&pq.Error{Code: "23505", Constraint: "openai_5h_wake_tasks_one_active_idx"})
	mock.ExpectRollback()
	mock.ExpectQuery(`(?s)FROM openai_5h_wake_tasks.*status IN \('pending', 'running'\)`).
		WillReturnRows(openAI5hWakeTaskRow(22, service.OpenAI5hWakeTaskStatusRunning, now))

	task, created, err := repo.CreateOrGetActive(context.Background(), service.OpenAI5hWakeCreateParams{})
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

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO openai_5h_wake_tasks`).WillReturnError(constraintErr)
	mock.ExpectRollback()

	task, created, err := repo.CreateOrGetActive(context.Background(), service.OpenAI5hWakeCreateParams{})
	require.Nil(t, task)
	require.False(t, created)
	require.ErrorIs(t, err, constraintErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAI5hWakeClaimTaskIncludesExpiredLeaseTakeover(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	leaseUntil := now.Add(60 * time.Second)

	mock.ExpectQuery(`(?s)UPDATE openai_5h_wake_tasks.*status = 'pending'.*status = 'running'.*lease_expires_at.*FOR UPDATE SKIP LOCKED`).
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

	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_tasks.*lease_expires_at = \$4.*lease_owner = \$2.*lease_expires_at > NOW\(\)`).
		WithArgs(int64(23), "worker-a", now, leaseUntil).
		WillReturnResult(sqlmock.NewResult(0, 0))

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
	query := `(?s)WITH owned_task AS MATERIALIZED \(.*lease_owner = \$2.*FOR UPDATE.*\), completed AS \(.*status = 'running'.*EXISTS \(SELECT 1 FROM owned_task\).*\).*processed_items = processed_items \+ 1.*WHERE id IN \(SELECT id FROM owned_task\).*EXISTS \(SELECT 1 FROM completed\)`
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
	query := `(?s)WITH owned_task AS MATERIALIZED \(.*lease_owner = \$2.*FOR UPDATE.*\), completed AS \(.*status = 'running'.*EXISTS \(SELECT 1 FROM owned_task\).*\).*processed_items = processed_items \+ 1.*WHERE id IN \(SELECT id FROM owned_task\).*EXISTS \(SELECT 1 FROM completed\)`
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
	mock.ExpectQuery(`(?s)WITH owned_task AS MATERIALIZED \(.*lease_owner = \$2.*lease_expires_at > NOW\(\).*cancel_requested_at IS NULL.*FOR UPDATE.*\), next_item AS \(.*FOR UPDATE OF item SKIP LOCKED.*\).*UPDATE openai_5h_wake_task_items`).
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

func TestOpenAI5hWakeRecoversItemsAndRebuildsTaskCounters(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewOpenAI5hWakeTaskRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT cancel_requested_at IS NOT NULL.*lease_owner = \$2.*lease_expires_at > NOW\(\).*FOR UPDATE`).
		WithArgs(int64(5), "worker-a").
		WillReturnRows(sqlmock.NewRows([]string{"cancel_requested"}).AddRow(false))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_task_items.*SET status = 'pending'.*task_id = \$1 AND status = 'running'`).
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
	mock.ExpectQuery(`(?s)SELECT id, cancel_requested_at IS NOT NULL FROM openai_5h_wake_tasks.*status = 'running'.*lease_owner = \$2.*lease_expires_at > \$3.*FOR UPDATE`).
		WithArgs(int64(5), "worker-a", now).
		WillReturnRows(sqlmock.NewRows([]string{"id", "cancel_requested"}).AddRow(5, false))
	mock.ExpectQuery(`(?s)WITH cancelled AS.*UPDATE openai_5h_wake_task_items.*status IN \('pending', 'running'\).*RETURNING id`).
		WithArgs(int64(5), now).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_tasks.*status = 'cancelled'.*processed_items = processed_items \+ \$4.*cancelled_count = cancelled_count \+ \$4`).
		WithArgs(int64(5), "worker-a", now, 2).
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
	mock.ExpectQuery(`(?s)SELECT id, cancel_requested_at IS NOT NULL FROM openai_5h_wake_tasks.*FOR UPDATE`).
		WithArgs(int64(5), "worker-a", now).
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
	mock.ExpectQuery(`(?s)SELECT id, cancel_requested_at IS NOT NULL FROM openai_5h_wake_tasks.*FOR UPDATE`).
		WithArgs(int64(5), "worker-a", now).
		WillReturnRows(sqlmock.NewRows([]string{"id", "cancel_requested"}).AddRow(5, true))
	mock.ExpectQuery(`(?s)WITH cancelled AS.*UPDATE openai_5h_wake_task_items.*status IN \('pending', 'running'\).*RETURNING id`).
		WithArgs(int64(5), now).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`(?s)UPDATE openai_5h_wake_tasks.*status = 'cancelled'.*processed_items = processed_items \+ \$4.*cancelled_count = cancelled_count \+ \$4`).
		WithArgs(int64(5), "worker-a", now, 1).
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
