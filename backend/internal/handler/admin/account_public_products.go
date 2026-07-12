package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

const (
	publicAccountImportProductsFile        = "/app/data/public-account-import-products.json"
	publicAccountImportProductStoreVersion = 1
	publicAccountImportProductSyncInterval = 10 * time.Second
	publicAccountImportProductRefreshAge   = 5 * time.Minute
	publicAccountImportProductPageSize     = 100
	publicAccountImportProductMaxPages     = 10
	publicAccountImportProductMaxBody      = 8 << 20
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
	Products       []PublicAccountImportProduct `json:"products"`
	ShopCount      int                          `json:"shop_count"`
	PendingShops   int                          `json:"pending_shops"`
	RefreshSeconds int                          `json:"refresh_seconds"`
}

type publicAccountImportProductShopCache struct {
	ShopID      string                       `json:"shop_id"`
	LastAttempt string                       `json:"last_attempt"`
	UpdatedAt   string                       `json:"updated_at,omitempty"`
	Error       string                       `json:"error,omitempty"`
	Products    []PublicAccountImportProduct `json:"products"`
}

type publicAccountImportProductStore struct {
	Version int                                            `json:"version"`
	Shops   map[string]publicAccountImportProductShopCache `json:"shops"`
}

type publicShopInfoResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ArticleCount  int `json:"article_count"`
		CardCount     int `json:"card_count"`
		ResourceCount int `json:"resource_count"`
		EquityCount   int `json:"equity_count"`
	} `json:"data"`
}

type publicShopGoodsResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Total int `json:"total"`
		List  []struct {
			Link        string  `json:"link"`
			GoodsType   string  `json:"goods_type"`
			GoodsKey    string  `json:"goods_key"`
			Name        string  `json:"name"`
			Price       float64 `json:"price"`
			MarketPrice float64 `json:"market_price"`
			Image       string  `json:"image"`
			Category    struct {
				Name string `json:"name"`
			} `json:"category"`
			Extend struct {
				StockCount int `json:"stock_count"`
			} `json:"extend"`
		} `json:"list"`
	} `json:"data"`
}

var (
	publicProductCacheMu     sync.Mutex
	publicProductCacheLoaded bool
	publicProductCache       = publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{}}
	publicProductSyncOnce    sync.Once
	publicProductHTTPClient  = &http.Client{Timeout: 12 * time.Second}
)

