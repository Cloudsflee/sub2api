package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	publicAccountImportProductSyncTokenEnv    = "PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN"
	publicAccountImportProductStrictModeEnv   = "PUBLIC_ACCOUNT_IMPORT_PRODUCT_STRICT_MODE"
	publicAccountImportProductsFile           = "/app/data/public-account-import-products.json"
	publicAccountImportProductStoreVersion    = 1
	publicAccountImportProductSchemaVersion   = 2
	publicAccountImportProductSyncInterval    = time.Second
	publicAccountImportProductRefreshAge      = 15 * time.Minute
	publicAccountImportProductRefreshCooldown = 5 * time.Minute
	publicAccountImportProductRetryAge        = 1 * time.Minute
	publicAccountImportProductSyncLeaseAge    = 90 * time.Second
	publicAccountImportProductSyncMaxAge      = 30 * time.Minute
	publicAccountImportProductMaxCacheAge     = 30 * time.Minute
	publicAccountImportProductRefreshMaxAge   = 30 * time.Minute
	publicAccountImportProductMaxSyncJobs     = 6
	publicAccountImportProductMaxProducts     = 1000
	publicAccountImportProductMaxBody         = 8 << 20
	publicAccountImportProductFailureMaxBody  = 8 << 10
)

type PublicAccountImportProduct struct {
	ID              string   `json:"id"`
	ShopID          string   `json:"shop_id"`
	ShopName        string   `json:"shop_name"`
	ShopURL         string   `json:"shop_url"`
	Name            string   `json:"name"`
	URL             string   `json:"url"`
	Image           string   `json:"image,omitempty"`
	Category        string   `json:"category,omitempty"`
	GoodsType       string   `json:"goods_type"`
	Price           float64  `json:"price"`
	MarketPrice     float64  `json:"market_price,omitempty"`
	PayablePrice    *float64 `json:"payable_price,omitempty"`
	UnitPrice       *float64 `json:"unit_price,omitempty"`
	Stock           int      `json:"stock"`
	MinimumQuantity int      `json:"minimum_quantity"`
	QuoteVerifiedAt string   `json:"quote_verified_at,omitempty"`
	UpdatedAt       string   `json:"updated_at"`
}

type PublicAccountImportProductsResponse struct {
	Products         []PublicAccountImportProduct           `json:"products"`
	ShopCount        int                                    `json:"shop_count"`
	PendingShops     int                                    `json:"pending_shops"`
	QueuedShops      int                                    `json:"queued_shops"`
	RefreshingShops  int                                    `json:"refreshing_shops"`
	FailedShops      int                                    `json:"failed_shops"`
	ExpiredShops     int                                    `json:"expired_shops"`
	RefreshSeconds   int                                    `json:"refresh_seconds"`
	ShopSyncStatuses []PublicAccountImportProductSyncStatus `json:"shop_sync_statuses"`
}

type PublicAccountImportProductSyncStatus struct {
	ShopID            string `json:"shop_id"`
	State             string `json:"state"`
	UpdatedAt         string `json:"updated_at"`
	SnapshotState     string `json:"snapshot_state"`
	SnapshotUpdatedAt string `json:"snapshot_updated_at"`
	SnapshotExpiresAt string `json:"snapshot_expires_at"`
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
	SnapshotState     string `json:"snapshot_state"`
	SnapshotUpdatedAt string `json:"snapshot_updated_at"`
	SnapshotExpiresAt string `json:"snapshot_expires_at"`
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
	GoodsKey        string   `json:"goods_key"`
	Name            string   `json:"name"`
	URL             string   `json:"url"`
	Image           string   `json:"image"`
	Category        string   `json:"category"`
	GoodsType       string   `json:"goods_type"`
	Price           *float64 `json:"price"`
	MarketPrice     *float64 `json:"market_price"`
	PayablePrice    *float64 `json:"payable_price"`
	Stock           *int     `json:"stock"`
	MinimumQuantity *int     `json:"minimum_quantity"`
	QuoteVerifiedAt string   `json:"quote_verified_at"`
}

type PublicAccountImportProductSyncRequest struct {
	SchemaVersion           int                                  `json:"schema_version"`
	ShopID                  string                               `json:"shop_id"`
	AttemptID               string                               `json:"attempt_id"`
	SourceProductCount      *int                                 `json:"source_product_count"`
	SellableProductCount    *int                                 `json:"sellable_product_count"`
	UnavailableProductCount *int                                 `json:"unavailable_product_count"`
	Products                []PublicAccountImportProductSyncItem `json:"products"`
}

