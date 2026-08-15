package service

// normalizeOpenAICodexDefaults applies the account defaults for OpenAI OAuth
// accounts. Missing values are filled without replacing explicit values, so an
// operator can still explicitly disable a setting.
func normalizeOpenAICodexDefaults(platform, accountType string, extra map[string]any) map[string]any {
	if platform != PlatformOpenAI || accountType != AccountTypeOAuth {
		return extra
	}
	if extra == nil {
		extra = make(map[string]any, 3)
	}
	if _, ok := extra["codex_cli_only"]; !ok {
		extra["codex_cli_only"] = true
	}
	if _, ok := extra["codex_cli_only_allow_app_server"]; !ok {
		extra["codex_cli_only_allow_app_server"] = true
	}
	if _, ok := extra[codexFingerprintModeExtraKey]; !ok {
		extra[codexFingerprintModeExtraKey] = string(codexFingerprintSession)
	}
	return extra
}