func (h *AccountHandler) ListPublicAccountImportProducts(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	startPublicAccountImportProductSync()

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

	products := make([]PublicAccountImportProduct, 0)
	pending := 0
	for _, shop := range shops {
		cached, ok := store.Shops[shop.ID]
		if !ok || cached.UpdatedAt == "" {
			pending++
			continue
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
	response.Success(c, PublicAccountImportProductsResponse{
		Products: products, ShopCount: len(shops), PendingShops: pending,
		RefreshSeconds: int(publicAccountImportProductRefreshAge.Seconds()),
	})
}

func startPublicAccountImportProductSync() {
	publicProductSyncOnce.Do(func() {
		go func() {
			publicAccountImportSyncOneShop(context.Background())
			ticker := time.NewTicker(publicAccountImportProductSyncInterval)
			defer ticker.Stop()
			for range ticker.C {
				publicAccountImportSyncOneShop(context.Background())
			}
		}()
	})
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

func publicAccountImportSyncOneShop(ctx context.Context) {
	shops, err := snapshotPublicAccountImportShops()
	if err != nil || len(shops) == 0 {
		return
	}
	publicProductCacheMu.Lock()
	if err := loadPublicProductCacheLocked(); err != nil {
		publicProductCacheMu.Unlock()
		return
	}
	now := time.Now().UTC()
	var selected *PublicAccountImportShop
	var selectedAttempt time.Time
	for i := range shops {
		cached := publicProductCache.Shops[shops[i].ID]
		attempt, _ := time.Parse(time.RFC3339, cached.LastAttempt)
		if !attempt.IsZero() && now.Sub(attempt) < publicAccountImportProductRefreshAge {
			continue
		}
		if selected == nil || attempt.Before(selectedAttempt) {
			shop := shops[i]
			selected = &shop
			selectedAttempt = attempt
		}
	}
	if selected == nil {
		publicProductCacheMu.Unlock()
		return
	}
	cached := publicProductCache.Shops[selected.ID]
	cached.ShopID = selected.ID
	cached.LastAttempt = now.Format(time.RFC3339)
	publicProductCache.Shops[selected.ID] = cached
	_ = savePublicProductCacheLocked()
	publicProductCacheMu.Unlock()

	products, fetchErr := fetchPublicAccountImportShopProducts(ctx, *selected)
	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	cached = publicProductCache.Shops[selected.ID]
	if fetchErr != nil {
		cached.Error = fetchErr.Error()
	} else {
		cached.Error = ""
		cached.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		cached.Products = products
	}
	publicProductCache.Shops[selected.ID] = cached
	_ = savePublicProductCacheLocked()
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
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, publicAccountImportProductsFile)
}

func fetchPublicAccountImportShopProducts(ctx context.Context, shop PublicAccountImportShop) ([]PublicAccountImportProduct, error) {
	token, err := publicAccountImportShopToken(shop.URL)
	if err != nil {
		return nil, err
	}
	var info publicShopInfoResponse
	if err := postPublicShopAPI(ctx, "/shopApi/Shop/info", map[string]any{"token": token, "category_key": nil}, &info); err != nil {
		return nil, err
	}
	if info.Code != 1 {
		return nil, fmt.Errorf("shop info failed: %s", info.Msg)
	}
	types := []struct {
		name  string
		count int
	}{{"card", info.Data.CardCount}, {"article", info.Data.ArticleCount}, {"resource", info.Data.ResourceCount}, {"equity", info.Data.EquityCount}}
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	products := make([]PublicAccountImportProduct, 0)
	for _, goodsType := range types {
		if goodsType.count <= 0 {
			continue
		}
		for page := 1; page <= publicAccountImportProductMaxPages; page++ {
			var goods publicShopGoodsResponse
			payload := map[string]any{"token": token, "keywords": "", "category_id": 0, "goods_type": goodsType.name, "current": page, "pageSize": publicAccountImportProductPageSize}
			if err := postPublicShopAPI(ctx, "/shopApi/Shop/goodsList", payload, &goods); err != nil {
				return nil, err
			}
			if goods.Code != 1 {
				return nil, fmt.Errorf("goods list failed: %s", goods.Msg)
			}
			for _, item := range goods.Data.List {
				if item.Extend.StockCount <= 0 || strings.TrimSpace(item.Name) == "" {
					continue
				}
				products = append(products, PublicAccountImportProduct{
					ID: publicAccountImportProductID(shop.ID, item.GoodsKey), ShopID: shop.ID,
					ShopName: shop.Name, ShopURL: shop.URL, Name: strings.TrimSpace(item.Name),
					URL: item.Link, Image: item.Image, Category: item.Category.Name,
					GoodsType: item.GoodsType, Price: item.Price, MarketPrice: item.MarketPrice,
					Stock: item.Extend.StockCount, UpdatedAt: updatedAt,
				})
			}
			if len(goods.Data.List) < publicAccountImportProductPageSize || page*publicAccountImportProductPageSize >= goods.Data.Total {
				break
			}
		}
	}
	return products, nil
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

func postPublicShopAPI(ctx context.Context, endpoint string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://pay.ldxp.cn"+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Visitorid", "sub2api-public-product-catalog")
	req.Header.Set("User-Agent", "Sub2API-Public-Catalog/1.0")
	resp, err := publicProductHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("shop API temporarily limited access: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("shop API returned HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, publicAccountImportProductMaxBody+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(data) > publicAccountImportProductMaxBody {
		return errors.New("shop API response is too large")
	}
	return json.Unmarshal(data, target)
}

func publicAccountImportProductID(shopID, goodsKey string) string {
	sum := sha256.Sum256([]byte(shopID + ":" + goodsKey))
	return hex.EncodeToString(sum[:12])
}
