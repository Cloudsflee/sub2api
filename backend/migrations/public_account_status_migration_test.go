package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicAccountStatusMigrationDefaultsPrivate(t *testing.T) {
	content, err := FS.ReadFile("187_add_group_public_status.sql")
	require.NoError(t, err)

	normalized := strings.ToUpper(strings.Join(strings.Fields(string(content)), " "))
	require.Contains(t, normalized, "PUBLIC_STATUS_ENABLED BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, normalized, "WHERE PUBLIC_STATUS_ENABLED = TRUE AND DELETED_AT IS NULL")
}
