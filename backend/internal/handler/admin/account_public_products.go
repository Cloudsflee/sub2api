package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

const (
	publicAccountImportProductsFileEnv        = "PUBLIC_ACCOUNT_IMPORT_PRODUCTS_FILE"
	publicAccountImportProductsFile           = "/app/data/public-account-import-products.json"
	publicAccountImportProductStoreVersion    = 1
	publicAccountImportProductSyncInterval    = 10 * time.Second
	publicAccountImportProductRefreshAge      = 15 * time.Minute
	publicAccountImportProductRefreshCooldown = 5 * time.Minute
	publicAccountImportProductRetryAge        = 1 * time.Minute
	publicAccountImportProductSyncLeaseAge    = 5 * time.Minute
	publicAccountImportProductMaxCacheAge     = 15 * time.Minute
	publicAccountImportProductRefreshMaxAge   = 30 * time.Minute
	publicAccountImportProductMaxSyncJobs     = 2
	publicAccountImportProductMaxBody         = 8 << 20
	publicAccountImportProductFailureMaxBody  = 8 << 10
)

type PublicAccountImportProduct struct {
	ID              string  `json:"id"`
	ShopID          string  `json:"shop_id"`
	ShopName        string  `json:"shop_name"`
	ShopURL         string  `json:"shop_url"`
	Name            string  `json:"name"`
	URL             string  `json:"url"`
	Image           string  `json:"image,omitempty"`
	Category        string  `json:"category,omitempty"`
	GoodsType       string  `json:"goods_type"`
	Price           float64 `json:"price"`
	MarketPrice     float64 `json:"market_price,omitempty"`
	Stock           int     `json:"stock"`
	MinimumQuantity int     `json:"minimum_quantity"`
	UpdatedAt       string  `json:"updated_at"`
}

type PublicAccountImportProductsResponse struct {
	Products         []PublicAccountImportProduct           `json:"products"`
	ShopCount        int                                    `json:"shop_count"`
	PendingShops     int                                    `json:"pending_shops"`
	QueuedShops      int                                    `json:"queued_shops"`
	RefreshingShops  int                                    `json:"refreshing_shops"`
	FailedShops      int                                    `json:"failed_shops"`
	RefreshSeconds   int                                    `json:"refresh_seconds"`
	ShopSyncStatuses []PublicAccountImportProductSyncStatus `json:"shop_sync_statuses"`
}

type PublicAccountImportProductSyncStatus struct {
	ShopID            string `json:"shop_id"`
	State             string `json:"state"`
	UpdatedAt         string `json:"updated_at"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
}

type PublicAccountImportProductRefreshRequest struct {
	ShopID string `json:"shop_id"`
}

type PublicAccountImportProductRefreshResponse struct {
	Accepted          bool   `json:"accepted"`
	ShopID            string `json:"shop_id"`
	State             string `json:"state"`
	UpdatedAt         string `json:"updated_at"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
}

type PublicAccountImportProductSyncJob struct {
	ShopID    string `json:"shop_id"`
	ShopName  string `json:"shop_name"`
	ShopURL   string `json:"shop_url"`
	Token     string `json:"token"`
	AttemptID string `json:"attempt_id"`
}

type PublicAccountImportProductSyncJobResponse struct {
	Job  *PublicAccountImportProductSyncJob  `json:"job"`
	Jobs []PublicAccountImportProductSyncJob `json:"jobs"`
}

type PublicAccountImportProductSyncItem struct {
	GoodsKey        string  `json:"goods_key"`
	Name            string  `json:"name"`
	URL             string  `json:"url"`
	Image           string  `json:"image"`
	Category        string  `json:"category"`
	GoodsType       string  `json:"goods_type"`
	Price           float64 `json:"price"`
	MarketPrice     float64 `json:"market_price"`
	Stock           int     `json:"stock"`
	MinimumQuantity int     `json:"minimum_quantity"`
}

type PublicAccountImportProductSyncRequest struct {
	ShopID    string                               `json:"shop_id"`
	AttemptID string                               `json:"attempt_id"`
	Products  []PublicAccountImportProductSyncItem `json:"products"`
}

