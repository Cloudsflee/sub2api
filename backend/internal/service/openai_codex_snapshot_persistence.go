package service

import (
	"context"
	"errors"
)

var errOpenAICodexSnapshotIdentityChanged = errors.New("openai codex snapshot identity changed")

func openAICodexSnapshotObservedAtFromExtra(extra map[string]any) (string, bool) {
	if extra == nil {
		return "", false
	}
	value, ok := extra[OpenAICodexSnapshotObservedAtExtraKey].(string)
	if !ok || len(value) != 20 {
		return "", false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	return value, true
}

// mergeOpenAICodexSnapshotExtra keeps a late response from making the
// request-local account object disagree with the monotonic database snapshot.
// Non-Codex probe fields in the same payload are still merged normally.
func mergeOpenAICodexSnapshotExtra(account *Account, updates map[string]any) {
	if account == nil || len(updates) == 0 {
		return
	}
	incoming, incomingOK := openAICodexSnapshotObservedAtFromExtra(updates)
	current, currentOK := openAICodexSnapshotObservedAtFromExtra(account.Extra)
	stale := incomingOK && currentOK && incoming <= current
	if account.Extra == nil {
		account.Extra = make(map[string]any, len(updates))
	}
	for key, value := range updates {
		if stale {
			if _, managed := openAICodexSnapshotManagedExtraKeys[key]; managed {
				continue
			}
		}
		account.Extra[key] = value
	}
}

// OpenAICodexSnapshotObservedAtExtraKey orders responses produced by the same
// account identity. It prevents an older asynchronous response from replacing
// a newer quota snapshot after both requests have completed.
const OpenAICodexSnapshotObservedAtExtraKey = "codex_usage_observed_at_unix_nano"

// openAICodexSnapshotCASRepository is implemented by the SQL repository. The
// optional interface keeps lightweight test repositories compatible while
// making production snapshot writes conditional on the identity that produced
// the upstream response.
type openAICodexSnapshotCASRepository interface {
	UpdateOpenAICodexSnapshot(
		ctx context.Context,
		id int64,
		account *Account,
		ordinaryUpdates map[string]any,
		managedUpdates map[string]any,
	) (bool, error)
}

func supportsOpenAICodexSnapshotCAS(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformOpenAI &&
		account.Type == AccountTypeOAuth &&
		account.QuotaDimensionOrDefault() == QuotaDimensionGlobal &&
		!account.IsShadow()
}

func cloneOpenAICodexSnapshotIdentity(account *Account) *Account {
	if account == nil {
		return nil
	}
	clone := &Account{
		ID:             account.ID,
		Platform:       account.Platform,
		Type:           account.Type,
		QuotaDimension: account.QuotaDimension,
		Credentials:    make(map[string]any, 3),
	}
	if account.ParentAccountID != nil {
		parentID := *account.ParentAccountID
		clone.ParentAccountID = &parentID
	}
	for _, key := range []string{"chatgpt_account_id", "organization_id", "chatgpt_user_id"} {
		if value, ok := account.Credentials[key]; ok {
			clone.Credentials[key] = value
		}
	}
	return clone
}

func persistOpenAICodexSnapshotForAccount(
	ctx context.Context,
	repo AccountRepository,
	account *Account,
	updates map[string]any,
) error {
	if len(updates) == 0 {
		return nil
	}
	if repo == nil {
		return errors.New("account repository unavailable")
	}
	if account == nil || account.ID <= 0 {
		return errors.New("invalid OpenAI snapshot account")
	}
	casRepo, useCAS := repo.(openAICodexSnapshotCASRepository)
	if !supportsOpenAICodexSnapshotCAS(account) || !useCAS {
		return repo.UpdateExtra(ctx, account.ID, updates)
	}

	// Compact capability probes and Codex quota observations can finish from
	// the same request, but only the latter participates in the monotonic
	// observation guard. Pass both parts through one repository CAS so identity
	// cannot change between writes and cache propagation cannot consume the
	// context budget before the managed snapshot is stored.
	managed, ordinary := splitOpenAICodexSnapshotUpdates(updates)
	applied, err := casRepo.UpdateOpenAICodexSnapshot(ctx, account.ID, account, ordinary, managed)
	if err != nil {
		return err
	}
	if !applied {
		return errOpenAICodexSnapshotIdentityChanged
	}
	return nil
}

func splitOpenAICodexSnapshotUpdates(updates map[string]any) (managed, ordinary map[string]any) {
	for key, value := range updates {
		if _, ok := openAICodexSnapshotManagedExtraKeys[key]; ok {
			if managed == nil {
				managed = make(map[string]any)
			}
			managed[key] = value
			continue
		}
		if ordinary == nil {
			ordinary = make(map[string]any)
		}
		ordinary[key] = value
	}
	return managed, ordinary
}
