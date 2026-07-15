package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	publicAccountImportProductsFile           = "/app/data/public-account-import-products.json"
	publicAccountImportProductStoreVersion    = 1
	publicAccountImportProductSyncInterval    = 10 * time.Second
	publicAccountImportProductRefreshAge      = 5 * time.Minute
	publicAccountImportProductRetryAge        = 1 * time.Minute
	publicAccountImportProductMaxCacheAge     = 15 * time.Minute
	publicAccountImportProductRefreshCooldown = 5 * time.Minute
	publicAccountImportProductRefreshMaxAge   = 30 * time.Minute
	publicAccountImportProductMaxSyncJobs     = 5
	publicAccountImportProductMaxBody         = 8 << 20
)

type PublicAccountImportProduct struct {
	ID          string  `json:"id"`
	ShopID      string  `json:"shop_id"`
	ShopName    string  `json:"shop_name"`
	ShopURL     string  `json:"shop_url"`
	Name        string  `json:"name"`
	URL         string  `json:"url"`
	Image       string  `json:"image,omitempty"`
	Category    string  `json:"category,omitempty"`
	GoodsType   string  `json:"goods_type"`
	Price       float64 `json:"price"`
	MarketPrice float64 `json:"market_price,omitempty"`
	Stock       int     `json:"stock"`
	UpdatedAt   string  `json:"updated_at"`
}

type PublicAccountImportProductsResponse struct {
	Products        []PublicAccountImportProduct `json:"products"`
	ShopCount       int                          `json:"shop_count"`
	PendingShops    int                          `json:"pending_shops"`
	RefreshingShops int                          `json:"refreshing_shops"`
	RefreshSeconds  int                          `json:"refresh_seconds"`
}

type PublicAccountImportProductSyncJob struct {
	ShopID   string `json:"shop_id"`
	ShopName string `json:"shop_name"`
	ShopURL  string `json:"shop_url"`
	Token    string `json:"token"`
}

type PublicAccountImportProductSyncJobResponse struct {
	Job  *PublicAccountImportProductSyncJob  `json:"job"`
	Jobs []PublicAccountImportProductSyncJob `json:"jobs"`
}

type PublicAccountImportProductRefreshResponse struct {
	Accepted          bool `json:"accepted"`
	QueuedShops       int  `json:"queued_shops"`
	RefreshingShops   int  `json:"refreshing_shops"`
	RetryAfterSeconds int  `json:"retry_after_seconds"`
}

type PublicAccountImportProductSyncItem struct {
	GoodsKey    string  `json:"goods_key"`
	Name        string  `json:"name"`
	URL         string  `json:"url"`
	Image       string  `json:"image"`
	Category    string  `json:"category"`
	GoodsType   string  `json:"goods_type"`
	Price       float64 `json:"price"`
	MarketPrice float64 `json:"market_price"`
	Stock       int     `json:"stock"`
}

type PublicAccountImportProductSyncRequest struct {
	ShopID   string                               `json:"shop_id"`
	Products []PublicAccountImportProductSyncItem `json:"products"`
}

type publicAccountImportProductShopCache struct {
	ShopID             string                       `json:"shop_id"`
	LastAttempt        string                       `json:"last_attempt"`
	UpdatedAt          string                       `json:"updated_at,omitempty"`
	RefreshRequestedAt string                       `json:"refresh_requested_at,omitempty"`
	Error              string                       `json:"error,omitempty"`
	Products           []PublicAccountImportProduct `json:"products"`
}

type publicAccountImportProductStore struct {
	Version int                                            `json:"version"`
	Shops   map[string]publicAccountImportProductShopCache `json:"shops"`
}

var (
	publicProductCacheMu             sync.Mutex
	publicProductCacheLoaded         bool
	publicProductCache               = publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{}}
	publicProductLastJobAt           time.Time
	publicProductLastManualRefreshAt time.Time
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
	store, err := snapshotPublicAccountImportProductStore()
	if err != nil {
		response.InternalError(c, "Failed to load product cache")
		return
	}

	products, pending, refreshing := publicAccountImportProductSnapshot(shops, store, time.Now().UTC())
	response.Success(c, PublicAccountImportProductsResponse{
		Products: products, ShopCount: len(shops), PendingShops: pending, RefreshingShops: refreshing,
		RefreshSeconds: int(publicAccountImportProductRefreshAge.Seconds()),
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
	selected := selectPublicAccountImportProductSyncShops(shops, publicProductCache, now, limit)
	if len(selected) == 0 {
		response.Success(c, PublicAccountImportProductSyncJobResponse{})
		return
	}
	jobs := make([]PublicAccountImportProductSyncJob, 0, len(selected))
	for _, shop := range selected {
		token, tokenErr := publicAccountImportShopToken(shop.URL)
		cached := publicProductCache.Shops[shop.ID]
		cached.ShopID = shop.ID
		cached.LastAttempt = now.Format(time.RFC3339)
		if tokenErr != nil {
			cached.Error = tokenErr.Error()
			cached.RefreshRequestedAt = ""
			publicProductCache.Shops[shop.ID] = cached
			continue
		}
		publicProductCache.Shops[shop.ID] = cached
		jobs = append(jobs, PublicAccountImportProductSyncJob{
			ShopID: shop.ID, ShopName: shop.Name, ShopURL: shop.URL, Token: token,
		})
	}
	_ = savePublicProductCacheLocked()
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
	shops, err := snapshotPublicAccountImportShops()
	if err != nil {
		response.InternalError(c, "Failed to load shop links")
		return
	}

	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to load product cache")
		return
	}
	now := time.Now().UTC()
	refreshing := countPublicAccountImportProductRefreshes(shops, publicProductCache, now)
	if refreshing > 0 {
		response.Success(c, PublicAccountImportProductRefreshResponse{RefreshingShops: refreshing})
		return
	}
	if elapsed := now.Sub(publicProductLastManualRefreshAt); elapsed < publicAccountImportProductRefreshCooldown {
		retryAfter := publicAccountImportProductRefreshCooldown - elapsed
		response.Success(c, PublicAccountImportProductRefreshResponse{
			RetryAfterSeconds: int((retryAfter + time.Second - 1) / time.Second),
		})
		return
	}
	requestedAt := now.Format(time.RFC3339Nano)
	for _, shop := range shops {
		cached := publicProductCache.Shops[shop.ID]
		cached.ShopID = shop.ID
		cached.RefreshRequestedAt = requestedAt
		publicProductCache.Shops[shop.ID] = cached
	}
	if err := savePublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to queue product refresh")
		return
	}
	publicProductLastManualRefreshAt = now
	publicProductLastJobAt = time.Time{}
	response.Success(c, PublicAccountImportProductRefreshResponse{
		Accepted: true, QueuedShops: len(shops), RefreshingShops: len(shops),
	})
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

	updatedAt := time.Now().UTC().Format(time.RFC3339)
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
	cached.ShopID = shop.ID
	cached.Error = ""
	cached.UpdatedAt = updatedAt
	cached.RefreshRequestedAt = ""
	cached.Products = products
	publicProductCache.Shops[shop.ID] = cached
	if err := savePublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to save product cache")
		return
	}
	response.Success(c, gin.H{"accepted": len(products)})
}

