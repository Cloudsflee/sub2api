package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeOpenAICodexDefaultsFillsMissingValues(t *testing.T) {
	extra := normalizeOpenAICodexDefaults(PlatformOpenAI, AccountTypeOAuth, nil)

	require.Equal(t, true, extra["codex_cli_only"])
	require.Equal(t, true, extra["codex_cli_only_allow_app_server"])
	require.Equal(t, string(codexFingerprintSession), extra[codexFingerprintModeExtraKey])
}

func TestNormalizeOpenAICodexDefaultsPreservesExplicitOptOuts(t *testing.T) {
	extra := map[string]any{
		"codex_cli_only":                  false,
		"codex_cli_only_allow_app_server": false,
		codexFingerprintModeExtraKey:       string(codexFingerprintOff),
		"custom_setting":                  "keep",
	}

	normalized := normalizeOpenAICodexDefaults(PlatformOpenAI, AccountTypeOAuth, extra)

	require.Equal(t, false, normalized["codex_cli_only"])
	require.Equal(t, false, normalized["codex_cli_only_allow_app_server"])
	require.Equal(t, string(codexFingerprintOff), normalized[codexFingerprintModeExtraKey])
	require.Equal(t, "keep", normalized["custom_setting"])
}

func TestNormalizeOpenAICodexDefaultsIgnoresOtherAccountKinds(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		accountType string
	}{
		{name: "OpenAI API key", platform: PlatformOpenAI, accountType: AccountTypeAPIKey},
		{name: "OpenAI setup token", platform: PlatformOpenAI, accountType: AccountTypeSetupToken},
		{name: "Anthropic OAuth", platform: PlatformAnthropic, accountType: AccountTypeOAuth},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Nil(t, normalizeOpenAICodexDefaults(tt.platform, tt.accountType, nil))
		})
	}
}