type PublicAccountImportProductSyncFailureRequest struct {
	ShopID    string `json:"shop_id"`
	AttemptID string `json:"attempt_id"`
	Error     string `json:"error"`
}

type publicAccountImportProductShopCache struct {
	ShopID                   string                       `json:"shop_id"`
	LastAttempt              string                       `json:"last_attempt"`
	SyncStartedAt            string                       `json:"sync_started_at,omitempty"`
	SyncAttemptID            string                       `json:"sync_attempt_id,omitempty"`
	UpdatedAt                string                       `json:"updated_at,omitempty"`
	RefreshRequestedAt       string                       `json:"refresh_requested_at,omitempty"`
	ManualRefreshCompletedAt string                       `json:"manual_refresh_completed_at,omitempty"`
	Error                    string                       `json:"error,omitempty"`
	Products                 []PublicAccountImportProduct `json:"products"`
}

type publicAccountImportProductStore struct {
	Version int                                            `json:"version"`
	Shops   map[string]publicAccountImportProductShopCache `json:"shops"`
}

type publicAccountImportProductRefreshStatus struct {
	Queued     int
	Refreshing int
	Failed     int
	Requested  int
}

var (
	publicProductCacheMu     sync.Mutex
	publicProductCacheLoaded bool
	publicProductCache       = publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{}}
	publicProductLastJobAt   time.Time
)

func (h *AccountHandler) ListPublicAccountImportProducts(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	shops, err := snapshotPublicAccountImportShops()
	if err != nil {
		response.InternalError(c, "Failed to load shop links")
		return
	}
	shops = supportedPublicAccountImportProductShops(shops)
	store, err := snapshotPublicAccountImportProductStore()
	if err != nil {
		response.InternalError(c, "Failed to load product cache")
		return
	}

	now := time.Now().UTC()
	products, pending, queued, refreshing, failed := publicAccountImportProductSnapshot(shops, store, now)
	response.Success(c, PublicAccountImportProductsResponse{
		Products: products, ShopCount: len(shops), PendingShops: pending, QueuedShops: queued, RefreshingShops: refreshing, FailedShops: failed,
		RefreshSeconds: int(publicAccountImportProductRefreshAge.Seconds()), ShopSyncStatuses: publicAccountImportProductSyncStatuses(shops, store, now),
	})
}

func (h *AccountHandler) GetPublicAccountImportProductSyncJob(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	shops, err := snapshotPublicAccountImportShops()
	if err != nil {
		response.InternalError(c, "Failed to load shop links")
		return
	}
	shops = supportedPublicAccountImportProductShops(shops)

	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to load product cache")
		return
	}
	now := time.Now().UTC()
	if now.Sub(publicProductLastJobAt) < publicAccountImportProductSyncInterval {
		response.Success(c, PublicAccountImportProductSyncJobResponse{})
		return
	}
	limit := 1
	if requested, err := strconv.Atoi(strings.TrimSpace(c.Query("limit"))); err == nil && requested > 0 {
		limit = min(requested, publicAccountImportProductMaxSyncJobs)
	}
	availableSlots := publicAccountImportProductMaxSyncJobs - countPublicAccountImportProductActiveSyncs(shops, publicProductCache, now)
	if availableSlots <= 0 {
		response.Success(c, PublicAccountImportProductSyncJobResponse{})
		return
	}
	limit = min(limit, availableSlots)
	selected := selectPublicAccountImportProductSyncShops(shops, publicProductCache, now, limit)
	if len(selected) == 0 {
		response.Success(c, PublicAccountImportProductSyncJobResponse{})
		return
	}
	jobs := make([]PublicAccountImportProductSyncJob, 0, len(selected))
	previousShops := publicProductCache.Shops
	publicProductCache.Shops = maps.Clone(previousShops)
	for _, shop := range selected {
		token, tokenErr := publicAccountImportShopToken(shop.URL)
		cached := publicProductCache.Shops[shop.ID]
		cached.ShopID = shop.ID
		cached.LastAttempt = now.Format(time.RFC3339Nano)
		if tokenErr != nil {
			cached.Error = tokenErr.Error()
			cached.SyncStartedAt = ""
			cached.SyncAttemptID = ""
			cached.RefreshRequestedAt = ""
			publicProductCache.Shops[shop.ID] = cached
			continue
		}
		cached.Error = ""
		cached.SyncStartedAt = cached.LastAttempt
		cached.SyncAttemptID = publicAccountImportProductSyncAttemptID(shop.ID, now)
		publicProductCache.Shops[shop.ID] = cached
		jobs = append(jobs, PublicAccountImportProductSyncJob{
			ShopID: shop.ID, ShopName: shop.Name, ShopURL: shop.URL, Token: token, AttemptID: cached.SyncAttemptID,
		})
	}
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache.Shops = previousShops
		response.InternalError(c, "Failed to lease product sync jobs")
		return
	}
	if len(jobs) == 0 {
		response.Success(c, PublicAccountImportProductSyncJobResponse{})
		return
	}
	publicProductLastJobAt = now
	first := jobs[0]
	response.Success(c, PublicAccountImportProductSyncJobResponse{Job: &first, Jobs: jobs})
}

