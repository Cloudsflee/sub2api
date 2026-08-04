package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAICodexPlanGatedModel(t *testing.T) {
	tests := map[string]bool{
		"gpt-5.6-sol":        true,
		"gpt-5.6":            true,
		"openai/gpt-5.6-max": true,
		"gpt-5.6-terra":      false,
		"gpt-5.6-luna":       false,
		"gpt-5.5":            false,
		"custom-sol-alias":   false,
	}
	for model, want := range tests {
		t.Run(model, func(t *testing.T) {
			require.Equal(t, want, isOpenAICodexPlanGatedModel(model))
		})
	}
}

func TestOpenAICodexAccountCanServeRequestedModel(t *testing.T) {
	tests := []struct {
		name        string
		account     *Account
		model       string
		wantAllowed bool
	}{
		{
			name:        "free ChatGPT OAuth is rejected for Sol",
			account:     &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "free"}},
			model:       "gpt-5.6-sol",
			wantAllowed: false,
		},
		{
			name:        "unknown ChatGPT OAuth remains eligible for Sol",
			account:     &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}},
			model:       "gpt-5.6",
			wantAllowed: true,
		},
		{
			name:        "paid ChatGPT OAuth is allowed for Sol",
			account:     &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "plus"}},
			model:       "gpt-5.6-sol",
			wantAllowed: true,
		},
		{
			name:        "free OAuth remains allowed for Terra",
			account:     &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "free"}},
			model:       "gpt-5.6-terra",
			wantAllowed: true,
		},
		{
			name:        "API key is not subject to ChatGPT plan gate",
			account:     &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			model:       "gpt-5.6-sol",
			wantAllowed: true,
		},
		{
			name: "mapped Sol alias is gated",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Credentials: map[string]any{
					"plan_type":     "free",
					"model_mapping": map[string]any{"sol-alias": "gpt-5.6-sol"},
				},
			},
			model:       "sol-alias",
			wantAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantAllowed, openAICodexAccountCanServeRequestedModel(tt.account, tt.model))
		})
	}
}
