package service

import (
	"context"
	"fmt"
	"strings"
)

const openAIAuthenticatedAccountSnapshotMaxAttempts = 3

// acquireOpenAIAuthenticatedAccountSnapshot returns an access token together
// with the durable account row that owns that exact token. Token acquisition
// may refresh credentials or hit a stale cache entry, so callers must not pair
// its result with an account snapshot loaded before GetAccessToken.
func acquireOpenAIAuthenticatedAccountSnapshot(
	ctx context.Context,
	repo AccountRepository,
	provider *OpenAITokenProvider,
	account *Account,
) (string, *Account, error) {
	if repo == nil {
		return "", nil, fmt.Errorf("account repository unavailable")
	}
	if provider == nil {
		return "", nil, fmt.Errorf("token provider unavailable")
	}
	if account == nil {
		return "", nil, fmt.Errorf("account unavailable")
	}
	if !account.IsOpenAIOAuth() || account.IsOpenAIAgentIdentity() || account.IsCredentialShadow() {
		return "", nil, fmt.Errorf("account is not a directly authenticated OpenAI OAuth account")
	}

	candidate := account
	for attempt := 0; attempt < openAIAuthenticatedAccountSnapshotMaxAttempts; attempt++ {
		accessToken, err := provider.GetAccessToken(ctx, candidate)
		if err != nil {
			return "", nil, err
		}
		accessToken = strings.TrimSpace(accessToken)
		if accessToken == "" {
			return "", nil, fmt.Errorf("access token unavailable")
		}

		durable, err := repo.GetByID(ctx, candidate.ID)
		if err != nil {
			return "", nil, fmt.Errorf("reload account after token acquisition: %w", err)
		}
		if durable == nil || durable.ID != candidate.ID {
			return "", nil, fmt.Errorf("account unavailable after token acquisition")
		}
		if !durable.IsOpenAIOAuth() || durable.IsOpenAIAgentIdentity() || durable.IsCredentialShadow() {
			return "", nil, fmt.Errorf("account authentication mode changed during token acquisition")
		}
		durableToken := strings.TrimSpace(durable.GetOpenAIAccessToken())
		if durableToken == accessToken ||
			(durableToken == "" && strings.TrimSpace(candidate.GetOpenAIAccessToken()) == "" && openAIAuthSnapshotIdentityMatches(candidate, durable)) {
			return accessToken, snapshotOAuthRefreshAccount(durable), nil
		}

		// A cache hit can race a refresh or reauthorization. Remove only this
		// account's cached token before retrying from the newest durable row.
		if provider.tokenCache != nil {
			if err := provider.tokenCache.DeleteAccessToken(ctx, OpenAITokenCacheKey(durable)); err != nil {
				return "", nil, fmt.Errorf("discard stale access token cache: %w", err)
			}
		}
		candidate = durable
	}

	return "", nil, fmt.Errorf("account credentials kept changing during token acquisition")
}

// openAIAuthSnapshotIdentityMatches permits a cache-only token when the
// durable row intentionally omits access_token, while still rejecting a
// concurrent reauthorization that changed the account's ownership identity.
// Expiry and access-token fields are deliberately excluded because they are
// expected to change during refresh.
func openAIAuthSnapshotIdentityMatches(left, right *Account) bool {
	if left == nil || right == nil || left.ID != right.ID {
		return false
	}
	for _, key := range []string{
		"chatgpt_account_id",
		"organization_id",
		"chatgpt_user_id",
		"refresh_token",
		"auth_mode",
	} {
		if strings.TrimSpace(left.GetCredential(key)) != strings.TrimSpace(right.GetCredential(key)) {
			return false
		}
	}
	return true
}
