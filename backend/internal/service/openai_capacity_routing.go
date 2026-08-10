package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const (
	openAIModelCapacityFailureWindow = time.Minute
	openAIModelCapacityShortCooldown = time.Minute
	openAIModelCapacityLongCooldown  = 5 * time.Minute

	openAIAccountDistributionDefaultMax = 4096
)

const openAIModelCapacityFailureReason = GatewayFailureReason("openai_model_capacity")

// isOpenAIModelCapacityError deliberately matches the explicit OpenAI capacity
// and server-overload responses only. Other transient 400/503 responses keep
// their existing retry and cooldown behavior.
func isOpenAIModelCapacityError(message string, payload []byte) bool {
	match := func(value string) bool {
		value = strings.ToLower(strings.TrimSpace(value))
		return strings.Contains(value, "selected model is at capacity") ||
			strings.Contains(value, "our servers are currently overloaded. please try again later.")
	}
	if match(message) {
		return true
	}
	if len(payload) == 0 {
		return false
	}
	for _, path := range []string{
		"error.message",
		"response.error.message",
		"message",
	} {
		if match(gjson.GetBytes(payload, path).String()) {
			return true
		}
	}
	return match(string(payload))
}

func (e *UpstreamFailoverError) IsOpenAIModelCapacityError() bool {
	return e != nil && e.Reason == openAIModelCapacityFailureReason
}

func newOpenAIModelCapacityState(maxEntries int) *openAIAccountModelTransientState {
	return newOpenAIAccountModelTransientStateWithPolicy(
		maxEntries,
		openAIModelCapacityFailureWindow,
		openAIModelCapacityShortCooldown,
		openAIModelCapacityLongCooldown,
	)
}

func (s *OpenAIGatewayService) getOpenAIModelCapacityState() *openAIAccountModelTransientState {
	if s == nil {
		return nil
	}
	s.openaiModelCapacityOnce.Do(func() {
		if s.openaiModelCapacity == nil {
			s.openaiModelCapacity = newOpenAIModelCapacityState(openAIModelTransientDefaultMax)
		}
	})
	return s.openaiModelCapacity
}

func (s *OpenAIGatewayService) recordOpenAIModelCapacityFailure(accountID int64, model string, now time.Time) {
	state := s.getOpenAIModelCapacityState()
	if state == nil {
		return
	}
	model = normalizeOpenAIAccountModelTransientModel(model)
	decision := state.recordFailure(accountID, model, now)
	if decision.FailureStreak == 0 {
		return
	}
	slog.Warn("openai_model_capacity_state",
		"account_id", accountID,
		"model", model,
		"failure_streak", decision.FailureStreak,
		"cooldown_ms", decision.Cooldown.Milliseconds(),
		"block_scope", "account_model",
	)
}

// A stream cannot be replayed after semantic output has reached the client.
// Still count a terminal capacity failure so later requests avoid the same
// account/model pair according to the normal cooldown policy.
func (s *OpenAIGatewayService) recordOpenAIModelCapacityFailureAfterOutput(
	account *Account,
	requestedModel string,
	payload []byte,
	message string,
	outputStarted bool,
) {
	if !outputStarted || account == nil || !isOpenAIModelCapacityError(message, payload) {
		return
	}
	model := canonicalOpenAIAccountSchedulingModel(account, requestedModel)
	s.recordOpenAIModelCapacityFailure(account.ID, model, time.Now())
}

func (s *OpenAIGatewayService) clearOpenAIModelCapacityState(accountID int64, model string) {
	state := s.getOpenAIModelCapacityState()
	if state != nil {
		state.recordSuccess(accountID, model)
	}
}

func (s *OpenAIGatewayService) isOpenAIModelCapacityBlocked(account *Account, requestedModel string) bool {
	if s == nil || account == nil {
		return false
	}
	state := s.getOpenAIModelCapacityState()
	if state == nil {
		return false
	}
	model := canonicalOpenAIAccountSchedulingModel(account, requestedModel)
	return state.isBlocked(account.ID, model, time.Now())
}

