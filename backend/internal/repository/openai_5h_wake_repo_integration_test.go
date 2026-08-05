//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type openAI5hWakeIntegrationItemSeed struct {
	status       string
	attemptCount int
	resetAt      *time.Time
}

func seedOpenAI5hWakeIntegrationTask(t *testing.T, owner string, seeds []openAI5hWakeIntegrationItemSeed) int64 {
	t.Helper()
	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	var taskID int64
	err = tx.QueryRowContext(context.Background(), `
INSERT INTO openai_5h_wake_tasks (
    status, total_items, lease_owner, lease_expires_at, requested_by_email
)
VALUES ('running', $1, $2, NOW() + INTERVAL '10 minutes', $3)
RETURNING id`, len(seeds), owner, "wake-integration-"+owner).Scan(&taskID)
	require.NoError(t, err)
	for index, seed := range seeds {
		var itemID int64
		err = tx.QueryRowContext(context.Background(), `
INSERT INTO openai_5h_wake_task_items (
    task_id, identity_hash, member_account_ids, status, attempt_count, reset_at
)
VALUES ($1, $2, '[1]'::jsonb, $3, $4, $5)
RETURNING id`, taskID, fmt.Sprintf("%064d", index+1), seed.status, seed.attemptCount, seed.resetAt).Scan(&itemID)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM openai_5h_wake_tasks WHERE id = $1`, taskID)
	})
	return taskID
}

func TestOpenAI5hWakeRecoveryRebuildsCountersFromItemState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	early := now.Add(time.Hour)
	late := now.Add(2 * time.Hour)
	owner := fmt.Sprintf("recovery-%d", now.UnixNano())
	taskID := seedOpenAI5hWakeIntegrationTask(t, owner, []openAI5hWakeIntegrationItemSeed{
		{status: service.OpenAI5hWakeItemStatusRunning, attemptCount: 1},
		{status: service.OpenAI5hWakeItemStatusPending, attemptCount: 3},
		{status: service.OpenAI5hWakeItemStatusWoken, attemptCount: 1, resetAt: &late},
		{status: service.OpenAI5hWakeItemStatusSkippedActive, attemptCount: 1, resetAt: &early},
		{status: service.OpenAI5hWakeItemStatusCancelled, attemptCount: 1},
	})
	repo := NewOpenAI5hWakeTaskRepository(integrationDB).(*openAI5hWakeRepository)

	exhausted, err := repo.RecoverTaskItems(context.Background(), taskID, owner, 3)
	require.NoError(t, err)
	require.Equal(t, 1, exhausted)

	var processed, woken, skipped, failed, cancelled int
	var earliest, latest time.Time
	err = integrationDB.QueryRowContext(context.Background(), `
SELECT processed_items, woken_count, skipped_active_count, failed_count, cancelled_count,
       earliest_reset_at, latest_reset_at
FROM openai_5h_wake_tasks WHERE id = $1`, taskID).
		Scan(&processed, &woken, &skipped, &failed, &cancelled, &earliest, &latest)
	require.NoError(t, err)
	require.Equal(t, 4, processed)
	require.Equal(t, 1, woken)
	require.Equal(t, 1, skipped)
	require.Equal(t, 1, failed)
	require.Equal(t, 1, cancelled)
	require.WithinDuration(t, early, earliest, time.Second)
	require.WithinDuration(t, late, latest, time.Second)

	rows, err := integrationDB.QueryContext(context.Background(), `
SELECT status FROM openai_5h_wake_task_items WHERE task_id = $1 ORDER BY id`, taskID)
	require.NoError(t, err)
	defer rows.Close()
	statuses := make([]string, 0, 5)
	for rows.Next() {
		var status string
		require.NoError(t, rows.Scan(&status))
		statuses = append(statuses, status)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{
		service.OpenAI5hWakeItemStatusPending,
		service.OpenAI5hWakeItemStatusFailed,
		service.OpenAI5hWakeItemStatusWoken,
		service.OpenAI5hWakeItemStatusSkippedActive,
		service.OpenAI5hWakeItemStatusCancelled,
	}, statuses)
}

func TestOpenAI5hWakeRecoveryRejectsDifferentOwner(t *testing.T) {
	now := time.Now().UTC()
	owner := fmt.Sprintf("owner-%d", now.UnixNano())
	taskID := seedOpenAI5hWakeIntegrationTask(t, owner, []openAI5hWakeIntegrationItemSeed{{
		status: service.OpenAI5hWakeItemStatusRunning, attemptCount: 1,
	}})
	repo := NewOpenAI5hWakeTaskRepository(integrationDB).(*openAI5hWakeRepository)

	_, err := repo.RecoverTaskItems(context.Background(), taskID, "another-owner", 3)
	require.ErrorContains(t, err, "lease is no longer owned")
	var status string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
SELECT status FROM openai_5h_wake_task_items WHERE task_id = $1`, taskID).Scan(&status))
	require.Equal(t, service.OpenAI5hWakeItemStatusRunning, status)
}