func (h *AccountHandler) RequestPublicAccountImportProductRefresh(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, publicAccountImportProductFailureMaxBody)
	var req PublicAccountImportProductRefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid product refresh request")
		return
	}
	req.ShopID = strings.TrimSpace(req.ShopID)
	if req.ShopID == "" {
		response.BadRequest(c, "Product refresh shop is required")
		return
	}

	shops, err := snapshotPublicAccountImportShops()
	if err != nil {
		response.InternalError(c, "Failed to load shop links")
		return
	}
	var shop *PublicAccountImportShop
	for i := range shops {
		if shops[i].ID == req.ShopID {
			shop = &shops[i]
			break
		}
	}
	if shop == nil {
		response.BadRequest(c, "Shop is not available")
		return
	}
	if _, err := publicAccountImportShopToken(shop.URL); err != nil {
		response.BadRequest(c, "Shop is not supported")
		return
	}

	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to load product cache")
		return
	}

	now := time.Now().UTC()
	cached := publicProductCache.Shops[shop.ID]
	status := publicAccountImportProductSyncStatusForShop(shop.ID, cached, now)
	if status.State == "queued" || status.State == "refreshing" || status.RetryAfterSeconds > 0 {
		response.Success(c, publicAccountImportProductRefreshResponse(false, status))
		return
	}

	cached.ShopID = shop.ID
	cached.RefreshRequestedAt = now.Format(time.RFC3339Nano)
	previousShops := publicProductCache.Shops
	publicProductCache.Shops = maps.Clone(previousShops)
	publicProductCache.Shops[shop.ID] = cached
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache.Shops = previousShops
		response.InternalError(c, "Failed to queue product refresh")
		return
	}
	publicProductLastJobAt = time.Time{}
	status = publicAccountImportProductSyncStatusForShop(shop.ID, cached, now)
	response.Success(c, publicAccountImportProductRefreshResponse(true, status))
}