type openAIAccountDistributionContextKey struct{}

func withOpenAIAccountDistribution(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAIAccountDistributionContextKey{}, true)
}

func openAIAccountDistributionEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(openAIAccountDistributionContextKey{}).(bool)
	return enabled
}

type openAIAccountDistributionKey struct {
	groupID                 int64
	ungrouped               bool
	platform                string
	model                   string
	requiredCapability      OpenAIEndpointCapability
	requiredImageCapability OpenAIImagesCapability
	requireCompact          bool
}

func newOpenAIAccountDistributionKey(
	groupID *int64,
	platform string,
	model string,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
) openAIAccountDistributionKey {
	key := openAIAccountDistributionKey{
		ungrouped:               groupID == nil,
		platform:                normalizeOpenAICompatiblePlatform(platform),
		model:                   normalizeOpenAIAccountModelTransientModel(model),
		requiredCapability:      requiredCapability,
		requiredImageCapability: requiredImageCapability,
		requireCompact:          requireCompact,
	}
	if groupID != nil {
		key.groupID = *groupID
	}
	return key
}

type openAIAccountDistributionEntry struct {
	lastAccountID int64
	lastTouched   time.Time
}

type openAIAccountDistributionState struct {
	mu         sync.Mutex
	entries    map[openAIAccountDistributionKey]openAIAccountDistributionEntry
	maxEntries int
}

func newOpenAIAccountDistributionState(maxEntries int) *openAIAccountDistributionState {
	if maxEntries <= 0 {
		maxEntries = openAIAccountDistributionDefaultMax
	}
	return &openAIAccountDistributionState{
		entries:    make(map[openAIAccountDistributionKey]openAIAccountDistributionEntry),
		maxEntries: maxEntries,
	}
}

// preferred returns the first candidate unless it was also selected for the
// previous independent request and another eligible candidate exists.
func (s *openAIAccountDistributionState) preferred(key openAIAccountDistributionKey, candidates []int64) int64 {
	if len(candidates) == 0 {
		return 0
	}
	if s == nil || len(candidates) == 1 {
		return candidates[0]
	}
	s.mu.Lock()
	entry := s.entries[key]
	s.mu.Unlock()
	if candidates[0] == entry.lastAccountID {
		return candidates[1]
	}
	return candidates[0]
}

func (s *openAIAccountDistributionState) record(key openAIAccountDistributionKey, accountID int64, now time.Time) {
	if s == nil || accountID <= 0 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; !exists && len(s.entries) >= s.maxEntries {
		var oldestKey openAIAccountDistributionKey
		var oldestTime time.Time
		found := false
		for candidateKey, entry := range s.entries {
			if !found || entry.lastTouched.Before(oldestTime) {
				oldestKey = candidateKey
				oldestTime = entry.lastTouched
				found = true
			}
		}
		if found {
			delete(s.entries, oldestKey)
		}
	}
	s.entries[key] = openAIAccountDistributionEntry{lastAccountID: accountID, lastTouched: now}
}

func (s *OpenAIGatewayService) getOpenAIAccountDistributionState() *openAIAccountDistributionState {
	if s == nil {
		return nil
	}
	s.openaiAccountDistributionOnce.Do(func() {
		if s.openaiAccountDistribution == nil {
			s.openaiAccountDistribution = newOpenAIAccountDistributionState(openAIAccountDistributionDefaultMax)
		}
	})
	return s.openaiAccountDistribution
}

func (s *OpenAIGatewayService) preferredOpenAIAccountForIndependentRequest(
	key openAIAccountDistributionKey,
	candidates []int64,
) int64 {
	state := s.getOpenAIAccountDistributionState()
	if state == nil {
		if len(candidates) == 0 {
			return 0
		}
		return candidates[0]
	}
	return state.preferred(key, candidates)
}

