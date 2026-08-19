-- Speed up automatic scans that ignore quota pools with a recently confirmed
-- reset window recorded by a previous wake task.
CREATE INDEX IF NOT EXISTS openai_5h_wake_task_items_active_pool_history_idx
    ON openai_5h_wake_task_items (identity_hash, reset_at)
    WHERE status IN ('woken', 'skipped_active') AND reset_at IS NOT NULL;
