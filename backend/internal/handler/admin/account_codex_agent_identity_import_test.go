package admin

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNormalizeCodexImportEntryAcceptsAgentIdentityAuthJSON(t *testing.T) {
	privateKeyBase64 := newCodexAgentIdentityPrivateKey(t)

	item, err := normalizeCodexImportEntry(codexImportEntry{
		Index: 1,
		Value: map[string]any{
			"auth_mode": "agentIdentity",
			"agent_identity": map[string]any{
				"agent_runtime_id":           "runtime-import",
				"agent_private_key":          privateKeyBase64,
				"account_id":                 "account-import",
				"chatgpt_user_id":            "user-import",
				"email":                      "agent@example.invalid",
				"plan_type":                  "pro",
				"chatgpt_account_is_fedramp": false,
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, item)
	require.True(t, item.IsAgentIdentity)
	require.Equal(t, service.OpenAIAuthModeAgentIdentity, item.Credentials["auth_mode"])
	require.Equal(t, "runtime-import", item.Credentials["agent_runtime_id"])
	require.Equal(t, privateKeyBase64, item.Credentials["agent_private_key"])
	require.Equal(t, "account-import", item.Credentials["chatgpt_account_id"])
	require.Equal(t, "user-import", item.Credentials["chatgpt_user_id"])
	require.NotContains(t, item.Credentials, "access_token")
	require.NotContains(t, item.Credentials, "refresh_token")
	require.NotEmpty(t, item.WarningTexts)
}

func TestNormalizeCodexImportEntryAcceptsExportedAgentIdentityAccount(t *testing.T) {
	privateKeyBase64 := newCodexAgentIdentityPrivateKey(t)

	item, err := normalizeCodexImportEntry(codexImportEntry{
		Index: 1,
		Value: map[string]any{
			"name": "Exported agent account",
			"credentials": map[string]any{
				"auth_mode":                  service.OpenAIAuthModeAgentIdentity,
				"agent_runtime_id":           "runtime-export",
				"agent_private_key":          privateKeyBase64,
				"task_id":                    "task-export",
				"chatgpt_account_id":         "account-export",
				"chatgpt_user_id":            "user-export",
				"email":                      "exported-agent@example.invalid",
				"plan_type":                  "team",
				"chatgpt_account_is_fedramp": true,
			},
			"extra": map[string]any{"custom_extra": "preserved"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, item)
	require.True(t, item.IsAgentIdentity)
	require.Equal(t, "Exported agent account", item.Name)
	require.Equal(t, "runtime-export", item.Credentials["agent_runtime_id"])
	require.Equal(t, privateKeyBase64, item.Credentials["agent_private_key"])
	require.Equal(t, "task-export", item.Credentials["task_id"])
	require.Equal(t, "account-export", item.Credentials["chatgpt_account_id"])
	require.Equal(t, "user-export", item.Credentials["chatgpt_user_id"])
	require.Equal(t, true, item.Credentials["chatgpt_account_is_fedramp"])
	require.Equal(t, "preserved", item.Extra["custom_extra"])
	require.Empty(t, item.WarningTexts)
}

func newCodexAgentIdentityPrivateKey(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(der)
}
