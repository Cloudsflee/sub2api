package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func productQuoteTestCache(t *testing.T, now time.Time) (PublicAccountImportShop, publicAccountImportProductShopCache) {
	t.Helper()
	shop := PublicAccountImportShop{ID: "shop", Name: "Shop", URL: "https://wzyp.cn/shop/token"}
	updatedAt := now.Add(-48 * time.Hour)
	item := validPublicProductSyncRequest(shop.ID, "shop-attempt", updatedAt).Products[0]
	product, err := normalizePublicProductSyncItem(shop, item, updatedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)
	return shop, publicAccountImportProductShopCache{
		ShopID: shop.ID, SchemaVersion: publicAccountImportProductSchemaVersion,
		SourceProductCount: 1, SellableProductCount: 1, UpdatedAt: product.UpdatedAt,
		LastAttempt: now.Format(time.RFC3339Nano), SyncStartedAt: now.Format(time.RFC3339Nano),
		SyncHeartbeatAt: now.Format(time.RFC3339Nano), SyncAttemptID: "shop-attempt",
		Products: []PublicAccountImportProduct{product},
		ProductRefreshes: map[string]publicAccountImportProductRefreshRecord{
			product.ID: {
				ProductID: product.ID, GoodsKey: item.GoodsKey, State: "running",
				RequestedAt: now.Format(time.RFC3339Nano), SyncStartedAt: now.Format(time.RFC3339Nano),
				SyncHeartbeatAt: now.Format(time.RFC3339Nano), SyncAttemptID: "product-attempt",
			},
		},
	}
}

func productQuoteTestRequest(cached publicAccountImportProductShopCache, at time.Time) PublicAccountImportProductSyncOneRequest {
	item := validPublicProductSyncRequest(cached.ShopID, "", at).Products[0]
	item.Price, item.PayablePrice, item.MarketPrice = float64Pointer(11), float64Pointer(20), nil
	stock, minimum := 12, 4
	item.Stock, item.MinimumQuantity, item.GoodsType = &stock, &minimum, "resource"
	item.Name, item.Image, item.Category = "Ignore quote metadata", "https://qn.ldxp.cn/quote.png", "Ignore"
	return PublicAccountImportProductSyncOneRequest{
		Scope: "product", ShopID: cached.ShopID, ProductID: cached.Products[0].ID,
		AttemptID: "product-attempt", Product: item,
	}
}

