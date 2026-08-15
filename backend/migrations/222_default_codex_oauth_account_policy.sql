-- Enable the recommended Codex account policy for existing OpenAI OAuth accounts.
-- Only fill missing keys so explicit operator opt-outs remain authoritative.
UPDATE accounts
SET extra = jsonb_set(COALESCE(extra, '{}'::jsonb), '{codex_cli_only}', 'true'::jsonb, true)
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND type = 'oauth'
  AND NOT (COALESCE(extra, '{}'::jsonb) ? 'codex_cli_only');

UPDATE accounts
SET extra = jsonb_set(COALESCE(extra, '{}'::jsonb), '{codex_cli_only_allow_app_server}', 'true'::jsonb, true)
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND type = 'oauth'
  AND NOT (COALESCE(extra, '{}'::jsonb) ? 'codex_cli_only_allow_app_server');

-- The runtime already treats an omitted fingerprint mode as session. Persisting
-- it makes the recommendation visible and stable in exports and the editor.
UPDATE accounts
SET extra = jsonb_set(COALESCE(extra, '{}'::jsonb), '{codex_fingerprint_mode}', '"session"'::jsonb, true)
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND type = 'oauth'
  AND NOT (COALESCE(extra, '{}'::jsonb) ? 'codex_fingerprint_mode');
