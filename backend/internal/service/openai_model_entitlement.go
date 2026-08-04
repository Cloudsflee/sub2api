package service

import "strings"

// This file is an intentionally isolated Codex entitlement workaround. The
// scheduler hooks call one function below; when upstream entitlement behavior
// changes, remove this file and those openAICodexAccountCanServeRequestedModel
// calls without changing the generic scheduler or model alias code.

const openAICodexPlanGatedSolModel = "gpt-5.6-sol"

// Keep model-specific exceptions in one explicit set so adding or removing a
// gated model does not spread another special case through scheduling code.
var openAICodexPlanGatedModels = map[string]struct{}{
	openAICodexPlanGatedSolModel: {},
}

func isOpenAICodexPlanGatedModel(model string) bool {
	_, ok := openAICodexPlanGatedModels[normalizeKnownOpenAICodexModel(model)]
	return ok
}

// openAICodexAccountCanServeRequestedModel applies the isolated account-level
// entitlement check that model_mapping cannot express. API-key accounts and
// personal-access-token OAuth accounts are not ChatGPT subscription accounts,
// so the Codex plan gate does not apply to them. A regular ChatGPT OAuth
// account with an explicitly free or abnormal plan is rejected for gated
// models. An empty plan type remains unknown and is handled by the existing
// upstream probe/cooldown path.
func openAICodexAccountCanServeRequestedModel(account *Account, requestedModel string) bool {
	if account == nil || !account.IsOpenAI() {
		return true
	}

	models := []string{requestedModel}
	if mapped, matched := account.ResolveMappedModel(requestedModel); matched {
		models = append(models, mapped)
	}
	for _, model := range models {
		if !isOpenAICodexPlanGatedModel(model) {
			continue
		}
		if !account.IsOpenAIOAuth() || account.IsOpenAIPersonalAccessToken() {
			return true
		}
		switch strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type"))) {
		case "free", "abnormal":
			return false
		default:
			return true
		}
	}
	return true
}