func readProductQuoteTestStatus(t *testing.T, router http.Handler, productID string) PublicAccountImportProductRefreshOneStatusResponse {
	t.Helper()
	response := performPublicProductRequest(t, router, http.MethodGet, "/products/refresh-one/status?shop_id=shop&product_id="+productID, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	data := decodePublicProductResponse[map[string]json.RawMessage](t, response.Body.Bytes())
	require.Contains(t, data, "product", "new status responses must distinguish null from a missing field")
	return decodePublicProductResponse[PublicAccountImportProductRefreshOneStatusResponse](t, response.Body.Bytes())
}

func TestPublicProductSingleQuotePersistsWithoutRenewingCatalog(t *testing.T) {
	for _, market := range []*float64{nil, float64Pointer(0), float64Pointer(30)} {
		t.Run("market", func(t *testing.T) {
			now := time.Now().UTC()
			shop, cached := productQuoteTestCache(t, now)
			router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
			t.Setenv(publicAccountImportProductStrictModeEnv, "true")
			before, err := snapshotPublicAccountImportProductStore()
			require.NoError(t, err)
			beforeStatus := publicAccountImportProductSyncStatusForShop(shop, cached, now)
			req := productQuoteTestRequest(cached, now)
			req.Product.MarketPrice = market
			response := performPublicProductRequest(t, router, http.MethodPost, "/products/sync-one", req)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			status := readProductQuoteTestStatus(t, router, req.ProductID)
			require.Equal(t, "succeeded", status.State)
			require.NotNil(t, status.Product)
			expected := cached.Products[0]
			expected.Price, expected.PayablePrice, expected.UnitPrice = 11, float64Pointer(20), float64Pointer(5)
			expected.Stock, expected.MinimumQuantity, expected.GoodsType, expected.QuoteVerifiedAt = 12, 4, "resource", req.Product.QuoteVerifiedAt
			if market != nil {
				expected.MarketPrice = *market
			}
			require.Equal(t, expected, *status.Product)
			list := performPublicProductRequest(t, router, http.MethodGet, "/products", nil)
			require.Equal(t, http.StatusOK, list.Code, list.Body.String())
			catalog := decodePublicProductResponse[PublicAccountImportProductsResponse](t, list.Body.Bytes())
			require.Equal(t, []PublicAccountImportProduct{expected}, catalog.Products)
			require.Equal(t, beforeStatus.SnapshotUpdatedAt, catalog.ShopSyncStatuses[0].SnapshotUpdatedAt)
			require.Equal(t, beforeStatus.SnapshotExpiresAt, catalog.ShopSyncStatuses[0].SnapshotExpiresAt)
			require.Equal(t, "expired", catalog.ShopSyncStatuses[0].SnapshotState)
			// A previously read snapshot must retain its own products and refresh map.
			require.Equal(t, cached, before.Shops[shop.ID])
			publicProductCacheMu.Lock()
			publicProductCacheLoaded = false
			publicProductCacheMu.Unlock()
			reloaded, err := snapshotPublicAccountImportProductStore()
			require.NoError(t, err)
			actual := reloaded.Shops[shop.ID]
			require.Equal(t, expected, actual.Products[0])
			require.Equal(t, "succeeded", actual.ProductRefreshes[req.ProductID].State)
			require.Empty(t, actual.ProductRefreshes[req.ProductID].SyncAttemptID)
			actual.Products, actual.ProductRefreshes = cached.Products, cached.ProductRefreshes
			require.Equal(t, cached, actual, "single quotes must not alter shop sync metadata or counts")
		})
	}
}

func TestPublicProductQuoteCommitsKeepNewestQuoteAndNewDirectory(t *testing.T) {
	for _, order := range []string{"shop-first", "product-first"} {
		for _, newer := range []string{"shop", "product", "equal"} {
			t.Run(order+"/"+newer, func(t *testing.T) {
				now := time.Now().UTC()
				shop, cached := productQuoteTestCache(t, now)
				// An old product missing from the incoming directory must stay removed.
				removed := cached.Products[0]
				removed.ID, removed.URL = "removed", "https://wzyp.cn/item/removed"
				removed.QuoteVerifiedAt = now.Format(time.RFC3339Nano)
				cached.Products = append(cached.Products, removed)
				cached.SourceProductCount, cached.SellableProductCount = 2, 2
				router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
				shopTime, productTime := now.Add(-time.Minute), now.Add(-time.Minute)
				if newer == "shop" {
					productTime = productTime.Add(-time.Minute)
				}
				if newer == "product" {
					shopTime = shopTime.Add(-time.Minute)
				}
				full := validPublicProductSyncRequest(shop.ID, "shop-attempt", shopTime)
				full.Products[0].Name, full.Products[0].Image, full.Products[0].Category = "New directory name", "https://qn.ldxp.cn/new.png", "New category"
				one := productQuoteTestRequest(cached, productTime)
				one.Product.MarketPrice = float64Pointer(40)
				submit := func(path string, payload any) {
					response := performPublicProductRequest(t, router, http.MethodPost, path, payload)
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				}
				if order == "shop-first" {
					submit("/products/sync", full)
					submit("/products/sync-one", one)
				} else {
					submit("/products/sync-one", one)
					submit("/products/sync", full)
				}
				store, err := snapshotPublicAccountImportProductStore()
				require.NoError(t, err)
				actual := store.Shops[shop.ID]
				require.Len(t, actual.Products, 1)
				expected, err := normalizePublicProductSyncItem(shop, full.Products[0], actual.UpdatedAt)
				require.NoError(t, err)
				if newer == "product" || (newer == "equal" && order == "product-first") {
					expected.Price, expected.MarketPrice, expected.PayablePrice, expected.UnitPrice = 11, 40, float64Pointer(20), float64Pointer(5)
					expected.Stock, expected.MinimumQuantity, expected.GoodsType, expected.QuoteVerifiedAt = 12, 4, "resource", one.Product.QuoteVerifiedAt
				}
				require.Equal(t, expected, actual.Products[0])
				status := readProductQuoteTestStatus(t, router, one.ProductID)
				require.Equal(t, "succeeded", status.State, "an older quote still completes its task")
				require.Equal(t, &expected, status.Product)
			})
		}
	}
}

func TestPublicProductQuoteCannotRestoreDeletedOrChangedProduct(t *testing.T) {
	for _, scenario := range []string{"deleted-before", "deleted-after", "identity-changed"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC()
			shop, cached := productQuoteTestCache(t, now)
			one := productQuoteTestRequest(cached, now)
			if scenario == "identity-changed" {
				cached.Products[0].URL = "https://wzyp.cn/item/replaced"
			}
			router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
			remove := func() {
				full := validPublicProductSyncRequest(shop.ID, "shop-attempt", now)
				*full.SellableProductCount, *full.UnavailableProductCount = 0, 1
				full.Products = []PublicAccountImportProductSyncItem{}
				response := performPublicProductRequest(t, router, http.MethodPost, "/products/sync", full)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			}
			if scenario == "deleted-before" {
				remove()
			}
			response := performPublicProductRequest(t, router, http.MethodPost, "/products/sync-one", one)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			if scenario == "deleted-after" {
				remove()
			}
			status := readProductQuoteTestStatus(t, router, one.ProductID)
			require.Equal(t, "superseded", status.State)
			require.Nil(t, status.Product)
			store, err := snapshotPublicAccountImportProductStore()
			require.NoError(t, err)
			if scenario == "identity-changed" {
				require.Equal(t, cached.Products, store.Shops[shop.ID].Products)
			} else {
				require.Empty(t, store.Shops[shop.ID].Products)
			}
		})
	}
}

