package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration222DefaultsOpenAIOAuthCodexPolicy(t *testing.T) {
	content, err := FS.ReadFile("222_default_codex_oauth_account_policy.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "platform = 'openai'")
	require.Contains(t, sql, "type = 'oauth'")
	require.Contains(t, sql, "codex_cli_only")
	require.Contains(t, sql, "codex_cli_only_allow_app_server")
	require.Contains(t, sql, "codex_fingerprint_mode")
	require.Contains(t, sql, `'"session"'`)
	require.Equal(t, 3, strings.Count(sql, "UPDATE accounts"))
	require.Contains(t, sql, "NOT (COALESCE(extra, '{}'::jsonb) ? 'codex_cli_only')")
	require.Contains(t, sql, "NOT (COALESCE(extra, '{}'::jsonb) ? 'codex_cli_only_allow_app_server')")
	require.Contains(t, sql, "NOT (COALESCE(extra, '{}'::jsonb) ? 'codex_fingerprint_mode')")
}
