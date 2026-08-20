package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/gin-gonic/gin"
)

const (
	publicUpstreamImportMaxNameRunes = 100
	publicUpstreamImportMaxURLBytes  = 2048
	publicUpstreamImportMaxKeyBytes  = 8192
)

// PublicAccountImportUpstreamRequest is the authenticated URL + API key
// importer payload. The API key is accepted only on this write endpoint and is
// never copied into the public result.
type PublicAccountImportUpstreamRequest struct {
	Name     string  `json:"name"`
	BaseURL  string  `json:"base_url"`
	APIKey   string  `json:"api_key"`
	GroupIDs []int64 `json:"group_ids"`
}

// PublicUpstreamAccountImportRequest is kept as a descriptive alias for
// callers that use the operation name rather than the page name.
type PublicUpstreamAccountImportRequest = PublicAccountImportUpstreamRequest

type normalizedPublicUpstreamImport struct {
	name     string
	baseURL  string
	apiKey   string
	groupIDs []int64
	priority int
}

// ImportPublicUpstreamAccount handles POST /api/v1/user/account-import/upstream.
// It intentionally lives beside the existing public importer so both paths
// share the public-group contract and account result shape, while the route is
// mounted only below the authenticated user group.
func (h *AccountHandler) ImportPublicUpstreamAccount(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	idempotencyKey, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if idempotencyKey == "" {
		response.ErrorFrom(c, service.ErrIdempotencyKeyRequired)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, publicAccountImportMaxRequestBytes)
	var req PublicAccountImportUpstreamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			response.Error(c, http.StatusRequestEntityTooLarge, "Import request is too large")
			return
		}
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	normalized, err := h.normalizePublicUpstreamImport(c.Request.Context(), req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	payload := PublicAccountImportUpstreamRequest{
		Name:     normalized.name,
		BaseURL:  normalized.baseURL,
		APIKey:   normalized.apiKey,
		GroupIDs: append([]int64(nil), normalized.groupIDs...),
	}
	executeUserAccountImportIdempotentJSON(c, "user.account_import.upstream", payload, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		result, importErr := h.importPublicUpstreamAccount(ctx, normalized)
		if importErr != nil {
			return nil, importErr
		}
		middleware2.SetAuditExtra(c, map[string]any{
			"result":          publicAccountImportResultActionSummary(result),
			"total_items":     result.Total,
			"requested_count": result.Total,
		})
		return result, nil
	})
}

// PublicImportUpstreamAccount and ImportUpstreamAccount are compatibility
// method names for integrations that mirror the JSON importer naming.
func (h *AccountHandler) PublicImportUpstreamAccount(c *gin.Context) {
	h.ImportPublicUpstreamAccount(c)
}

func (h *AccountHandler) ImportUpstreamAccount(c *gin.Context) {
	h.ImportPublicUpstreamAccount(c)
}

func (h *AccountHandler) normalizePublicUpstreamImport(ctx context.Context, req PublicAccountImportUpstreamRequest) (normalizedPublicUpstreamImport, error) {
	baseURL, parsed, err := normalizePublicAccountImportUpstreamURLWithConfig(req.BaseURL, h.urlPolicyConfig)
	if err != nil {
		return normalizedPublicUpstreamImport{}, err
	}
	if len([]byte(strings.TrimSpace(req.BaseURL))) > publicUpstreamImportMaxURLBytes {
		return normalizedPublicUpstreamImport{}, fmt.Errorf("base_url must be at most %d bytes", publicUpstreamImportMaxURLBytes)
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		return normalizedPublicUpstreamImport{}, errors.New("api_key is required")
	}
	if len([]byte(apiKey)) > publicUpstreamImportMaxKeyBytes {
		return normalizedPublicUpstreamImport{}, fmt.Errorf("api_key must be at most %d bytes", publicUpstreamImportMaxKeyBytes)
	}
	if publicUpstreamAPIKeyContainsControlCharacter(apiKey) {
		return normalizedPublicUpstreamImport{}, errors.New("api_key contains invalid control characters")
	}

	name := strings.TrimSpace(req.Name)
	if len([]rune(name)) > publicUpstreamImportMaxNameRunes {
		return normalizedPublicUpstreamImport{}, fmt.Errorf("name must be at most %d characters", publicUpstreamImportMaxNameRunes)
	}
	if name == "" {
		name = strings.TrimSpace(parsed.Hostname())
	}
	if name == "" {
		return normalizedPublicUpstreamImport{}, errors.New("name or a URL hostname is required")
	}

	groupIDs, priority, err := h.resolvePublicAccountImportGroups(ctx, req.GroupIDs)
	if err != nil {
		return normalizedPublicUpstreamImport{}, err
	}
	return normalizedPublicUpstreamImport{
		name:     name,
		baseURL:  baseURL,
		apiKey:   apiKey,
		groupIDs: groupIDs,
		priority: priority,
	}, nil
}

