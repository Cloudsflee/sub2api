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
	query := `(?s)WITH completed AS.*status = 'running'.*processed_items = processed_items \+ 1.*EXISTS \(SELECT 1 FROM completed\)`
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
	mock.ExpectQuery(`(?s)UPDATE openai_5h_wake_task_items.*lease_owner = \$2.*lease_expires_at > NOW\(\).*cancel_requested_at IS NULL.*FOR UPDATE SKIP LOCKED`).
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