type PublicAccountImportProductSyncFailureRequest struct {
	ShopID    string `json:"shop_id"`
	AttemptID string `json:"attempt_id"`
	Error     string `json:"error"`
}

type PublicAccountImportProductSyncHeartbeatRequest struct {
	ShopID    string `json:"shop_id"`
	AttemptID string `json:"attempt_id"`
}

type publicAccountImportProductShopCache struct {
	ShopID                   string                       `json:"shop_id"`
	SchemaVersion            int                          `json:"schema_version"`
	SourceProductCount       int                          `json:"source_product_count"`
	SellableProductCount     int                          `json:"sellable_product_count"`
	UnavailableProductCount  int                          `json:"unavailable_product_count"`
	LastAttempt              string                       `json:"last_attempt"`
	SyncStartedAt            string                       `json:"sync_started_at,omitempty"`
	SyncHeartbeatAt          string                       `json:"sync_heartbeat_at,omitempty"`
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
	publicProductCacheMu     sync.RWMutex
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
	products, pending, queued, refreshing, failed, expired := publicAccountImportProductSnapshot(shops, store, now)
	result := PublicAccountImportProductsResponse{
		Products: products, ShopCount: len(shops), PendingShops: pending, QueuedShops: queued, RefreshingShops: refreshing, FailedShops: failed,
		ExpiredShops: expired, RefreshSeconds: int(publicAccountImportProductRefreshAge.Seconds()), ShopSyncStatuses: publicAccountImportProductSyncStatuses(shops, store, now),
	}
	writePublicAccountImportProductsResponse(c, result)
}

