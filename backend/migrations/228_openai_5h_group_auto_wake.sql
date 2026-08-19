-- Group-scoped automatic OpenAI OAuth 5h-window wake scheduling.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS openai_5h_auto_wake_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS openai_5h_auto_wake_last_checked_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS openai_5h_auto_wake_last_candidate_pool_count INTEGER NULL,
    ADD COLUMN IF NOT EXISTS openai_5h_auto_wake_last_reason VARCHAR(64) NULL,
    ADD COLUMN IF NOT EXISTS openai_5h_auto_wake_last_task_id BIGINT NULL,
    ADD COLUMN IF NOT EXISTS openai_5h_auto_wake_last_task_status VARCHAR(32) NULL;

ALTER TABLE openai_5h_wake_tasks
    ADD COLUMN IF NOT EXISTS trigger_type VARCHAR(32) NOT NULL DEFAULT 'manual',
    ADD COLUMN IF NOT EXISTS group_id BIGINT NULL REFERENCES groups(id) ON DELETE SET NULL;

DROP INDEX IF EXISTS openai_5h_wake_tasks_one_active_idx;

CREATE UNIQUE INDEX IF NOT EXISTS openai_5h_wake_tasks_one_manual_active_idx
    ON openai_5h_wake_tasks ((TRUE))
    WHERE trigger_type = 'manual' AND status IN ('pending', 'running');

CREATE UNIQUE INDEX IF NOT EXISTS openai_5h_wake_tasks_one_group_auto_active_idx
    ON openai_5h_wake_tasks (group_id)
    WHERE trigger_type = 'group_auto'
      AND group_id IS NOT NULL
      AND status IN ('pending', 'running');

CREATE INDEX IF NOT EXISTS openai_5h_wake_tasks_trigger_status_idx
    ON openai_5h_wake_tasks (trigger_type, status, id);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'openai_5h_wake_tasks_trigger_type_check'
    ) THEN
        ALTER TABLE openai_5h_wake_tasks
            ADD CONSTRAINT openai_5h_wake_tasks_trigger_type_check
            CHECK (trigger_type IN ('manual', 'group_auto'));
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'groups_openai_5h_auto_wake_candidate_count_check'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_openai_5h_auto_wake_candidate_count_check
            CHECK (
                openai_5h_auto_wake_last_candidate_pool_count IS NULL
                OR openai_5h_auto_wake_last_candidate_pool_count >= 0
            );
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'groups_openai_5h_auto_wake_reason_check'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_openai_5h_auto_wake_reason_check
            CHECK (
                openai_5h_auto_wake_last_reason IS NULL
                OR openai_5h_auto_wake_last_reason IN (
                    'no_candidate',
                    'task_created',
                    'skipped_manual_active',
                    'skipped_auto_active',
                    'check_error'
                )
            );
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS openai_5h_wake_pool_leases (
    id BIGSERIAL PRIMARY KEY,
    identity_hash VARCHAR(64) NOT NULL UNIQUE,
    task_id BIGINT NOT NULL REFERENCES openai_5h_wake_tasks(id) ON DELETE CASCADE,
    item_id BIGINT NOT NULL REFERENCES openai_5h_wake_task_items(id) ON DELETE CASCADE,
    lease_owner VARCHAR(128) NOT NULL,
    lease_expires_at TIMESTAMPTZ NOT NULL,
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_5h_wake_pool_leases_item_unique UNIQUE (item_id)
);

CREATE INDEX IF NOT EXISTS openai_5h_wake_pool_leases_task_idx
    ON openai_5h_wake_pool_leases (task_id);
CREATE INDEX IF NOT EXISTS openai_5h_wake_pool_leases_expiry_idx
    ON openai_5h_wake_pool_leases (lease_expires_at);
