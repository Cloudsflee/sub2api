package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type openAI5hWakeRepository struct {
	db *sql.DB
}

func NewOpenAI5hWakeTaskRepository(db *sql.DB) service.OpenAI5hWakeTaskRepository {
	return &openAI5hWakeRepository{db: db}
}

const openAI5hWakeTaskSelect = `
SELECT id, status, eligible_account_count, active_window_count, estimated_request_count,
       total_items, processed_items, woken_count, skipped_active_count, failed_count, cancelled_count,
       requested_by_user_id, requested_by_email, lease_owner, lease_expires_at, heartbeat_at,
       earliest_reset_at, latest_reset_at, cancel_requested_at, started_at, finished_at, created_at, updated_at
FROM openai_5h_wake_tasks`

const openAI5hWakeItemSelect = `
SELECT id, task_id, identity_hash, member_account_ids, attempted_account_ids,
       successful_account_id, status, attempt_count, error_code, reset_at,
       started_at, finished_at, created_at, updated_at
FROM openai_5h_wake_task_items`

const openAI5hWakeEventSelect = `
SELECT id, task_id, item_id, level, code, message, created_at
FROM openai_5h_wake_task_events`

type sqlScanner interface {
	Scan(dest ...any) error
}

func scanOpenAI5hWakeTask(scanner sqlScanner) (*service.OpenAI5hWakeTask, error) {
	var task service.OpenAI5hWakeTask
	var requestedBy sql.NullInt64
	var requestedEmail, leaseOwner sql.NullString
	var leaseExpires, heartbeat, earliestReset, latestReset sql.NullTime
	var cancelRequested, started, finished sql.NullTime
	if err := scanner.Scan(
		&task.ID, &task.Status, &task.EligibleAccountCount, &task.ActiveWindowCount, &task.EstimatedRequestCount,
		&task.TotalItems, &task.ProcessedItems, &task.WokenCount, &task.SkippedActiveCount, &task.FailedCount, &task.CancelledCount,
		&requestedBy, &requestedEmail, &leaseOwner, &leaseExpires, &heartbeat,
		&earliestReset, &latestReset, &cancelRequested, &started, &finished, &task.CreatedAt, &task.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if requestedBy.Valid {
		value := requestedBy.Int64
		task.RequestedByUserID = &value
	}
	if requestedEmail.Valid {
		task.RequestedByEmail = requestedEmail.String
	}
	if leaseOwner.Valid {
		task.LeaseOwner = leaseOwner.String
	}
	task.LeaseExpiresAt = nullTimePtr(leaseExpires)
	task.HeartbeatAt = nullTimePtr(heartbeat)
	task.EarliestResetAt = nullTimePtr(earliestReset)
	task.LatestResetAt = nullTimePtr(latestReset)
	task.CancelRequestedAt = nullTimePtr(cancelRequested)
	task.StartedAt = nullTimePtr(started)
	task.FinishedAt = nullTimePtr(finished)
	task.AlignmentSpanSeconds = task.ComputeAlignmentSpanSeconds()
	return &task, nil
}

func scanOpenAI5hWakeItem(scanner sqlScanner) (*service.OpenAI5hWakeTaskItem, error) {
	var item service.OpenAI5hWakeTaskItem
	var memberJSON, attemptedJSON []byte
	var successfulAccount sql.NullInt64
	var errorCode sql.NullString
	var resetAt, startedAt, finishedAt sql.NullTime
	if err := scanner.Scan(
		&item.ID, &item.TaskID, &item.IdentityHash, &memberJSON, &attemptedJSON,
		&successfulAccount, &item.Status, &item.AttemptCount, &errorCode, &resetAt,
		&startedAt, &finishedAt, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(memberJSON, &item.MemberAccountIDs); err != nil {
		return nil, fmt.Errorf("decode wake item member ids: %w", err)
	}
	if len(attemptedJSON) > 0 {
		if err := json.Unmarshal(attemptedJSON, &item.AttemptedAccountIDs); err != nil {
			return nil, fmt.Errorf("decode wake item attempted ids: %w", err)
		}
	}
	if item.AttemptedAccountIDs == nil {
		item.AttemptedAccountIDs = []int64{}
	}
	if successfulAccount.Valid {
		value := successfulAccount.Int64
		item.SuccessfulAccountID = &value
	}
	if errorCode.Valid {
		item.ErrorCode = errorCode.String
	}
	item.ResetAt = nullTimePtr(resetAt)
	item.StartedAt = nullTimePtr(startedAt)
	item.FinishedAt = nullTimePtr(finishedAt)
	return &item, nil
}

func scanOpenAI5hWakeEvent(scanner sqlScanner) (*service.OpenAI5hWakeTaskEvent, error) {
	var event service.OpenAI5hWakeTaskEvent
	var itemID sql.NullInt64
	if err := scanner.Scan(
		&event.ID, &event.TaskID, &itemID, &event.Level, &event.Code, &event.Message, &event.CreatedAt,
	); err != nil {
		return nil, err
	}
	if itemID.Valid {
		value := itemID.Int64
		event.ItemID = &value
	}
	return &event, nil
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func isWakeUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr != nil &&
		pqErr.Code == "23505" && pqErr.Constraint == "openai_5h_wake_tasks_one_active_idx"
}

func (r *openAI5hWakeRepository) CreateOrGetActive(ctx context.Context, params service.OpenAI5hWakeCreateParams) (*service.OpenAI5hWakeTask, bool, error) {
	active, err := r.getActiveTask(ctx)
	if err != nil {
		return nil, false, err
	}
	if active != nil {
		return active, false, nil
	}
	if len(params.Items) == 0 {
		return nil, false, service.ErrOpenAI5hWakeNoEligiblePools
	}
	for attempt := 0; attempt < 2; attempt++ {
		task, err := r.createTask(ctx, params)
		if err == nil {
			return task, true, nil
		}
		if !isWakeUniqueViolation(err) {
			return nil, false, err
		}
		active, activeErr := r.getActiveTask(ctx)
		if activeErr != nil {
			return nil, false, activeErr
		}
		if active != nil {
			return active, false, nil
		}
	}
	return nil, false, fmt.Errorf("active OpenAI 5h wake task changed while creating task")
}

func (r *openAI5hWakeRepository) createTask(ctx context.Context, params service.OpenAI5hWakeCreateParams) (*service.OpenAI5hWakeTask, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	row := tx.QueryRowContext(ctx, `
INSERT INTO openai_5h_wake_tasks (
    status, eligible_account_count, active_window_count, estimated_request_count,
    total_items, requested_by_user_id, requested_by_email, created_at, updated_at
) VALUES ('pending', $1, $2, $3, $4, $5, NULLIF($6, ''), $7, $7)
RETURNING id, status, eligible_account_count, active_window_count, estimated_request_count,
          total_items, processed_items, woken_count, skipped_active_count, failed_count, cancelled_count,
          requested_by_user_id, requested_by_email, lease_owner, lease_expires_at, heartbeat_at,
          earliest_reset_at, latest_reset_at, cancel_requested_at, started_at, finished_at, created_at, updated_at`,
		params.EligibleAccountCount, params.ActiveWindowCount, params.EstimatedRequestCount,
		len(params.Items), params.RequestedByUserID, params.RequestedByEmail, now)
	task, err := scanOpenAI5hWakeTask(row)
	if err != nil {
		return nil, err
	}

	for _, item := range params.Items {
		memberJSON, marshalErr := json.Marshal(item.MemberAccountIDs)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if _, err = tx.ExecContext(ctx, `
INSERT INTO openai_5h_wake_task_items (
    task_id, identity_hash, member_account_ids, attempted_account_ids, status, created_at, updated_at
) VALUES ($1, $2, $3, '[]'::jsonb, 'pending', $4, $4)`, task.ID, item.IdentityHash, memberJSON, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return task, nil
}

func (r *openAI5hWakeRepository) getActiveTask(ctx context.Context) (*service.OpenAI5hWakeTask, error) {
	task, err := scanOpenAI5hWakeTask(r.db.QueryRowContext(ctx, openAI5hWakeTaskSelect+`
 WHERE status IN ('pending', 'running') ORDER BY id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return task, err
}

func (r *openAI5hWakeRepository) GetTask(ctx context.Context, id int64) (*service.OpenAI5hWakeTask, error) {
	task, err := scanOpenAI5hWakeTask(r.db.QueryRowContext(ctx, openAI5hWakeTaskSelect+` WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAI5hWakeTaskNotFound
	}
	return task, err
}

func (r *openAI5hWakeRepository) GetLatestTask(ctx context.Context) (*service.OpenAI5hWakeTask, error) {
	task, err := scanOpenAI5hWakeTask(r.db.QueryRowContext(ctx, openAI5hWakeTaskSelect+` ORDER BY id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return task, err
}

func (r *openAI5hWakeRepository) CountRunningTaskItems(ctx context.Context, taskID int64) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM openai_5h_wake_task_items WHERE task_id = $1 AND status = 'running'`, taskID).Scan(&count)
	return count, err
}

func (r *openAI5hWakeRepository) ListTaskItems(ctx context.Context, taskID int64, page, pageSize int) ([]*service.OpenAI5hWakeTaskItem, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_5h_wake_task_items WHERE task_id = $1`, taskID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		if _, err := r.GetTask(ctx, taskID); err != nil {
			return nil, 0, err
		}
	}
	rows, err := r.db.QueryContext(ctx, openAI5hWakeItemSelect+`
 WHERE task_id = $1 ORDER BY id ASC LIMIT $2 OFFSET $3`, taskID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]*service.OpenAI5hWakeTaskItem, 0, pageSize)
	for rows.Next() {
		item, scanErr := scanOpenAI5hWakeItem(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (r *openAI5hWakeRepository) ListTaskEvents(ctx context.Context, taskID int64, page, pageSize int) ([]*service.OpenAI5hWakeTaskEvent, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_5h_wake_task_events WHERE task_id = $1`, taskID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		if _, err := r.GetTask(ctx, taskID); err != nil {
			return nil, 0, err
		}
	}
	rows, err := r.db.QueryContext(ctx, openAI5hWakeEventSelect+`
 WHERE task_id = $1 ORDER BY id DESC LIMIT $2 OFFSET $3`, taskID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]*service.OpenAI5hWakeTaskEvent, 0, pageSize)
	for rows.Next() {
		event, scanErr := scanOpenAI5hWakeEvent(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		events = append(events, event)
	}
	return events, total, rows.Err()
}

func (r *openAI5hWakeRepository) AppendTaskEvent(ctx context.Context, params service.OpenAI5hWakeTaskEventParams) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO openai_5h_wake_task_events (task_id, item_id, level, code, message, created_at)
VALUES ($1, $2, $3, $4, $5, NOW())`,
		params.TaskID, params.ItemID, params.Level, params.Code, params.Message)
	return err
}

func (r *openAI5hWakeRepository) ClaimTask(ctx context.Context, owner string, now, leaseUntil time.Time) (*service.OpenAI5hWakeTask, error) {
	row := r.db.QueryRowContext(ctx, `
UPDATE openai_5h_wake_tasks
SET status = 'running', lease_owner = $1,
    lease_expires_at = NOW() + ($3::timestamptz - $2::timestamptz),
    heartbeat_at = NOW(), started_at = COALESCE(started_at, NOW()), updated_at = NOW()
WHERE id = (
    SELECT id FROM openai_5h_wake_tasks
    WHERE status = 'pending'
       OR (status = 'running' AND (lease_expires_at IS NULL OR lease_expires_at < NOW()))
    ORDER BY id ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING id, status, eligible_account_count, active_window_count, estimated_request_count,
          total_items, processed_items, woken_count, skipped_active_count, failed_count, cancelled_count,
          requested_by_user_id, requested_by_email, lease_owner, lease_expires_at, heartbeat_at,
          earliest_reset_at, latest_reset_at, cancel_requested_at, started_at, finished_at, created_at, updated_at`, owner, now, leaseUntil)
	task, err := scanOpenAI5hWakeTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return task, err
}

func (r *openAI5hWakeRepository) HeartbeatTask(ctx context.Context, taskID int64, owner string, now, leaseUntil time.Time) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
UPDATE openai_5h_wake_tasks
SET heartbeat_at = NOW(),
    lease_expires_at = NOW() + ($4::timestamptz - $3::timestamptz),
    updated_at = NOW()
WHERE id = $1 AND status = 'running' AND lease_owner = $2
  AND lease_expires_at > NOW()`, taskID, owner, now, leaseUntil)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *openAI5hWakeRepository) RecoverTaskItems(ctx context.Context, taskID int64, owner string, maxAttempts int) (int, error) {
	if maxAttempts < 1 {
		return 0, fmt.Errorf("max wake item attempts must be positive")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var cancelRequested bool
	err = tx.QueryRowContext(ctx, `
SELECT cancel_requested_at IS NOT NULL
FROM openai_5h_wake_tasks
WHERE id = $1 AND status = 'running' AND lease_owner = $2
  AND lease_expires_at > NOW()
FOR UPDATE`, taskID, owner).Scan(&cancelRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("wake task %d lease is no longer owned", taskID)
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `
UPDATE openai_5h_wake_task_items
SET status = 'pending', attempted_account_ids = '[]'::jsonb,
    successful_account_id = NULL, error_code = NULL, reset_at = NULL,
    finished_at = NULL, updated_at = NOW()
WHERE task_id = $1 AND status = 'running'`, taskID); err != nil {
		return 0, err
	}

	var exhausted int64
	if !cancelRequested {
		result, updateErr := tx.ExecContext(ctx, `
UPDATE openai_5h_wake_task_items
SET status = 'failed', error_code = 'worker_retry_exhausted',
    successful_account_id = NULL, reset_at = NULL,
    finished_at = NOW(), updated_at = NOW()
WHERE task_id = $1 AND status = 'pending' AND attempt_count >= $2`, taskID, maxAttempts)
		if updateErr != nil {
			return 0, updateErr
		}
		exhausted, err = result.RowsAffected()
		if err != nil {
			return 0, err
		}
	}

	result, err := tx.ExecContext(ctx, `
UPDATE openai_5h_wake_tasks AS task
SET processed_items = progress.processed_items,
    woken_count = progress.woken_count,
    skipped_active_count = progress.skipped_active_count,
    failed_count = progress.failed_count,
    cancelled_count = progress.cancelled_count,
    earliest_reset_at = progress.earliest_reset_at,
    latest_reset_at = progress.latest_reset_at,
    updated_at = NOW()
FROM (
    SELECT COUNT(*) FILTER (WHERE status IN ('woken', 'skipped_active', 'failed', 'cancelled'))::integer AS processed_items,
           COUNT(*) FILTER (WHERE status = 'woken')::integer AS woken_count,
           COUNT(*) FILTER (WHERE status = 'skipped_active')::integer AS skipped_active_count,
           COUNT(*) FILTER (WHERE status = 'failed')::integer AS failed_count,
           COUNT(*) FILTER (WHERE status = 'cancelled')::integer AS cancelled_count,
           MIN(reset_at) FILTER (WHERE status IN ('woken', 'skipped_active')) AS earliest_reset_at,
           MAX(reset_at) FILTER (WHERE status IN ('woken', 'skipped_active')) AS latest_reset_at
    FROM openai_5h_wake_task_items
    WHERE task_id = $1
) AS progress
WHERE task.id = $1 AND task.status = 'running' AND task.lease_owner = $2`, taskID, owner)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, fmt.Errorf("wake task %d lease is no longer owned", taskID)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(exhausted), nil
}

func (r *openAI5hWakeRepository) ClaimNextItem(ctx context.Context, taskID int64, owner string) (*service.OpenAI5hWakeTaskItem, error) {
	row := r.db.QueryRowContext(ctx, `
WITH owned_task AS MATERIALIZED (
    SELECT id FROM openai_5h_wake_tasks
    WHERE id = $1 AND status = 'running' AND lease_owner = $2
      AND lease_expires_at > NOW() AND cancel_requested_at IS NULL
    FOR UPDATE
), next_item AS (
    SELECT item.id
    FROM openai_5h_wake_task_items AS item
    WHERE item.task_id = $1 AND item.status = 'pending'
      AND EXISTS (SELECT 1 FROM owned_task)
    ORDER BY item.id ASC
    LIMIT 1
    FOR UPDATE OF item SKIP LOCKED
)
UPDATE openai_5h_wake_task_items
SET status = 'running', attempt_count = attempt_count + 1,
    attempted_account_ids = '[]'::jsonb,
    successful_account_id = NULL, error_code = NULL, reset_at = NULL,
    finished_at = NULL,
    started_at = COALESCE(started_at, NOW()), updated_at = NOW()
WHERE id = (SELECT id FROM next_item)
RETURNING id, task_id, identity_hash, member_account_ids, attempted_account_ids,
          successful_account_id, status, attempt_count, error_code, reset_at,
          started_at, finished_at, created_at, updated_at`, taskID, owner)
	item, err := scanOpenAI5hWakeItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (r *openAI5hWakeRepository) CompleteItem(ctx context.Context, taskID int64, owner string, params service.OpenAI5hWakeCompleteItemParams) (bool, error) {
	attemptedAccountIDs := params.AttemptedAccountIDs
	if attemptedAccountIDs == nil {
		attemptedAccountIDs = []int64{}
	}
	attemptedJSON, err := json.Marshal(attemptedAccountIDs)
	if err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `
WITH owned_task AS MATERIALIZED (
    SELECT id FROM openai_5h_wake_tasks
    WHERE id = $1 AND status = 'running' AND lease_owner = $2
      AND lease_expires_at > NOW()
    FOR UPDATE
), completed AS (
    UPDATE openai_5h_wake_task_items
    SET status = $4,
        attempted_account_ids = $5,
        successful_account_id = $6,
        reset_at = $7,
        error_code = NULLIF($8, ''),
        finished_at = NOW(),
        updated_at = NOW()
    WHERE id = $3 AND task_id = $1 AND status = 'running'
      AND EXISTS (SELECT 1 FROM owned_task)
    RETURNING status, reset_at
)
UPDATE openai_5h_wake_tasks
SET processed_items = processed_items + 1,
    woken_count = woken_count + CASE WHEN $4 = 'woken' THEN 1 ELSE 0 END,
    skipped_active_count = skipped_active_count + CASE WHEN $4 = 'skipped_active' THEN 1 ELSE 0 END,
    failed_count = failed_count + CASE WHEN $4 = 'failed' THEN 1 ELSE 0 END,
    cancelled_count = cancelled_count + CASE WHEN $4 = 'cancelled' THEN 1 ELSE 0 END,
    earliest_reset_at = CASE
        WHEN $7::timestamptz IS NULL THEN earliest_reset_at
        WHEN earliest_reset_at IS NULL THEN $7
        ELSE LEAST(earliest_reset_at, $7)
    END,
    latest_reset_at = CASE
        WHEN $7::timestamptz IS NULL THEN latest_reset_at
        WHEN latest_reset_at IS NULL THEN $7
        ELSE GREATEST(latest_reset_at, $7)
    END,
    updated_at = NOW()
WHERE id IN (SELECT id FROM owned_task)
  AND EXISTS (SELECT 1 FROM completed)`,
		taskID, owner, params.ItemID, params.Status, attemptedJSON,
		params.SuccessfulAccountID, params.ResetAt, params.ErrorCode)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *openAI5hWakeRepository) RequestCancel(ctx context.Context, taskID int64, now time.Time) (*service.OpenAI5hWakeTask, bool, error) {
	row := r.db.QueryRowContext(ctx, `
UPDATE openai_5h_wake_tasks
SET cancel_requested_at = $2, updated_at = $2
WHERE id = $1 AND status IN ('pending', 'running') AND cancel_requested_at IS NULL
RETURNING id, status, eligible_account_count, active_window_count, estimated_request_count,
          total_items, processed_items, woken_count, skipped_active_count, failed_count, cancelled_count,
          requested_by_user_id, requested_by_email, lease_owner, lease_expires_at, heartbeat_at,
          earliest_reset_at, latest_reset_at, cancel_requested_at, started_at, finished_at, created_at, updated_at`, taskID, now)
	task, err := scanOpenAI5hWakeTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		task, err = r.GetTask(ctx, taskID)
		return task, false, err
	}
	return task, err == nil, err
}

func (r *openAI5hWakeRepository) IsCancelRequested(ctx context.Context, taskID int64) (bool, error) {
	var requested bool
	err := r.db.QueryRowContext(ctx, `
SELECT cancel_requested_at IS NOT NULL FROM openai_5h_wake_tasks WHERE id = $1`, taskID).Scan(&requested)
	if errors.Is(err, sql.ErrNoRows) {
		return false, service.ErrOpenAI5hWakeTaskNotFound
	}
	return requested, err
}

func (r *openAI5hWakeRepository) FinalizeTask(ctx context.Context, taskID int64, owner string, cancelled bool, now time.Time) (*service.OpenAI5hWakeTask, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var lockedTaskID int64
	var cancelAlreadyRequested bool
	err = tx.QueryRowContext(ctx, `
SELECT id, cancel_requested_at IS NOT NULL
FROM openai_5h_wake_tasks
WHERE id = $1 AND status = 'running' AND lease_owner = $2
  AND lease_expires_at > NOW()
FOR UPDATE`, taskID, owner).Scan(&lockedTaskID, &cancelAlreadyRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("wake task %d lease is no longer owned", taskID)
	}
	if err != nil {
		return nil, err
	}
	// RequestCancel and finalization can race. The row lock above makes this
	// check authoritative: a cancellation committed before finalization must
	// never be lost just because the caller's earlier poll saw false.
	cancelled = cancelled || cancelAlreadyRequested

	if cancelled {
		var cancelledItems int
		if err := tx.QueryRowContext(ctx, `
WITH cancelled AS (
    UPDATE openai_5h_wake_task_items
    SET status = 'cancelled', attempted_account_ids = '[]'::jsonb,
        successful_account_id = NULL, error_code = NULL, reset_at = NULL,
        finished_at = NOW(), updated_at = NOW()
    WHERE task_id = $1 AND status IN ('pending', 'running')
    RETURNING id
)
SELECT COUNT(*) FROM cancelled`, taskID).Scan(&cancelledItems); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `
UPDATE openai_5h_wake_tasks
SET status = 'cancelled',
    processed_items = processed_items + $3,
    cancelled_count = cancelled_count + $3,
    lease_owner = NULL, lease_expires_at = NULL,
    finished_at = NOW(), updated_at = NOW()
WHERE id = $1 AND status = 'running' AND lease_owner = $2
  AND lease_expires_at > NOW()`, taskID, owner, cancelledItems)
		if err != nil {
			return nil, err
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			if rowsErr != nil {
				return nil, rowsErr
			}
			return nil, fmt.Errorf("wake task %d lease is no longer owned", taskID)
		}
	} else {
		result, err := tx.ExecContext(ctx, `
UPDATE openai_5h_wake_tasks
SET status = CASE
		WHEN total_items = 0 THEN 'failed'
		WHEN failed_count = 0 THEN 'succeeded'
        WHEN woken_count + skipped_active_count > 0 THEN 'partial_succeeded'
        ELSE 'failed'
    END,
    lease_owner = NULL, lease_expires_at = NULL,
    finished_at = NOW(), updated_at = NOW()
WHERE id = $1 AND status = 'running' AND lease_owner = $2
  AND lease_expires_at > NOW()
  AND processed_items >= total_items`, taskID, owner)
		if err != nil {
			return nil, err
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			if rowsErr != nil {
				return nil, rowsErr
			}
			return nil, fmt.Errorf("wake task %d is not ready to finalize", taskID)
		}
	}

	task, err := scanOpenAI5hWakeTask(tx.QueryRowContext(ctx, openAI5hWakeTaskSelect+` WHERE id = $1`, taskID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return task, nil
}

func (r *openAI5hWakeRepository) DeleteTerminalBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
DELETE FROM openai_5h_wake_tasks
WHERE status IN ('succeeded', 'partial_succeeded', 'failed', 'cancelled')
  AND finished_at IS NOT NULL AND finished_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
