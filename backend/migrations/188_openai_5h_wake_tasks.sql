-- Durable administrator-triggered OpenAI OAuth 5h-window wake tasks.
CREATE TABLE IF NOT EXISTS openai_5h_wake_tasks (
    id BIGSERIAL PRIMARY KEY,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    eligible_account_count INTEGER NOT NULL DEFAULT 0,
    active_window_count INTEGER NOT NULL DEFAULT 0,
    estimated_request_count INTEGER NOT NULL DEFAULT 0,
    total_items INTEGER NOT NULL DEFAULT 0,
    processed_items INTEGER NOT NULL DEFAULT 0,
    woken_count INTEGER NOT NULL DEFAULT 0,
    skipped_active_count INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    cancelled_count INTEGER NOT NULL DEFAULT 0,
    requested_by_user_id BIGINT NULL,
    requested_by_email VARCHAR(320) NULL,
    lease_owner VARCHAR(128) NULL,
    lease_expires_at TIMESTAMPTZ NULL,
    heartbeat_at TIMESTAMPTZ NULL,
    earliest_reset_at TIMESTAMPTZ NULL,
    latest_reset_at TIMESTAMPTZ NULL,
    cancel_requested_at TIMESTAMPTZ NULL,
    started_at TIMESTAMPTZ NULL,
    finished_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_5h_wake_tasks_status_check CHECK (
        status IN ('pending', 'running', 'succeeded', 'partial_succeeded', 'failed', 'cancelled')
    )
);

CREATE TABLE IF NOT EXISTS openai_5h_wake_task_items (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL REFERENCES openai_5h_wake_tasks(id) ON DELETE CASCADE,
    identity_hash VARCHAR(64) NOT NULL,
    member_account_ids JSONB NOT NULL,
    attempted_account_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    successful_account_id BIGINT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    error_code VARCHAR(128) NULL,
    reset_at TIMESTAMPTZ NULL,
    started_at TIMESTAMPTZ NULL,
    finished_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_5h_wake_task_items_status_check CHECK (
        status IN ('pending', 'running', 'woken', 'skipped_active', 'failed', 'cancelled')
    ),
    CONSTRAINT openai_5h_wake_task_items_member_ids_array_check CHECK (jsonb_typeof(member_account_ids) = 'array'),
    CONSTRAINT openai_5h_wake_task_items_attempted_ids_array_check CHECK (jsonb_typeof(attempted_account_ids) = 'array')
);

CREATE UNIQUE INDEX IF NOT EXISTS openai_5h_wake_tasks_one_active_idx
    ON openai_5h_wake_tasks ((TRUE))
    WHERE status IN ('pending', 'running');
CREATE INDEX IF NOT EXISTS openai_5h_wake_tasks_status_idx ON openai_5h_wake_tasks (status);
CREATE INDEX IF NOT EXISTS openai_5h_wake_tasks_created_at_idx ON openai_5h_wake_tasks (created_at DESC);
CREATE INDEX IF NOT EXISTS openai_5h_wake_tasks_lease_idx ON openai_5h_wake_tasks (lease_expires_at)
    WHERE status = 'running';
CREATE INDEX IF NOT EXISTS openai_5h_wake_tasks_finished_idx ON openai_5h_wake_tasks (finished_at)
    WHERE finished_at IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS openai_5h_wake_task_items_pool_idx
    ON openai_5h_wake_task_items (task_id, identity_hash);
CREATE INDEX IF NOT EXISTS openai_5h_wake_task_items_dispatch_idx
    ON openai_5h_wake_task_items (task_id, status, id);
