//go:build unit

package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration227UsesConfiguredTimezoneInTriggers(t *testing.T) {
	content, err := FS.ReadFile("227_group_usage_rollup_timezone_function_fix.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "COALESCE(NULLIF(timezone_name, ''), current_setting('TimeZone'))")
	require.Contains(t, sql, "CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_state()")
	require.Contains(t, sql, "CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_state_after_insert()")
}