func publicUpstreamAPIKeyContainsControlCharacter(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] == 0x7f {
			return true
		}
	}
	return false
}

func normalizePublicAccountImportUpstreamURLWithConfig(raw string, cfg *config.Config) (string, *url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil, errors.New("base_url is required")
	}
	if len([]byte(trimmed)) > publicUpstreamImportMaxURLBytes {
		return "", nil, fmt.Errorf("base_url must be at most %d bytes", publicUpstreamImportMaxURLBytes)
	}
	// url.Parse treats an empty fragment (`https://host/#`) as valid and drops
	// the marker. Reject a literal marker as well as a non-empty fragment.
	if strings.ContainsRune(trimmed, '#') {
		return "", nil, errors.New("base_url must not contain a fragment")
	}
	parsedInput, err := url.Parse(trimmed)
	if err != nil || parsedInput.Scheme == "" || parsedInput.Host == "" {
		return "", nil, errors.New("base_url must be an absolute HTTP URL")
	}
	if parsedInput.User != nil {
		return "", nil, errors.New("base_url must not contain URL userinfo")
	}

	var validationErr error
	if cfg == nil {
		_, validationErr = urlvalidator.ValidateHTTPSURL(trimmed, urlvalidator.ValidationOptions{AllowPrivate: false})
	} else if cfg.Security.URLAllowlist.Enabled {
		_, validationErr = urlvalidator.ValidateHTTPSURL(trimmed, urlvalidator.ValidationOptions{
			AllowedHosts:     cfg.Security.URLAllowlist.UpstreamHosts,
			RequireAllowlist: true,
			AllowPrivate:     cfg.Security.URLAllowlist.AllowPrivateHosts,
		})
	} else {
		_, validationErr = urlvalidator.ValidateURLFormat(trimmed, cfg.Security.URLAllowlist.AllowInsecureHTTP)
	}
	if validationErr != nil {
		return "", nil, fmt.Errorf("invalid base_url: %w", validationErr)
	}

	parsed := parsedInput
	canonicalizePublicAccountImportURL(parsed)
	canonical := parsed.String()
	if len([]byte(canonical)) > publicUpstreamImportMaxURLBytes {
		return "", nil, fmt.Errorf("base_url must be at most %d bytes", publicUpstreamImportMaxURLBytes)
	}
	return canonical, parsed, nil
}

func canonicalizePublicAccountImportURL(parsed *url.URL) {
	if parsed == nil {
		return
	}
	parsed.Scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	parsed.Host = host
	if parsed.RawPath == "" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	} else {
		// Trim only literal path separators. An escaped slash (%2F) can be part
		// of an upstream route prefix and must not collapse into a separator.
		rawPath := strings.TrimRight(parsed.RawPath, "/")
		if decodedPath, err := url.PathUnescape(rawPath); err == nil {
			parsed.Path = decodedPath
			parsed.RawPath = rawPath
		}
	}
	parsed.Fragment = ""
}

func (h *AccountHandler) importPublicUpstreamAccount(ctx context.Context, input normalizedPublicUpstreamImport) (PublicAccountImportResult, error) {
	result := PublicAccountImportResult{Total: 1, Items: make([]PublicAccountImportItem, 0, 1)}
	if h == nil || h.adminService == nil {
		return result, errors.New("account service is not configured")
	}

	accounts, err := h.adminService.ListAccountsForSchedulerScoreFilter(ctx, service.PlatformOpenAI, service.AccountTypeAPIKey, "", "", 0, "")
	if err != nil {
		return result, errors.New("failed to look up existing accounts")
	}
	existing := findPublicUpstreamImportAccount(accounts, input.baseURL, input.apiKey)
	if existing != nil {
		mergedGroupIDs, changed := mergePublicUpstreamImportGroupIDs(existing, input.groupIDs)
		if !changed {
			result.Skipped = 1
			result.Items = append(result.Items, PublicAccountImportItem{
				Index: 1, Name: existing.Name, Action: "skipped",
				Message: "账号已存在且已绑定所选分组",
			})
			return result, nil
		}
		updated, updateErr := h.adminService.UpdateAccount(ctx, existing.ID, &service.UpdateAccountInput{GroupIDs: &mergedGroupIDs})
		if updateErr != nil {
			result.Failed = 1
			result.Items = append(result.Items, PublicAccountImportItem{Index: 1, Name: existing.Name, Action: "failed", Message: "账号分组绑定失败"})
			return result, nil
		}
		result.Updated = 1
		name := existing.Name
		if updated != nil && strings.TrimSpace(updated.Name) != "" {
			name = updated.Name
		}
		result.Items = append(result.Items, PublicAccountImportItem{
			Index: 1, Name: name, Action: "updated", Message: "账号已存在，已追加绑定所选分组",
		})
		return result, nil
	}

	probeEnabled := true
	created, createErr := h.adminService.CreateAccount(ctx, &service.CreateAccountInput{
		Name:                 input.name,
		Platform:             service.PlatformOpenAI,
		Type:                 service.AccountTypeAPIKey,
		Credentials:          map[string]any{"base_url": input.baseURL, "api_key": input.apiKey},
		Extra:                map[string]any{"import_source": "upstream_url_key"},
		Concurrency:          publicAccountImportDefaultConcurrency,
		Priority:             input.priority,
		GroupIDs:             append([]int64(nil), input.groupIDs...),
		ProbeEnabled:         &probeEnabled,
		SkipDefaultGroupBind: true,
	})
	if createErr != nil {
		result.Failed = 1
		result.Items = append(result.Items, PublicAccountImportItem{Index: 1, Name: input.name, Action: "failed", Message: "账号创建失败"})
		return result, nil
	}
	// Match the normal admin create flow: capability detection is asynchronous
	// and does not turn this endpoint into a synchronous connectivity test.
	h.scheduleOpenAIResponsesProbe(created)
	result.Created = 1
	result.Items = append(result.Items, PublicAccountImportItem{Index: 1, Name: input.name, Action: "created"})
	return result, nil
}

