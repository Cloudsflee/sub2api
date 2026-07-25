-- Public account status is opt-in. Existing and copied groups remain private.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS public_status_enabled BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_groups_public_status_enabled_active
    ON groups (sort_order, id)
    WHERE public_status_enabled = TRUE AND deleted_at IS NULL;