func TestPublicProductQuoteRejectsInvalidSubmissionTimes(t *testing.T) {
	for _, age := range []time.Duration{-31 * time.Minute, 2 * time.Minute} {
		t.Run(age.String(), func(t *testing.T) {
			now := time.Now().UTC()
			shop, cached := productQuoteTestCache(t, now)
			router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
			response := performPublicProductRequest(t, router, http.MethodPost, "/products/sync-one", productQuoteTestRequest(cached, now.Add(age)))
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			store, err := snapshotPublicAccountImportProductStore()
			require.NoError(t, err)
			require.Equal(t, cached, store.Shops[shop.ID])
		})
	}
}

func TestPublicProductRefreshPersistenceFailureRollsBack(t *testing.T) {
	for _, operation := range []string{"quote", "superseded", "queue", "lease", "heartbeat", "unavailable"} {
		t.Run(operation, func(t *testing.T) {
			now := time.Now().UTC()
			shop, cached := productQuoteTestCache(t, now)
			req := productQuoteTestRequest(cached, now)
			method, path, payload := http.MethodPost, "/products/sync-one", any(req)
			switch operation {
			case "superseded":
				cached.Products = []PublicAccountImportProduct{}
				cached.SellableProductCount, cached.UnavailableProductCount = 0, 1
			case "queue", "lease":
				rec := cached.ProductRefreshes[req.ProductID]
				rec.State, rec.SyncAttemptID = "idle", ""
				path, payload = "/products/refresh-one", PublicAccountImportProductRefreshOneRequest{ShopID: shop.ID, ProductID: req.ProductID}
				if operation == "lease" {
					rec.State = "queued"
					method, path, payload = http.MethodGet, "/products/sync-job", nil
				}
				cached.ProductRefreshes[req.ProductID] = rec
			case "heartbeat":
				path, payload = "/products/sync-heartbeat", PublicAccountImportProductSyncHeartbeatRequest{Scope: "product", ShopID: shop.ID, ProductID: req.ProductID, AttemptID: req.AttemptID}
			case "unavailable":
				// Deleting the first element must not overwrite the original backing slice.
				second := cached.Products[0]
				second.ID = "second"
				cached.Products = append(cached.Products, second)
				cached.SourceProductCount, cached.SellableProductCount = 2, 2
				path, payload = "/products/sync-failure", PublicAccountImportProductSyncFailureRequest{Scope: "product", ShopID: shop.ID, ProductID: req.ProductID, AttemptID: req.AttemptID, Kind: "unavailable"}
			}
			router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
			publicProductCacheMu.Lock()
			err := savePublicProductCacheLocked()
			publicProductCacheMu.Unlock()
			require.NoError(t, err)
			originalPath := publicAccountImportProductsPath()
			originalFile, err := os.ReadFile(originalPath)
			require.NoError(t, err)
			before, err := snapshotPublicAccountImportProductStore()
			require.NoError(t, err)
			// Renaming the temporary file over a directory fails on Windows and Unix.
			t.Setenv(publicAccountImportProductsFileEnv, t.TempDir())
			response := performPublicProductRequest(t, router, method, path, payload)
			require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
			after, err := snapshotPublicAccountImportProductStore()
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, cached, after.Shops[shop.ID])
			persisted, err := os.ReadFile(originalPath)
			require.NoError(t, err)
			require.Equal(t, originalFile, persisted)
			t.Setenv(publicAccountImportProductsFileEnv, originalPath)
			response = performPublicProductRequest(t, router, method, path, payload)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		})
	}
}