func TestOpenAI5hWakeCompleteItemDoesNotWriteAfterLeaseTakeover(t *testing.T) {
	now := time.Now().UTC()
	owner := fmt.Sprintf("complete-%d", now.UnixNano())
	taskID := seedOpenAI5hWakeIntegrationTask(t, owner, []openAI5hWakeIntegrationItemSeed{{
		status: service.OpenAI5hWakeItemStatusRunning, attemptCount: 1,
	}})
	repo := NewOpenAI5hWakeTaskRepository(integrationDB).(*openAI5hWakeRepository)
	var itemID int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
SELECT id FROM openai_5h_wake_task_items WHERE task_id = $1`, taskID).Scan(&itemID))

	lockTx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	var lockedID int64
	require.NoError(t, lockTx.QueryRowContext(context.Background(), `
SELECT id FROM openai_5h_wake_tasks WHERE id = $1 FOR UPDATE`, taskID).Scan(&lockedID))

	completeDone := make(chan struct{})
	var completed bool
	var completeErr error
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		completed, completeErr = repo.CompleteItem(ctx, taskID, owner, service.OpenAI5hWakeCompleteItemParams{
			ItemID: itemID, Status: service.OpenAI5hWakeItemStatusWoken,
		})
		close(completeDone)
	}()
	time.Sleep(100 * time.Millisecond)
	_, err = lockTx.ExecContext(context.Background(), `
UPDATE openai_5h_wake_tasks
SET lease_owner = 'new-owner', lease_expires_at = NOW() + INTERVAL '10 minutes'
WHERE id = $1`, taskID)
	require.NoError(t, err)
	require.NoError(t, lockTx.Commit())
	select {
	case <-completeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not unblock after lease takeover")
	}
	require.NoError(t, completeErr)
	require.False(t, completed)

	var status string
	var processed int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
SELECT status FROM openai_5h_wake_task_items WHERE id = $1`, itemID).Scan(&status))
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
SELECT processed_items FROM openai_5h_wake_tasks WHERE id = $1`, taskID).Scan(&processed))
	require.Equal(t, service.OpenAI5hWakeItemStatusRunning, status)
	require.Zero(t, processed)
}

func TestOpenAI5hWakeClaimNextItemRejectsDifferentOwner(t *testing.T) {
	now := time.Now().UTC()
	owner := fmt.Sprintf("claim-%d", now.UnixNano())
	taskID := seedOpenAI5hWakeIntegrationTask(t, owner, []openAI5hWakeIntegrationItemSeed{{
		status: service.OpenAI5hWakeItemStatusPending,
	}})
	repo := NewOpenAI5hWakeTaskRepository(integrationDB).(*openAI5hWakeRepository)

	item, err := repo.ClaimNextItem(context.Background(), taskID, "another-owner")
	require.NoError(t, err)
	require.Nil(t, item)
	var status string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
SELECT status FROM openai_5h_wake_task_items WHERE task_id = $1`, taskID).Scan(&status))
	require.Equal(t, service.OpenAI5hWakeItemStatusPending, status)
}