func (h *AccountHandler) SubmitPublicAccountImportProductSync(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, publicAccountImportProductMaxBody)
	var req PublicAccountImportProductSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid product sync request")
		return
	}
	if len(req.Products) > 1000 {
		response.BadRequest(c, "Product sync contains too many products")
		return
	}
	shops, err := snapshotPublicAccountImportShops()
	if err != nil {
		response.InternalError(c, "Failed to load shop links")
		return
	}
	var shop *PublicAccountImportShop
	for i := range shops {
		if shops[i].ID == req.ShopID {
			candidate := shops[i]
			shop = &candidate
			break
		}
	}
	if shop == nil {
		response.BadRequest(c, "Shop is not available")
		return
	}
	if _, err := publicAccountImportShopToken(shop.URL); err != nil {
		response.BadRequest(c, "Shop is not supported")
		return
	}

	now := time.Now().UTC()
	updatedAt := now.Format(time.RFC3339Nano)
	products := make([]PublicAccountImportProduct, 0, len(req.Products))
	seen := make(map[string]struct{}, len(req.Products))
	for _, item := range req.Products {
		product, ok := normalizePublicProductSyncItem(*shop, item, updatedAt)
		if !ok {
			continue
		}
		if _, exists := seen[product.ID]; exists {
			continue
		}
		seen[product.ID] = struct{}{}
		products = append(products, product)
	}

	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to load product cache")
		return
	}
	cached := publicProductCache.Shops[shop.ID]
	if req.AttemptID != "" && cached.SyncAttemptID != req.AttemptID {
		response.Error(c, http.StatusConflict, "Product sync job lease expired")
		return
	}
	cached.ShopID = shop.ID
	cached.Error = ""
	cached.SyncStartedAt = ""
	cached.SyncAttemptID = ""
	cached.UpdatedAt = updatedAt
	if requestedAt := parsePublicAccountImportProductTimestamp(cached.RefreshRequestedAt); !requestedAt.IsZero() && !requestedAt.After(now.Add(time.Minute)) {
		cached.ManualRefreshCompletedAt = updatedAt
	}
	cached.RefreshRequestedAt = ""
	cached.Products = products
	previousShops := publicProductCache.Shops
	publicProductCache.Shops = maps.Clone(previousShops)
	publicProductCache.Shops[shop.ID] = cached
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache.Shops = previousShops
		response.InternalError(c, "Failed to save product cache")
		return
	}
	response.Success(c, gin.H{"accepted": len(products)})
}

func (h *AccountHandler) FailPublicAccountImportProductSync(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, publicAccountImportProductFailureMaxBody)
	var req PublicAccountImportProductSyncFailureRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid product sync failure report")
		return
	}
	req.ShopID = strings.TrimSpace(req.ShopID)
	req.AttemptID = strings.TrimSpace(req.AttemptID)
	if req.ShopID == "" || req.AttemptID == "" {
		response.BadRequest(c, "Product sync shop and attempt are required")
		return
	}

	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to load product cache")
		return
	}
	cached, ok := publicProductCache.Shops[req.ShopID]
	if !ok || cached.SyncAttemptID != req.AttemptID {
		response.Success(c, gin.H{"accepted": false})
		return
	}
	cached.Error = publicAccountImportProductSyncError(req.Error)
	cached.SyncStartedAt = ""
	cached.SyncAttemptID = ""
	previousShops := publicProductCache.Shops
	publicProductCache.Shops = maps.Clone(previousShops)
	publicProductCache.Shops[req.ShopID] = cached
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache.Shops = previousShops
		response.InternalError(c, "Failed to save product sync failure")
		return
	}
	response.Success(c, gin.H{"accepted": true})
}

