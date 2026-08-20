-- Persist the next OpenAI 5h automatic-wake check so workers can sleep until
-- a trusted quota window is actually due.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS openai_5h_auto_wake_next_check_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS groups_openai_5h_auto_wake_due_idx
    ON groups (openai_5h_auto_wake_next_check_at, id)
    WHERE deleted_at IS NULL
      AND platform = 'openai'
      AND status = 'active'
      AND openai_5h_auto_wake_enabled = TRUE;

-- Existing enabled groups must be considered immediately after upgrading.
UPDATE groups
SET openai_5h_auto_wake_next_check_at = NOW()
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND status = 'active'
  AND openai_5h_auto_wake_enabled = TRUE
  AND openai_5h_auto_wake_next_check_at IS NULL;

-- public_status_enabled is meaningful only for OpenAI account-entry groups.
UPDATE groups
SET public_status_enabled = FALSE
WHERE platform IS DISTINCT FROM 'openai'
  AND public_status_enabled = TRUE;