func findPublicUpstreamImportAccount(accounts []service.Account, normalizedURL, apiKey string) *service.Account {
	var match *service.Account
	for index := range accounts {
		account := &accounts[index]
		if account.Platform != service.PlatformOpenAI || account.Type != service.AccountTypeAPIKey {
			continue
		}
		storedKey, ok := account.Credentials["api_key"].(string)
		if !ok || strings.TrimSpace(storedKey) != apiKey {
			continue
		}
		storedURL, ok := account.Credentials["base_url"].(string)
		if !ok {
			continue
		}
		canonicalStored, _, err := canonicalPublicAccountImportURLForMatch(storedURL)
		if err != nil || canonicalStored != normalizedURL {
			continue
		}
		if match == nil || (account.ID > 0 && account.ID < match.ID) {
			copy := *account
			match = &copy
		}
	}
	return match
}

func canonicalPublicAccountImportURLForMatch(raw string) (string, *url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsRune(trimmed, '#') {
		return "", nil, errors.New("invalid stored base_url")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", nil, errors.New("invalid stored base_url")
	}
	canonicalizePublicAccountImportURL(parsed)
	return parsed.String(), parsed, nil
}

func mergePublicUpstreamImportGroupIDs(account *service.Account, requested []int64) ([]int64, bool) {
	existing := make([]int64, 0)
	if account != nil {
		existing = append(existing, account.GroupIDs...)
		for _, binding := range account.AccountGroups {
			existing = append(existing, binding.GroupID)
		}
	}
	merged := make([]int64, 0, len(existing)+len(requested))
	seen := make(map[int64]struct{}, len(existing)+len(requested))
	for _, id := range existing {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		merged = append(merged, id)
	}
	changed := false
	for _, id := range requested {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		merged = append(merged, id)
		changed = true
	}
	return merged, changed
}

func publicAccountImportResultActionSummary(result PublicAccountImportResult) string {
	switch {
	case result.Created > 0:
		return "created"
	case result.Updated > 0:
		return "updated"
	case result.Skipped > 0:
		return "skipped"
	case result.Failed > 0:
		return "failed"
	default:
		return "none"
	}
}

// executeUserAccountImportIdempotentJSON is the user-scope counterpart of the
// admin importer helper. It keeps replay records isolated per authenticated
// user while retaining the coordinator's fail-closed semantics.
func executeUserAccountImportIdempotentJSON(c *gin.Context, scope string, payload any, ttl time.Duration, execute func(context.Context) (any, error)) {
	coordinator := service.DefaultIdempotencyCoordinator()
	if coordinator == nil {
		data, err := execute(c.Request.Context())
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, data)
		return
	}

	actorScope := "user:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		actorScope = "user:" + strconv.FormatInt(subject.UserID, 10)
	}
	result, err := coordinator.Execute(c.Request.Context(), service.IdempotencyExecuteOptions{
		Scope:          scope,
		ActorScope:     actorScope,
		Method:         c.Request.Method,
		Route:          c.FullPath(),
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		Payload:        payload,
		RequireKey:     true,
		TTL:            ttl,
	}, execute)
	if err != nil {
		if infraerrors.Code(err) == infraerrors.Code(service.ErrIdempotencyStoreUnavail) {
			service.RecordIdempotencyStoreUnavailable(c.FullPath(), scope, "handler_fail_close")
			logger.LegacyPrintf("handler.idempotency", "[Idempotency] store unavailable: method=%s route=%s scope=%s strategy=fail_close", c.Request.Method, c.FullPath(), scope)
		}
		if retryAfter := service.RetryAfterSecondsFromError(err); retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		response.ErrorFrom(c, err)
		return
	}
	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, result.Data)
}
