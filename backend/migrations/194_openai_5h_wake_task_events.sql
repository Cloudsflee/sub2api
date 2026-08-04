-- Administrator-visible execution timeline for OpenAI 5h wake tasks.
CREATE TABLE IF NOT EXISTS openai_5h_wake_task_events (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL REFERENCES openai_5h_wake_tasks(id) ON DELETE CASCADE,
    item_id BIGINT NULL REFERENCES openai_5h_wake_task_items(id) ON DELETE CASCADE,
    level VARCHAR(16) NOT NULL,
    code VARCHAR(128) NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_5h_wake_task_events_level_check CHECK (level IN ('info', 'warn', 'error'))
);

CREATE INDEX IF NOT EXISTS openai_5h_wake_task_events_task_idx
    ON openai_5h_wake_task_events (task_id, id DESC);