func (h *AccountHandler) GetPublicAccountImportProductSyncJob(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	if !authorizePublicAccountImportProductWorker(c) {
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
			cached.SyncHeartbeatAt = ""
			cached.SyncAttemptID = ""
			cached.RefreshRequestedAt = ""
			publicProductCache.Shops[shop.ID] = cached
			continue
		}
		cached.Error = ""
		cached.SyncStartedAt = cached.LastAttempt
		cached.SyncHeartbeatAt = cached.LastAttempt
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
	if updatedAt := parsePublicAccountImportProductTimestamp(cached.UpdatedAt); !updatedAt.IsZero() && !updatedAt.Before(now) {
		now = updatedAt.Add(time.Nanosecond)
	}
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
	if !authorizePublicAccountImportProductWorker(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, publicAccountImportProductMaxBody)
	var req PublicAccountImportProductSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid product sync request")
		return
	}
	req.ShopID = strings.TrimSpace(req.ShopID)
	req.AttemptID = strings.TrimSpace(req.AttemptID)
	if req.ShopID == "" || req.AttemptID == "" {
		response.BadRequest(c, "Product sync shop and attempt are required")
		return
	}
	if err := validatePublicAccountImportProductSyncCounts(req); err != nil {
		response.BadRequest(c, err.Error())
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
	publicProductCacheMu.Lock()
	if err := loadPublicProductCacheLocked(); err != nil {
		publicProductCacheMu.Unlock()
		response.InternalError(c, "Failed to load product cache")
		return
	}
	cached := publicProductCache.Shops[shop.ID]
	if cached.SyncAttemptID != req.AttemptID || !publicAccountImportProductSyncIsActive(cached, now) {
		publicProductCacheMu.Unlock()
		response.Error(c, http.StatusConflict, "Product sync job lease expired")
		return
	}
	publicProductCacheMu.Unlock()

	updatedAt := now.Format(time.RFC3339Nano)
	products := make([]PublicAccountImportProduct, 0, len(req.Products))
	seen := make(map[string]struct{}, len(req.Products))
	for index, item := range req.Products {
		product, err := normalizePublicProductSyncItem(*shop, item, updatedAt)
		if err != nil {
			response.BadRequest(c, fmt.Sprintf("Invalid product sync item %d: %v", index, err))
			return
		}
		if _, exists := seen[product.ID]; exists {
			response.BadRequest(c, "Product sync contains duplicate products")
			return
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
	cached = publicProductCache.Shops[shop.ID]
	now = time.Now().UTC()
	if cached.SyncAttemptID != req.AttemptID || !publicAccountImportProductSyncIsActive(cached, now) {
		response.Error(c, http.StatusConflict, "Product sync job lease expired")
		return
	}
	updatedAt = now.Format(time.RFC3339Nano)
	for index := range products {
		products[index].UpdatedAt = updatedAt
	}
	cached.ShopID = shop.ID
	cached.SchemaVersion = req.SchemaVersion
	cached.SourceProductCount = *req.SourceProductCount
	cached.SellableProductCount = *req.SellableProductCount
	cached.UnavailableProductCount = *req.UnavailableProductCount
	cached.Error = ""
	cached.SyncStartedAt = ""
	cached.SyncHeartbeatAt = ""
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
	response.Success(c, gin.H{"accepted": len(products), "schema_version": publicAccountImportProductSchemaVersion})
}

func (h *AccountHandler) FailPublicAccountImportProductSync(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	if !authorizePublicAccountImportProductWorker(c) {
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
	cached.SyncHeartbeatAt = ""
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

func (h *AccountHandler) HeartbeatPublicAccountImportProductSync(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	if !authorizePublicAccountImportProductWorker(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, publicAccountImportProductFailureMaxBody)
	var req PublicAccountImportProductSyncHeartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid product sync heartbeat")
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
	now := time.Now().UTC()
	cached, ok := publicProductCache.Shops[req.ShopID]
	if !ok || cached.SyncAttemptID != req.AttemptID || !publicAccountImportProductSyncIsActive(cached, now) {
		response.Error(c, http.StatusConflict, "Product sync job lease expired")
		return
	}
	cached.SyncHeartbeatAt = now.Format(time.RFC3339Nano)
	previousShops := publicProductCache.Shops
	publicProductCache.Shops = maps.Clone(previousShops)
	publicProductCache.Shops[req.ShopID] = cached
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache.Shops = previousShops
		response.InternalError(c, "Failed to renew product sync job")
		return
	}
	response.Success(c, gin.H{
		"accepted":      true,
		"lease_seconds": int(publicAccountImportProductSyncLeaseAge.Seconds()),
	})
}

func validatePublicAccountImportProductSyncCounts(req PublicAccountImportProductSyncRequest) error {
	if req.SchemaVersion != publicAccountImportProductSchemaVersion {
		return fmt.Errorf("product sync schema_version must be %d", publicAccountImportProductSchemaVersion)
	}
	if req.SourceProductCount == nil || req.SellableProductCount == nil || req.UnavailableProductCount == nil || req.Products == nil {
		return errors.New("product sync metadata and products array are required")
	}
	source := *req.SourceProductCount
	sellable := *req.SellableProductCount
	unavailable := *req.UnavailableProductCount
	if source < 0 || sellable < 0 || unavailable < 0 || source > publicAccountImportProductMaxProducts {
		return errors.New("product sync counts are out of range")
	}
	if sellable != len(req.Products) || source != sellable+unavailable {
		return errors.New("product sync counts do not match the complete snapshot")
	}
	return nil
}

func normalizePublicProductSyncItem(shop PublicAccountImportShop, item PublicAccountImportProductSyncItem, updatedAt string) (PublicAccountImportProduct, error) {
	item.GoodsKey = strings.TrimSpace(item.GoodsKey)
	item.Name = strings.TrimSpace(item.Name)
	if item.Price == nil || item.PayablePrice == nil || item.Stock == nil || item.MinimumQuantity == nil {
		return PublicAccountImportProduct{}, errors.New("price, payable price, stock, and minimum quantity are required")
	}
	price := *item.Price
	payablePrice := *item.PayablePrice
	stock := *item.Stock
	minimumQuantity := *item.MinimumQuantity
	marketPrice := 0.0
	if item.MarketPrice != nil {
		marketPrice = *item.MarketPrice
	}
	if item.GoodsKey == "" || len(item.GoodsKey) > 100 || item.Name == "" || len(item.Name) > 500 {
		return PublicAccountImportProduct{}, errors.New("product identity is invalid")
	}
	if minimumQuantity < 1 || stock < minimumQuantity {
		return PublicAccountImportProduct{}, errors.New("product stock cannot satisfy its minimum quantity")
	}
	if !finitePublicAccountImportProductPrice(price) || price > 1_000_000 || !finitePublicAccountImportProductPrice(marketPrice) || marketPrice > 1_000_000 {
		return PublicAccountImportProduct{}, errors.New("catalog price is invalid")
	}
	if !finitePublicAccountImportProductPrice(payablePrice) {
		return PublicAccountImportProduct{}, errors.New("payable price is invalid")
	}
	unitPrice := payablePrice / float64(minimumQuantity)
	if !finitePublicAccountImportProductPrice(unitPrice) {
		return PublicAccountImportProduct{}, errors.New("unit price is invalid")
	}
	quoteVerifiedAt := parsePublicAccountImportProductTimestamp(item.QuoteVerifiedAt)
	updatedTime := parsePublicAccountImportProductTimestamp(updatedAt)
	if quoteVerifiedAt.IsZero() || quoteVerifiedAt.Before(updatedTime.Add(-publicAccountImportProductSyncMaxAge)) || quoteVerifiedAt.After(updatedTime.Add(time.Minute)) {
		return PublicAccountImportProduct{}, errors.New("quote verification timestamp is invalid")
	}
	productURL, err := url.Parse(strings.TrimSpace(item.URL))
	if err != nil || productURL.Scheme != "https" || !strings.EqualFold(productURL.Hostname(), "pay.ldxp.cn") {
		return PublicAccountImportProduct{}, errors.New("product URL is invalid")
	}
	pathParts := strings.Split(strings.Trim(productURL.EscapedPath(), "/"), "/")
	pathGoodsKey := ""
	if len(pathParts) == 2 && pathParts[0] == "item" {
		pathGoodsKey, err = url.PathUnescape(pathParts[1])
	}
	if err != nil || pathGoodsKey != item.GoodsKey {
		return PublicAccountImportProduct{}, errors.New("product URL does not match its goods key")
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
		return PublicAccountImportProduct{}, errors.New("product type is invalid")
	}
	return PublicAccountImportProduct{
		ID: publicAccountImportProductID(shop.ID, item.GoodsKey), ShopID: shop.ID,
		ShopName: shop.Name, ShopURL: shop.URL, Name: item.Name, URL: productURL.String(),
		Image: image, Category: strings.TrimSpace(item.Category), GoodsType: goodsType,
		Price: price, MarketPrice: marketPrice, PayablePrice: float64Pointer(payablePrice), UnitPrice: float64Pointer(unitPrice),
		Stock: stock, MinimumQuantity: minimumQuantity, QuoteVerifiedAt: quoteVerifiedAt.Format(time.RFC3339Nano), UpdatedAt: updatedAt,
	}, nil
}

func finitePublicAccountImportProductPrice(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func float64Pointer(value float64) *float64 {
	return &value
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
	publicProductCacheMu.RLock()
	if publicProductCacheLoaded {
		store := clonePublicAccountImportProductStore(publicProductCache)
		publicProductCacheMu.RUnlock()
		return store, nil
	}
	publicProductCacheMu.RUnlock()

	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		return publicAccountImportProductStore{}, err
	}
	return clonePublicAccountImportProductStore(publicProductCache), nil
}

func clonePublicAccountImportProductStore(store publicAccountImportProductStore) publicAccountImportProductStore {
	clone := publicAccountImportProductStore{Version: store.Version, Shops: maps.Clone(store.Shops)}
	for shopID, cached := range clone.Shops {
		cached.Products = slices.Clone(cached.Products)
		clone.Shops[shopID] = cached
	}
	return clone
}

func publicAccountImportProductSnapshot(shops []PublicAccountImportShop, store publicAccountImportProductStore, now time.Time) ([]PublicAccountImportProduct, int, int, int, int, int) {
	products := make([]PublicAccountImportProduct, 0)
	pending := 0
	expired := 0
	strictMode := publicAccountImportProductStrictMode()
	for _, shop := range shops {
		cached, ok := store.Shops[shop.ID]
		if !ok {
			pending++
			continue
		}
		snapshotState := publicAccountImportProductSnapshotState(cached, now, strictMode)
		if snapshotState != "fresh" {
			pending++
		}
		if snapshotState == "expired" {
			expired++
			continue
		}
		if snapshotState != "fresh" && snapshotState != "stale" && snapshotState != "legacy" {
			continue
		}
		for _, product := range cached.Products {
			product.MinimumQuantity = max(product.MinimumQuantity, 1)
			if product.Stock >= product.MinimumQuantity {
				products = append(products, product)
			}
		}
	}
	sort.SliceStable(products, func(i, j int) bool {
		leftPrice := publicAccountImportProductEffectiveUnitPrice(products[i])
		rightPrice := publicAccountImportProductEffectiveUnitPrice(products[j])
		if leftPrice != rightPrice {
			return leftPrice > rightPrice
		}
		if products[i].ShopName != products[j].ShopName {
			return products[i].ShopName < products[j].ShopName
		}
		return products[i].Name < products[j].Name
	})
	status := publicAccountImportProductRefreshStatusForShops(shops, store, now)
	return products, pending, status.Queued, status.Refreshing, status.Failed, expired
}

func publicAccountImportProductEffectiveUnitPrice(product PublicAccountImportProduct) float64 {
	if product.UnitPrice != nil && finitePublicAccountImportProductPrice(*product.UnitPrice) {
		return *product.UnitPrice
	}
	return product.Price / float64(max(product.MinimumQuantity, 1))
}

func publicAccountImportProductCacheIsFresh(cached publicAccountImportProductShopCache, now time.Time, maxAge time.Duration) bool {
	updatedAt := parsePublicAccountImportProductTimestamp(cached.UpdatedAt)
	if updatedAt.IsZero() || updatedAt.After(now.Add(time.Minute)) {
		return false
	}
	return now.Sub(updatedAt) <= maxAge
}

func publicAccountImportProductSnapshotState(cached publicAccountImportProductShopCache, now time.Time, strictMode bool) string {
	updatedAt := parsePublicAccountImportProductTimestamp(cached.UpdatedAt)
	if updatedAt.IsZero() {
		return "pending"
	}
	if updatedAt.After(now.Add(time.Minute)) {
		return "expired"
	}
	authoritative := publicAccountImportProductSnapshotIsAuthoritative(cached)
	if cached.SchemaVersion >= publicAccountImportProductSchemaVersion && !authoritative {
		return "expired"
	}
	if !authoritative {
		if strictMode {
			return "expired"
		}
		return "legacy"
	}
	if now.Sub(updatedAt) <= publicAccountImportProductRefreshAge {
		return "fresh"
	}
	if strictMode && now.Sub(updatedAt) > publicAccountImportProductMaxCacheAge {
		return "expired"
	}
	return "stale"
}

func publicAccountImportProductSnapshotIsAuthoritative(cached publicAccountImportProductShopCache) bool {
	if cached.SchemaVersion != publicAccountImportProductSchemaVersion || cached.Products == nil ||
		cached.SourceProductCount < 0 || cached.SourceProductCount > publicAccountImportProductMaxProducts ||
		cached.SellableProductCount != len(cached.Products) ||
		cached.SourceProductCount != cached.SellableProductCount+cached.UnavailableProductCount {
		return false
	}
	updatedAt := parsePublicAccountImportProductTimestamp(cached.UpdatedAt)
	if updatedAt.IsZero() {
		return false
	}
	seen := make(map[string]struct{}, len(cached.Products))
	for _, product := range cached.Products {
		if product.ID == "" || product.PayablePrice == nil || product.UnitPrice == nil ||
			!finitePublicAccountImportProductPrice(*product.PayablePrice) ||
			!finitePublicAccountImportProductPrice(*product.UnitPrice) ||
			product.MinimumQuantity < 1 || product.Stock < product.MinimumQuantity {
			return false
		}
		if _, duplicate := seen[product.ID]; duplicate {
			return false
		}
		seen[product.ID] = struct{}{}
		quoteVerifiedAt := parsePublicAccountImportProductTimestamp(product.QuoteVerifiedAt)
		if quoteVerifiedAt.IsZero() || quoteVerifiedAt.After(updatedAt.Add(time.Minute)) {
			return false
		}
		expectedUnitPrice := *product.PayablePrice / float64(product.MinimumQuantity)
		tolerance := math.Max(1, math.Abs(expectedUnitPrice)) * 1e-9
		if math.Abs(*product.UnitPrice-expectedUnitPrice) > tolerance {
			return false
		}
	}
	return true
}

func publicAccountImportProductStrictMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(publicAccountImportProductStrictModeEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func authorizePublicAccountImportProductWorker(c *gin.Context) bool {
	expected := strings.TrimSpace(os.Getenv(publicAccountImportProductSyncTokenEnv))
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	parts := strings.Split(authorization, " ")
	valid := expected != "" && len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != "" &&
		subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) == 1
	if valid {
		return true
	}
	c.Header("WWW-Authenticate", `Bearer realm="product-sync-worker"`)
	response.Unauthorized(c, "Product sync worker authentication is required")
	return false
}

func writePublicAccountImportProductsResponse(c *gin.Context, result PublicAccountImportProductsResponse) {
	body, err := json.Marshal(response.Response{Code: 0, Message: "success", Data: result})
	if err != nil {
		response.InternalError(c, "Failed to encode product catalog")
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	c.Header("Cache-Control", "public, no-cache")
	c.Header("ETag", etag)
	for _, candidate := range strings.Split(c.GetHeader("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == etag || candidate == "W/"+etag {
			c.Status(http.StatusNotModified)
			return
		}
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
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
	activeManualJobs := 0
	for i := range shops {
		cached := store.Shops[shops[i].ID]
		if publicAccountImportProductSyncIsActive(cached, now) && publicAccountImportProductRefreshIsPending(cached, now) {
			activeManualJobs++
		}
	}
	for i := range shops {
		cached := store.Shops[shops[i].ID]
		if publicAccountImportProductSyncIsActive(cached, now) {
			continue
		}
		manual := publicAccountImportProductRefreshIsPending(cached, now)
		if !manual && publicAccountImportProductSnapshotState(cached, now, false) == "fresh" {
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
	manualCapacity := max(0, 1-activeManualJobs)
	selectedCandidates := make([]candidate, 0, min(limit, len(candidates)))
	selectedManual := 0
	for _, item := range candidates {
		if item.manual {
			if selectedManual >= manualCapacity {
				continue
			}
			selectedManual++
		}
		selectedCandidates = append(selectedCandidates, item)
		if len(selectedCandidates) == limit {
			break
		}
	}
	selected := make([]PublicAccountImportShop, len(selectedCandidates))
	for i := range selectedCandidates {
		selected[i] = selectedCandidates[i].shop
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
	if !publicAccountImportProductSyncWithinMaxAge(cached, now) {
		return false
	}
	heartbeatAt := parsePublicAccountImportProductTimestamp(cached.SyncHeartbeatAt)
	if heartbeatAt.IsZero() {
		heartbeatAt = parsePublicAccountImportProductTimestamp(cached.SyncStartedAt)
	}
	if heartbeatAt.IsZero() || heartbeatAt.After(now.Add(time.Minute)) {
		return false
	}
	return now.Sub(heartbeatAt) <= publicAccountImportProductSyncLeaseAge
}

func publicAccountImportProductSyncWithinMaxAge(cached publicAccountImportProductShopCache, now time.Time) bool {
	startedAt := parsePublicAccountImportProductTimestamp(cached.SyncStartedAt)
	if startedAt.IsZero() || startedAt.After(now.Add(time.Minute)) {
		return false
	}
	return now.Sub(startedAt) <= publicAccountImportProductSyncMaxAge
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
		if active {
			if requested || failed {
				status.Requested++
			}
			status.Refreshing++
			continue
		}
		if requested {
			status.Requested++
			status.Queued++
			continue
		}
		if failed || strings.TrimSpace(cached.Error) != "" {
			status.Failed++
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
	} else if failed || strings.TrimSpace(cached.Error) != "" {
		state = "failed"
	}
	snapshotState := publicAccountImportProductSnapshotState(cached, now, publicAccountImportProductStrictMode())
	expiresAt := ""
	if updatedAt := parsePublicAccountImportProductTimestamp(cached.UpdatedAt); !updatedAt.IsZero() {
		expiresAt = updatedAt.Add(publicAccountImportProductMaxCacheAge).Format(time.RFC3339Nano)
	}
	return PublicAccountImportProductSyncStatus{
		ShopID: shopID, State: state, UpdatedAt: cached.UpdatedAt,
		SnapshotState: snapshotState, SnapshotUpdatedAt: cached.UpdatedAt, SnapshotExpiresAt: expiresAt,
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
		UpdatedAt: status.UpdatedAt, SnapshotState: status.SnapshotState,
		SnapshotUpdatedAt: status.SnapshotUpdatedAt, SnapshotExpiresAt: status.SnapshotExpiresAt,
		RetryAfterSeconds: status.RetryAfterSeconds,
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
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if (len(parts) != 2 && len(parts) != 3) || parts[0] != "shop" {
		return "", errors.New("invalid pay.ldxp.cn shop link")
	}
	token, err := url.PathUnescape(parts[1])
	token = strings.TrimSpace(token)
	if err != nil || token == "" || strings.Contains(token, "/") {
		return "", errors.New("invalid pay.ldxp.cn shop link")
	}
	if len(parts) == 3 {
		categoryKey, err := url.PathUnescape(parts[2])
		categoryKey = strings.TrimSpace(categoryKey)
		if err != nil || categoryKey == "" || strings.Contains(categoryKey, "/") {
			return "", errors.New("invalid pay.ldxp.cn shop link")
		}
	}
	return token, nil
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