func normalizePublicProductSyncItem(shop PublicAccountImportShop, item PublicAccountImportProductSyncItem, updatedAt string) (PublicAccountImportProduct, bool) {
	item.GoodsKey = strings.TrimSpace(item.GoodsKey)
	item.Name = strings.TrimSpace(item.Name)
	minimumQuantity := item.MinimumQuantity
	if minimumQuantity == 0 {
		minimumQuantity = 1
	}
	if item.GoodsKey == "" || len(item.GoodsKey) > 100 || item.Name == "" || len(item.Name) > 500 || minimumQuantity < 1 || item.Stock < minimumQuantity || item.Price < 0 || item.Price > 1_000_000 || item.MarketPrice < 0 || item.MarketPrice > 1_000_000 {
		return PublicAccountImportProduct{}, false
	}
	productURL, err := url.Parse(strings.TrimSpace(item.URL))
	if err != nil || productURL.Scheme != "https" || !strings.EqualFold(productURL.Hostname(), "pay.ldxp.cn") || !strings.HasPrefix(productURL.Path, "/item/") {
		return PublicAccountImportProduct{}, false
	}
	image := strings.TrimSpace(item.Image)
	if image != "" {
		imageURL, err := url.Parse(image)
		allowedImageHost := err == nil && imageURL.Scheme == "https" && (strings.EqualFold(imageURL.Hostname(), "qn.ldxp.cn") || strings.EqualFold(imageURL.Hostname(), "www.ldxp.cn") || strings.EqualFold(imageURL.Hostname(), "pay.ldxp.cn"))
		if !allowedImageHost {
			image = ""
		}
	}
	goodsType := strings.ToLower(strings.TrimSpace(item.GoodsType))
	if goodsType != "card" && goodsType != "article" && goodsType != "resource" && goodsType != "equity" {
		return PublicAccountImportProduct{}, false
	}
	return PublicAccountImportProduct{
		ID: publicAccountImportProductID(shop.ID, item.GoodsKey), ShopID: shop.ID,
		ShopName: shop.Name, ShopURL: shop.URL, Name: item.Name, URL: productURL.String(),
		Image: image, Category: strings.TrimSpace(item.Category), GoodsType: goodsType,
		Price: item.Price, MarketPrice: item.MarketPrice, Stock: item.Stock, MinimumQuantity: minimumQuantity, UpdatedAt: updatedAt,
	}, true
}

func snapshotPublicAccountImportShops() ([]PublicAccountImportShop, error) {
	publicAccountImportShopLinksMu.Lock()
	defer publicAccountImportShopLinksMu.Unlock()
	return loadPublicAccountImportShops(publicAccountImportShopLinksPath())
}

func supportedPublicAccountImportProductShops(shops []PublicAccountImportShop) []PublicAccountImportShop {
	supported := make([]PublicAccountImportShop, 0, len(shops))
	for _, shop := range shops {
		if _, err := publicAccountImportShopToken(shop.URL); err == nil {
			supported = append(supported, shop)
		}
	}
	return supported
}

func snapshotPublicAccountImportProductStore() (publicAccountImportProductStore, error) {
	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		return publicAccountImportProductStore{}, err
	}
	data, err := json.Marshal(publicProductCache)
	if err != nil {
		return publicAccountImportProductStore{}, err
	}
	var clone publicAccountImportProductStore
	err = json.Unmarshal(data, &clone)
	return clone, err
}

func publicAccountImportProductSnapshot(shops []PublicAccountImportShop, store publicAccountImportProductStore, now time.Time) ([]PublicAccountImportProduct, int, int, int, int) {
	products := make([]PublicAccountImportProduct, 0)
	pending := 0
	for _, shop := range shops {
		cached, ok := store.Shops[shop.ID]
		if !ok {
			pending++
			continue
		}
		if !publicAccountImportProductCacheIsFresh(cached, now, publicAccountImportProductMaxCacheAge) {
			pending++
		}
		for _, product := range cached.Products {
			product.MinimumQuantity = max(product.MinimumQuantity, 1)
			if product.Stock >= product.MinimumQuantity {
				products = append(products, product)
			}
		}
	}
	sort.SliceStable(products, func(i, j int) bool {
		if products[i].Price != products[j].Price {
			return products[i].Price > products[j].Price
		}
		if products[i].ShopName != products[j].ShopName {
			return products[i].ShopName < products[j].ShopName
		}
		return products[i].Name < products[j].Name
	})
	status := publicAccountImportProductRefreshStatusForShops(shops, store, now)
	return products, pending, status.Queued, status.Refreshing, status.Failed
}

func publicAccountImportProductCacheIsFresh(cached publicAccountImportProductShopCache, now time.Time, maxAge time.Duration) bool {
	updatedAt := parsePublicAccountImportProductTimestamp(cached.UpdatedAt)
	if updatedAt.IsZero() || updatedAt.After(now.Add(time.Minute)) {
		return false
	}
	return now.Sub(updatedAt) <= maxAge
}

func selectPublicAccountImportProductSyncShop(shops []PublicAccountImportShop, store publicAccountImportProductStore, now time.Time) *PublicAccountImportShop {
	selected := selectPublicAccountImportProductSyncShops(shops, store, now, 1)
	if len(selected) == 0 {
		return nil
	}
	return &selected[0]
}