func (s *OpenAIGatewayService) recordOpenAIAccountForIndependentRequest(
	key openAIAccountDistributionKey,
	accountID int64,
) {
	state := s.getOpenAIAccountDistributionState()
	if state != nil {
		state.record(key, accountID, time.Now())
	}
}

func (s *OpenAIGatewayService) shouldDistributeOpenAIAccountRequest(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
) bool {
	if s == nil || strings.TrimSpace(previousResponseID) != "" {
		return false
	}
	if strings.TrimSpace(sessionHash) == "" {
		return true
	}
	// A session hash is already a request-level affinity anchor. When the
	// backing cache is unavailable (for example in a lightweight deployment or
	// during a transient cache outage), keep the normal candidate order instead
	// of treating every retry as a fresh independent request. This preserves
	// same-account pool retries; with a healthy cache, a cache miss still allows
	// first-request distribution below.
	if s.cache == nil {
		return false
	}
	accountID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash)
	return err != nil || accountID <= 0
}

func moveOpenAIAccountToFront[T any](items []T, accountID func(T) int64, preferredID int64) {
	if len(items) < 2 || preferredID <= 0 || accountID(items[0]) == preferredID {
		return
	}
	for i := 1; i < len(items); i++ {
		if accountID(items[i]) == preferredID {
			items[0], items[i] = items[i], items[0]
			return
		}
	}
}

func (s *OpenAIGatewayService) distributeOpenAIAccounts(
	ctx context.Context,
	groupID *int64,
	platform string,
	model string,
	requiredCapability OpenAIEndpointCapability,
	requireCompact bool,
	accounts []*Account,
) {
	if !openAIAccountDistributionEnabled(ctx) || len(accounts) < 2 {
		return
	}
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account != nil {
			ids = append(ids, account.ID)
		}
	}
	key := newOpenAIAccountDistributionKey(groupID, platform, model, requiredCapability, "", requireCompact)
	preferredID := s.preferredOpenAIAccountForIndependentRequest(key, ids)
	moveOpenAIAccountToFront(accounts, func(account *Account) int64 {
		if account == nil {
			return 0
		}
		return account.ID
	}, preferredID)
}

func (s *OpenAIGatewayService) distributeOpenAIAccountsWithLoad(
	ctx context.Context,
	groupID *int64,
	platform string,
	model string,
	requiredCapability OpenAIEndpointCapability,
	requireCompact bool,
	accounts []accountWithLoad,
) {
	if !openAIAccountDistributionEnabled(ctx) || len(accounts) < 2 {
		return
	}
	ids := make([]int64, 0, len(accounts))
	for _, item := range accounts {
		if item.account != nil {
			ids = append(ids, item.account.ID)
		}
	}
	key := newOpenAIAccountDistributionKey(groupID, platform, model, requiredCapability, "", requireCompact)
	preferredID := s.preferredOpenAIAccountForIndependentRequest(key, ids)
	moveOpenAIAccountToFront(accounts, func(item accountWithLoad) int64 {
		if item.account == nil {
			return 0
		}
		return item.account.ID
	}, preferredID)
}

func (s *defaultOpenAIAccountScheduler) distributeSelectionOrder(
	req OpenAIAccountScheduleRequest,
	order []openAIAccountCandidateScore,
) []openAIAccountCandidateScore {
	if s == nil || s.service == nil || !req.DistributeIndependent || len(order) < 2 {
		return order
	}
	ids := make([]int64, 0, len(order))
	for _, candidate := range order {
		if candidate.account != nil {
			ids = append(ids, candidate.account.ID)
		}
	}
	key := newOpenAIAccountDistributionKey(
		req.GroupID,
		req.Platform,
		req.RequestedModel,
		req.RequiredCapability,
		req.RequiredImageCapability,
		req.RequireCompact,
	)
	preferredID := s.service.preferredOpenAIAccountForIndependentRequest(key, ids)
	moveOpenAIAccountToFront(order, func(candidate openAIAccountCandidateScore) int64 {
		if candidate.account == nil {
			return 0
		}
		return candidate.account.ID
	}, preferredID)
	return order
}
