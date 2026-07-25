//go:build unit

package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegularGroupDTODoesNotExposePublicStatusFlag(t *testing.T) {
	regular, err := json.Marshal(Group{})
	require.NoError(t, err)
	require.NotContains(t, string(regular), "public_status_enabled")

	admin, err := json.Marshal(AdminGroup{PublicStatusEnabled: true})
	require.NoError(t, err)
	require.Contains(t, string(admin), `"public_status_enabled":true`)
}