func normalizePublicProductSyncItem(shop PublicAccountImportShop, item PublicAccountImportProductSyncItem, updatedAt string) (PublicAccountImportProduct, bool) {
	item.GoodsKey = strings.TrimSpace(item.GoodsKey)
	item.Name = strings.TrimSpace(item.Name)
	if item.GoodsKey == "" || len(item.GoodsKey) > 100 || item.Name == "" || len(item.Name) > 500 || item.Stock <= 0 || item.Price < 0 || item.Price > 1_000_000 || item.MarketPrice < 0 || item.MarketPrice > 1_000_000 {
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
		Price: item.Price, MarketPrice: item.MarketPrice, Stock: item.Stock, UpdatedAt: updatedAt,
	}, true
}

func snapshotPublicAccountImportShops() ([]PublicAccountImportShop, error) {
	publicAccountImportShopLinksMu.Lock()
	defer publicAccountImportShopLinksMu.Unlock()
	return loadPublicAccountImportShops(publicAccountImportShopLinksPath())
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

func publicAccountImportProductSnapshot(shops []PublicAccountImportShop, store publicAccountImportProductStore, now time.Time) ([]PublicAccountImportProduct, int, int) {
	products := make([]PublicAccountImportProduct, 0)
	pending := 0
	refreshing := 0
	for _, shop := range shops {
		cached, ok := store.Shops[shop.ID]
		if !ok {
			pending++
			continue
		}
		if !publicAccountImportProductCacheIsFresh(cached, now, publicAccountImportProductMaxCacheAge) {
			pending++
		}
		if publicAccountImportProductRefreshIsPending(cached, now) {
			refreshing++
		}
		for _, product := range cached.Products {
			if product.Stock > 0 {
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
	return products, pending, refreshing
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
	}
	candidates := make([]candidate, 0, len(shops))
	for i := range shops {
		cached := store.Shops[shops[i].ID]
		if !publicAccountImportProductRefreshIsPending(cached, now) && publicAccountImportProductCacheIsFresh(cached, now, publicAccountImportProductRefreshAge) {
			continue
		}
		attempt := parsePublicAccountImportProductTimestamp(cached.LastAttempt)
		if attempt.After(now.Add(time.Minute)) {
			attempt = time.Time{}
		}
		if !attempt.IsZero() && now.Sub(attempt) < publicAccountImportProductRetryAge {
			continue
		}
		candidates = append(candidates, candidate{shop: shops[i], attempt: attempt})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
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
	requestedAt := parsePublicAccountImportProductTimestamp(cached.RefreshRequestedAt)
	if requestedAt.IsZero() || requestedAt.After(now.Add(time.Minute)) {
		return false
	}
	return now.Sub(requestedAt) <= publicAccountImportProductRefreshMaxAge
}

func countPublicAccountImportProductRefreshes(shops []PublicAccountImportShop, store publicAccountImportProductStore, now time.Time) int {
	count := 0
	for _, shop := range shops {
		if publicAccountImportProductRefreshIsPending(store.Shops[shop.ID], now) {
			count++
		}
	}
	return count
}

func parsePublicAccountImportProductTimestamp(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return parsed
}

func loadPublicProductCacheLocked() error {
	if publicProductCacheLoaded {
		return nil
	}
	publicProductCacheLoaded = true
	data, err := os.ReadFile(publicAccountImportProductsFile)
	if errors.Is(err, os.ErrNotExist) {
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
	return nil
}

func savePublicProductCacheLocked() error {
	data, err := json.MarshalIndent(publicProductCache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(publicAccountImportProductsFile)
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
	return os.Rename(tempPath, publicAccountImportProductsFile)
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
