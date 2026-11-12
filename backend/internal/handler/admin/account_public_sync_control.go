package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// The control file shares the product-cache mutex. Reads never claim, extend,
// expire or save jobs. No in-memory control is changed before a durable rename.
type PublicAccountImportSyncControl struct {
	Version            int               `json:"version"`
	Revision           uint64            `json:"revision"`
	IntervalMultiplier int               `json:"interval_multiplier"`
	AutomaticPaused    bool              `json:"automatic_paused"`
	QuarantinedShops   map[string]string `json:"quarantined_shops"`
	UpdatedAt          string            `json:"updated_at,omitempty"`
	ControllerID       string            `json:"controller_id,omitempty"`
	ControllerUntil    string            `json:"controller_until,omitempty"`
}

type PublicAccountImportSyncControlRequest struct {
	ExpectedRevision   *uint64            `json:"expected_revision"`
	IntervalMultiplier *int               `json:"interval_multiplier"`
	AutomaticPaused    *bool              `json:"automatic_paused"`
	QuarantinedShops   *map[string]string `json:"quarantined_shops"`
	ControllerID       string             `json:"controller_id"`
	LeaseSeconds       int                `json:"lease_seconds"`
}

type PublicAccountImportSyncShopStats struct {
	ShopID              string `json:"shop_id"`
	PlatformID          string `json:"platform_id"`
	TrustLevel          string `json:"trust_level"`
	AutomaticDue        bool   `json:"automatic_due"`
	AutomaticEligible   bool   `json:"automatic_eligible"`
	ManualPending       bool   `json:"manual_pending"`
	Active              bool   `json:"active"`
	NextAutomaticAt     string `json:"next_automatic_at"`
	WaitingSeconds      int    `json:"waiting_seconds"`
	SnapshotComplete    bool   `json:"snapshot_complete"`
	SnapshotUpdatedAt   string `json:"snapshot_updated_at"`
	SourceProductCount  int    `json:"source_product_count"`
	ProductCount        int    `json:"product_count"`
	FirstStartedAt      string `json:"first_started_at,omitempty"`
	FirstSyncedAt       string `json:"first_synced_at,omitempty"`
	LastAttempt         string `json:"last_attempt,omitempty"`
	SuccessCount        int    `json:"success_count"`
	FailureCount        int    `json:"failure_count"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	LastFailureKind     string `json:"last_failure_kind,omitempty"`
	LastFailureAt       string `json:"last_failure_at,omitempty"`
	Error               string `json:"error,omitempty"`
	RetryNotBeforeAt    string `json:"retry_not_before_at,omitempty"`
}

type PublicAccountImportSyncControlResponse struct {
	Control              PublicAccountImportSyncControl             `json:"control"`
	ObservedAt           string                                     `json:"observed_at"`
	AutomaticDue         int                                        `json:"automatic_due"`
	AutomaticEligible    int                                        `json:"automatic_eligible"`
	ActiveShops          int                                        `json:"active_shops"`
	OrdinaryCycleSeconds int                                        `json:"ordinary_cycle_seconds"`
	Shops                []PublicAccountImportSyncShopStats         `json:"shops"`
	Worker               PublicAccountImportProductSyncWorkerStatus `json:"worker"`
}

func defaultPublicAccountImportSyncControl() PublicAccountImportSyncControl {
	return PublicAccountImportSyncControl{Version: 1, IntervalMultiplier: 1, QuarantinedShops: map[string]string{}}
}

func publicAccountImportSyncControlPath() string {
	return filepath.Join(filepath.Dir(publicAccountImportProductsPath()), "public-account-import-sync-control.json")
}

func validPublicAccountImportSyncControl(control PublicAccountImportSyncControl) bool {
	if control.Version != 1 || (control.IntervalMultiplier != 1 && control.IntervalMultiplier != 2 && control.IntervalMultiplier != 4) || len(control.QuarantinedShops) > publicAccountImportShopMaxCount {
		return false
	}
	for id, reason := range control.QuarantinedShops {
		if strings.TrimSpace(id) == "" || len(id) > 100 || strings.TrimSpace(reason) == "" || len(reason) > 500 {
			return false
		}
	}
	return (control.ControllerID == "" && control.ControllerUntil == "") ||
		(publicAccountImportProductEgressIDPattern.MatchString(control.ControllerID) && !parsePublicAccountImportProductTimestamp(control.ControllerUntil).IsZero())
}

func loadPublicAccountImportSyncControlLocked() (PublicAccountImportSyncControl, error) {
	control := defaultPublicAccountImportSyncControl()
	data, err := os.ReadFile(publicAccountImportSyncControlPath())
	if errors.Is(err, os.ErrNotExist) {
		return control, nil
	}
	if err != nil {
		return control, err
	}
	if len(data) > 512<<10 {
		return control, errors.New("sync control is too large")
	}
	if err := json.Unmarshal(data, &control); err != nil {
		return control, err
	}
	if !validPublicAccountImportSyncControl(control) {
		return control, errors.New("invalid sync control")
	}
	if control.QuarantinedShops == nil {
		control.QuarantinedShops = map[string]string{}
	}
	return control, nil
}

func savePublicAccountImportSyncControlLocked(control PublicAccountImportSyncControl) error {
	return savePublicAccountImportSyncControlWithRename(control, os.Rename)
}

func savePublicAccountImportSyncControlWithRename(control PublicAccountImportSyncControl, rename func(string, string) error) error {
	data, err := json.MarshalIndent(control, "", "  ")
	if err != nil {
		return err
	}
	path := publicAccountImportSyncControlPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sync-control-*")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return rename(f.Name(), path)
}

func (h *AccountHandler) PutPublicAccountImportSyncControl(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	if !authorizePublicAccountImportProductWorker(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512<<10)
	var req PublicAccountImportSyncControlRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.ExpectedRevision == nil || decoder.Decode(new(any)) != io.EOF {
		response.BadRequest(c, "Invalid sync control request; expected_revision is required")
		return
	}
	if req.LeaseSeconds != 0 && (req.LeaseSeconds < 30 || req.LeaseSeconds > 180 || req.ControllerID == "") {
		response.BadRequest(c, "Controller leases must be between 30 and 180 seconds")
		return
	}
	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	control, err := loadPublicAccountImportSyncControlLocked()
	if err != nil {
		response.InternalError(c, "Failed to load sync control")
		return
	}
	now := time.Now().UTC()
	if control.Revision != *req.ExpectedRevision || (parsePublicAccountImportProductTimestamp(control.ControllerUntil).After(now) && control.ControllerID != req.ControllerID) {
		response.Error(c, http.StatusConflict, "Sync control changed or another controller holds the lease")
		return
	}
	if req.IntervalMultiplier != nil {
		control.IntervalMultiplier = *req.IntervalMultiplier
	}
	if req.AutomaticPaused != nil {
		control.AutomaticPaused = *req.AutomaticPaused
	}
	if req.QuarantinedShops != nil {
		control.QuarantinedShops = *req.QuarantinedShops
	}
	control.ControllerID, control.ControllerUntil = "", ""
	if req.LeaseSeconds > 0 {
		control.ControllerID = req.ControllerID
		control.ControllerUntil = now.Add(time.Duration(req.LeaseSeconds) * time.Second).Format(time.RFC3339Nano)
	}
	if !validPublicAccountImportSyncControl(control) {
		response.BadRequest(c, "Invalid sync control configuration")
		return
	}
	control.Revision++
	control.UpdatedAt = now.Format(time.RFC3339Nano)
	if err := savePublicAccountImportSyncControlLocked(control); err != nil {
		response.InternalError(c, "Failed to save sync control; previous configuration retained")
		return
	}
	response.Success(c, control)
}

func (h *AccountHandler) GetPublicAccountImportSyncControl(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	if !authorizePublicAccountImportProductWorker(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	shops, err := snapshotPublicAccountImportShops()
	if err != nil {
		response.InternalError(c, "Failed to load shop links")
		return
	}
	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	control, err := loadPublicAccountImportSyncControlLocked()
	if err != nil {
		response.InternalError(c, "Failed to load sync control")
		return
	}
	if err := loadPublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to load product cache")
		return
	}
	now := time.Now().UTC()
	out := PublicAccountImportSyncControlResponse{Control: control, ObservedAt: now.Format(time.RFC3339Nano),
		OrdinaryCycleSeconds: int(publicAccountImportProductRefreshAge.Seconds()) * control.IntervalMultiplier,
		Shops:                []PublicAccountImportSyncShopStats{}, Worker: publicAccountImportProductSyncWorkerStatus(now)}
	for _, shop := range supportedPublicAccountImportProductShops(shops) {
		cached := publicProductCache.Shops[shop.ID]
		platformID, _ := publicAccountImportShopToken(shop.URL)
		active := publicAccountImportProductSyncIsActive(cached, now)
		due := !active && publicAccountImportProductSyncEligible(shop, cached, now, control.IntervalMultiplier, false)
		_, isolated := control.QuarantinedShops[shop.ID]
		next := publicAccountImportProductAutomaticAt(shop, cached, now, control.IntervalMultiplier)
		stat := PublicAccountImportSyncShopStats{ShopID: shop.ID, PlatformID: platformID, TrustLevel: shop.TrustLevel,
			AutomaticDue: due, AutomaticEligible: due && !control.AutomaticPaused && !isolated,
			Active: active, ManualPending: publicAccountImportProductRefreshIsPending(cached, now),
			NextAutomaticAt: next.Format(time.RFC3339Nano), SnapshotComplete: publicAccountImportProductSnapshotIsAuthoritative(cached, now),
			SnapshotUpdatedAt: cached.UpdatedAt, SourceProductCount: cached.SourceProductCount, ProductCount: len(cached.Products),
			FirstStartedAt: cached.FirstStartedAt, FirstSyncedAt: cached.FirstSyncedAt, LastAttempt: cached.LastAttempt,
			SuccessCount: cached.SuccessCount, FailureCount: cached.FailureCount, ConsecutiveFailures: cached.ConsecutiveFailures,
			LastFailureKind: cached.LastFailureKind, LastFailureAt: cached.LastFailureAt,
			Error: sanitizePublicAccountImportProductSyncWorkerReason(cached.Error), RetryNotBeforeAt: cached.RetryNotBeforeAt}
		if due {
			stat.WaitingSeconds = max(0, int(now.Sub(next).Seconds()))
			out.AutomaticDue++
		}
		if stat.AutomaticEligible {
			out.AutomaticEligible++
		}
		if active {
			out.ActiveShops++
		}
		out.Shops = append(out.Shops, stat)
	}
	response.Success(c, out)
}

func publicAccountImportProductSyncEligible(shop PublicAccountImportShop, cached publicAccountImportProductShopCache, now time.Time, multiplier int, manual bool) bool {
	if publicAccountImportProductFailureRetryAfter(cached, now) > 0 {
		return false
	}
	if !manual && publicAccountImportProductSnapshotIsAuthoritative(cached, now) &&
		publicAccountImportProductCacheIsFresh(cached, now, publicAccountImportProductRefreshAgeForShop(shop)*time.Duration(multiplier)) {
		return false
	}
	attempt := parsePublicAccountImportProductTimestamp(cached.LastAttempt)
	return attempt.IsZero() || attempt.After(now.Add(time.Minute)) || now.Sub(attempt) >= publicAccountImportProductRetryAgeForShop(shop)
}

func publicAccountImportProductAutomaticAt(shop PublicAccountImportShop, cached publicAccountImportProductShopCache, now time.Time, multiplier int) time.Time {
	next := parsePublicAccountImportProductTimestamp(shop.CreatedAt)
	if next.IsZero() || next.After(now) {
		next = now
	}
	if publicAccountImportProductSnapshotIsAuthoritative(cached, now) {
		updated := parsePublicAccountImportProductTimestamp(cached.UpdatedAt)
		if !updated.After(now.Add(time.Minute)) {
			next = updated.Add(publicAccountImportProductRefreshAgeForShop(shop) * time.Duration(multiplier))
		}
	}
	attempt := parsePublicAccountImportProductTimestamp(cached.LastAttempt)
	if !attempt.IsZero() && !attempt.After(now.Add(time.Minute)) && attempt.Add(publicAccountImportProductRetryAgeForShop(shop)).After(next) {
		next = attempt.Add(publicAccountImportProductRetryAgeForShop(shop))
	}
	if retry := publicAccountImportProductFailureRetryAfter(cached, now); retry > 0 && now.Add(time.Duration(retry)*time.Second).After(next) {
		next = now.Add(time.Duration(retry) * time.Second)
	}
	return next
}