func selectPublicAccountImportProductSyncShops(shops []PublicAccountImportShop, store publicAccountImportProductStore, now time.Time, limit int) []PublicAccountImportShop {
	if limit <= 0 {
		return []PublicAccountImportShop{}
	}
	type candidate struct {
		shop    PublicAccountImportShop
		attempt time.Time
		manual  bool
	}
	candidates := make([]candidate, 0, len(shops))
	for i := range shops {
		cached := store.Shops[shops[i].ID]
		if publicAccountImportProductSyncIsActive(cached, now) {
			continue
		}
		manual := publicAccountImportProductRefreshIsPending(cached, now)
		if !manual && publicAccountImportProductCacheIsFresh(cached, now, publicAccountImportProductRefreshAge) {
			continue
		}
		attempt := parsePublicAccountImportProductTimestamp(cached.LastAttempt)
		if attempt.After(now.Add(time.Minute)) {
			attempt = time.Time{}
		}
		if !attempt.IsZero() && now.Sub(attempt) < publicAccountImportProductRetryAge {
			continue
		}
		candidates = append(candidates, candidate{shop: shops[i], attempt: attempt, manual: manual})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].manual != candidates[j].manual {
			return candidates[i].manual
		}
		return candidates[i].attempt.Before(candidates[j].attempt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	selected := make([]PublicAccountImportShop, len(candidates))
	for i := range candidates {
		selected[i] = candidates[i].shop
	}
	return selected
}

func publicAccountImportProductRefreshIsPending(cached publicAccountImportProductShopCache, now time.Time) bool {
	pending, _ := publicAccountImportProductRefreshState(cached, now)
	return pending
}

func publicAccountImportProductRefreshState(cached publicAccountImportProductShopCache, now time.Time) (bool, bool) {
	requestedAt := parsePublicAccountImportProductTimestamp(cached.RefreshRequestedAt)
	if requestedAt.IsZero() || requestedAt.After(now.Add(time.Minute)) {
		return false, false
	}
	updatedAt := parsePublicAccountImportProductTimestamp(cached.UpdatedAt)
	if !updatedAt.IsZero() && !updatedAt.After(now.Add(time.Minute)) && !updatedAt.Before(requestedAt) {
		return false, false
	}
	if now.Sub(requestedAt) > publicAccountImportProductRefreshMaxAge {
		return false, true
	}
	return true, false
}

func publicAccountImportProductSyncIsActive(cached publicAccountImportProductShopCache, now time.Time) bool {
	if strings.TrimSpace(cached.Error) != "" {
		return false
	}
	startedAt := parsePublicAccountImportProductTimestamp(cached.SyncStartedAt)
	if startedAt.IsZero() || startedAt.After(now.Add(time.Minute)) {
		return false
	}
	return now.Sub(startedAt) <= publicAccountImportProductSyncLeaseAge
}

func countPublicAccountImportProductActiveSyncs(shops []PublicAccountImportShop, store publicAccountImportProductStore, now time.Time) int {
	active := 0
	for _, shop := range shops {
		if publicAccountImportProductSyncIsActive(store.Shops[shop.ID], now) {
			active++
		}
	}
	return active
}

func publicAccountImportProductRefreshStatusForShops(shops []PublicAccountImportShop, store publicAccountImportProductStore, now time.Time) publicAccountImportProductRefreshStatus {
	status := publicAccountImportProductRefreshStatus{}
	for _, shop := range shops {
		cached := store.Shops[shop.ID]
		requested, failed := publicAccountImportProductRefreshState(cached, now)
		active := publicAccountImportProductSyncIsActive(cached, now)
		if failed && active {
			status.Requested++
			status.Refreshing++
			continue
		}
		if failed {
			status.Failed++
		}
		if !requested {
			continue
		}
		status.Requested++
		if active {
			status.Refreshing++
		} else {
			status.Queued++
		}
	}
	return status
}

func publicAccountImportProductSyncStatuses(shops []PublicAccountImportShop, store publicAccountImportProductStore, now time.Time) []PublicAccountImportProductSyncStatus {
	statuses := make([]PublicAccountImportProductSyncStatus, 0, len(shops))
	for _, shop := range shops {
		statuses = append(statuses, publicAccountImportProductSyncStatusForShop(shop.ID, store.Shops[shop.ID], now))
	}
	return statuses
}

func publicAccountImportProductSyncStatusForShop(shopID string, cached publicAccountImportProductShopCache, now time.Time) PublicAccountImportProductSyncStatus {
	state := "idle"
	requested, failed := publicAccountImportProductRefreshState(cached, now)
	if publicAccountImportProductSyncIsActive(cached, now) {
		state = "refreshing"
	} else if requested {
		state = "queued"
	} else if failed {
		state = "failed"
	}
	return PublicAccountImportProductSyncStatus{
		ShopID: shopID, State: state, UpdatedAt: cached.UpdatedAt,
		RetryAfterSeconds: publicAccountImportProductRefreshRetryAfter(cached, now),
	}
}

func publicAccountImportProductRefreshRetryAfter(cached publicAccountImportProductShopCache, now time.Time) int {
	completedAt := parsePublicAccountImportProductTimestamp(cached.ManualRefreshCompletedAt)
	if completedAt.IsZero() || completedAt.After(now) {
		return 0
	}
	remaining := completedAt.Add(publicAccountImportProductRefreshCooldown).Sub(now)
	if remaining <= 0 {
		return 0
	}
	return int((remaining + time.Second - 1) / time.Second)
}

func publicAccountImportProductRefreshResponse(accepted bool, status PublicAccountImportProductSyncStatus) PublicAccountImportProductRefreshResponse {
	return PublicAccountImportProductRefreshResponse{
		Accepted: accepted, ShopID: status.ShopID, State: status.State,
		UpdatedAt: status.UpdatedAt, RetryAfterSeconds: status.RetryAfterSeconds,
	}
}

func parsePublicAccountImportProductTimestamp(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return parsed
}

func loadPublicProductCacheLocked() error {
	if publicProductCacheLoaded {
		return nil
	}
	data, err := os.ReadFile(publicAccountImportProductsPath())
	if errors.Is(err, os.ErrNotExist) {
		publicProductCacheLoaded = true
		return nil
	}
	if err != nil {
		return err
	}
	var store publicAccountImportProductStore
	if err := json.Unmarshal(data, &store); err != nil {
		return err
	}
	if store.Version != publicAccountImportProductStoreVersion {
		return fmt.Errorf("unsupported product cache version: %d", store.Version)
	}
	if store.Shops == nil {
		store.Shops = map[string]publicAccountImportProductShopCache{}
	}
	publicProductCache = store
	publicProductCacheLoaded = true
	return nil
}

func savePublicProductCacheLocked() error {
	data, err := json.MarshalIndent(publicProductCache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := publicAccountImportProductsPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".public-account-import-products-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func publicAccountImportProductsPath() string {
	if path := strings.TrimSpace(os.Getenv(publicAccountImportProductsFileEnv)); path != "" {
		return path
	}
	return publicAccountImportProductsFile
}

func publicAccountImportShopToken(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "pay.ldxp.cn") {
		return "", errors.New("product sync supports pay.ldxp.cn shop links only")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "shop" || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("invalid pay.ldxp.cn shop link")
	}
	return parts[1], nil
}

func publicAccountImportProductID(shopID, goodsKey string) string {
	sum := sha256.Sum256([]byte(shopID + ":" + goodsKey))
	return hex.EncodeToString(sum[:12])
}

func publicAccountImportProductSyncAttemptID(shopID string, now time.Time) string {
	sum := sha256.Sum256([]byte(shopID + ":" + now.Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:16])
}

func publicAccountImportProductSyncError(value string) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if value == "" {
		return "product sync failed"
	}
	runes := []rune(value)
	if len(runes) > 500 {
		return string(runes[:500])
	}
	return value
}
