package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPublicAccountImportShopsSubmitDeduplicatesAndPersists(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	storePath := filepath.Join(t.TempDir(), "shops.json")
	t.Setenv(publicAccountImportShopLinksFileEnv, storePath)

	h := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/shops", h.ListPublicAccountImportShops)
	router.POST("/shops", h.SubmitPublicAccountImportShop)

	first := performPublicShopRequest(t, router, http.MethodPost, `/shops`, PublicAccountImportShopRequest{
		Name: "  Example   Shop  ",
		URL:  "HTTPS://Example.COM:443/store?b=2&a=1#offer",
	})
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	var firstPayload struct {
		Data PublicAccountImportShopSubmission `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstPayload))
	require.True(t, firstPayload.Data.Created)
	require.Equal(t, "Example Shop", firstPayload.Data.Shop.Name)
	require.Equal(t, "https://example.com/store?a=1&b=2", firstPayload.Data.Shop.URL)
	require.Equal(t, publicAccountImportShopNeutral, firstPayload.Data.Shop.TrustLevel)

	duplicate := performPublicShopRequest(t, router, http.MethodPost, `/shops`, PublicAccountImportShopRequest{
		Name: "Different name",
		URL:  "https://example.com:443/store?a=1&b=2",
	})
	require.Equal(t, http.StatusOK, duplicate.Code, duplicate.Body.String())
	var duplicatePayload struct {
		Data PublicAccountImportShopSubmission `json:"data"`
	}
	require.NoError(t, json.Unmarshal(duplicate.Body.Bytes(), &duplicatePayload))
	require.False(t, duplicatePayload.Data.Created)
	require.Equal(t, firstPayload.Data.Shop, duplicatePayload.Data.Shop)

	list := performPublicShopRequest(t, router, http.MethodGet, `/shops`, nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var listPayload struct {
		Data PublicAccountImportShopsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listPayload))
	require.Equal(t, []PublicAccountImportShop{firstPayload.Data.Shop}, listPayload.Data.Shops)

	data, err := os.ReadFile(storePath)
	require.NoError(t, err)
	require.True(t, json.Valid(data))
	require.Equal(t, os.FileMode(0o600), requireFileMode(t, storePath))
}

func TestLoadPublicAccountImportShopsDefaultsMissingAndUnknownTrustLevels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shops.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "version": 1,
  "shops": [
    {"id":"missing","name":"Missing","url":"https://example.com/missing","created_at":"2026-07-31T00:00:00Z"},
    {"id":"unknown","name":"Unknown","url":"https://example.com/unknown","created_at":"2026-07-31T00:00:00Z","trust_level":"vip"},
    {"id":"trusted","name":"Trusted","url":"https://example.com/trusted","created_at":"2026-07-31T00:00:00Z","trust_level":"trusted"}
  ]
}`), 0o600))

	shops, err := loadPublicAccountImportShops(path)
	require.NoError(t, err)
	require.Equal(t, []string{publicAccountImportShopNeutral, publicAccountImportShopNeutral, publicAccountImportShopTrusted}, []string{
		shops[0].TrustLevel, shops[1].TrustLevel, shops[2].TrustLevel,
	})
}

func TestUpdatePublicAccountImportShopTrustLevelValidatesAndPersists(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	path := filepath.Join(t.TempDir(), "shops.json")
	t.Setenv(publicAccountImportShopLinksFileEnv, path)
	shop := PublicAccountImportShop{ID: "shop", Name: "Shop", URL: "https://example.com/shop", TrustLevel: publicAccountImportShopNeutral}
	require.NoError(t, savePublicAccountImportShops(path, []PublicAccountImportShop{shop}))

	h := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PATCH("/shops/:id", h.UpdatePublicAccountImportShopTrustLevel)

	updated := performPublicShopRequest(t, router, http.MethodPatch, "/shops/shop", PublicAccountImportShopTrustLevelRequest{TrustLevel: publicAccountImportShopTrusted})
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	var payload struct {
		Data PublicAccountImportShop `json:"data"`
	}
	require.NoError(t, json.Unmarshal(updated.Body.Bytes(), &payload))
	require.Equal(t, publicAccountImportShopTrusted, payload.Data.TrustLevel)
	persisted, err := loadPublicAccountImportShops(path)
	require.NoError(t, err)
	require.Equal(t, publicAccountImportShopTrusted, persisted[0].TrustLevel)

	invalid := performPublicShopRequest(t, router, http.MethodPatch, "/shops/shop", PublicAccountImportShopTrustLevelRequest{TrustLevel: "vip"})
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	missing := performPublicShopRequest(t, router, http.MethodPatch, "/shops/missing", PublicAccountImportShopTrustLevelRequest{TrustLevel: publicAccountImportShopUntrusted})
	require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
}

func TestDeletePublicAccountImportShopClearsLeaseAndDoesNotReuseProducts(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(publicAccountImportProductSyncTokenEnv, "worker-token")
	shopPath := filepath.Join(t.TempDir(), "shops.json")
	productPath := filepath.Join(t.TempDir(), "products.json")
	t.Setenv(publicAccountImportShopLinksFileEnv, shopPath)
	t.Setenv(publicAccountImportProductsFileEnv, productPath)

	shopURL := "https://pay.ldxp.cn/shop/delete-token"
	shop := PublicAccountImportShop{ID: publicAccountImportShopID(shopURL), Name: "Delete", URL: shopURL, TrustLevel: publicAccountImportShopNeutral}
	require.NoError(t, savePublicAccountImportShops(shopPath, []PublicAccountImportShop{shop}))
	now := time.Now().UTC()
	setPublicProductCacheForShopTest(t, publicAccountImportProductStore{
		Version: publicAccountImportProductStoreVersion,
		Shops: map[string]publicAccountImportProductShopCache{
			shop.ID: {
				ShopID: shop.ID, SyncStartedAt: now.Format(time.RFC3339Nano), SyncHeartbeatAt: now.Format(time.RFC3339Nano), SyncAttemptID: "attempt",
				UpdatedAt: now.Format(time.RFC3339Nano), Products: []PublicAccountImportProduct{{ID: "old-product", ShopID: shop.ID}},
			},
		},
	})

	h := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.DELETE("/shops/:id", h.DeletePublicAccountImportShop)
	router.POST("/shops", h.SubmitPublicAccountImportShop)
	router.POST("/products/sync", h.SubmitPublicAccountImportProductSync)
	router.POST("/products/sync-heartbeat", h.HeartbeatPublicAccountImportProductSync)

	deleted := performPublicShopRequest(t, router, http.MethodDelete, "/shops/"+shop.ID, nil)
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	var deletedPayload struct {
		Data PublicAccountImportShopDeletion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(deleted.Body.Bytes(), &deletedPayload))
	require.Equal(t, shop.ID, deletedPayload.Data.ID)

	shops, err := loadPublicAccountImportShops(shopPath)
	require.NoError(t, err)
	require.Empty(t, shops)
	publicProductCacheMu.RLock()
	_, cacheExists := publicProductCache.Shops[shop.ID]
	publicProductCacheMu.RUnlock()
	require.False(t, cacheExists)

	heartbeat := performAuthorizedPublicShopRequest(t, router, http.MethodPost, "/products/sync-heartbeat", PublicAccountImportProductSyncHeartbeatRequest{ShopID: shop.ID, AttemptID: "attempt"})
	require.Equal(t, http.StatusConflict, heartbeat.Code, heartbeat.Body.String())
	submit := performAuthorizedPublicShopRequest(t, router, http.MethodPost, "/products/sync", validPublicProductSyncRequest(shop.ID, "attempt", now))
	require.Equal(t, http.StatusBadRequest, submit.Code, submit.Body.String())

	readded := performPublicShopRequest(t, router, http.MethodPost, "/shops", PublicAccountImportShopRequest{Name: shop.Name, URL: shop.URL})
	require.Equal(t, http.StatusCreated, readded.Code, readded.Body.String())
	var readdedPayload struct {
		Data PublicAccountImportShopSubmission `json:"data"`
	}
	require.NoError(t, json.Unmarshal(readded.Body.Bytes(), &readdedPayload))
	require.Equal(t, shop.ID, readdedPayload.Data.Shop.ID)
	publicProductCacheMu.RLock()
	_, cacheExists = publicProductCache.Shops[shop.ID]
	store := clonePublicAccountImportProductStore(publicProductCache)
	publicProductCacheMu.RUnlock()
	require.False(t, cacheExists)
	require.Equal(t, shop.ID, selectPublicAccountImportProductSyncShop([]PublicAccountImportShop{readdedPayload.Data.Shop}, store, time.Now().UTC()).ID)
}

func TestDeletePublicAccountImportShopRestoresProductsWhenShopSaveFails(t *testing.T) {
	shopPath := filepath.Join(t.TempDir(), "shops.json")
	productPath := filepath.Join(t.TempDir(), "products.json")
	t.Setenv(publicAccountImportShopLinksFileEnv, shopPath)
	t.Setenv(publicAccountImportProductsFileEnv, productPath)
	shop := PublicAccountImportShop{ID: "shop", Name: "Shop", URL: "https://pay.ldxp.cn/shop/token", TrustLevel: publicAccountImportShopNeutral}
	require.NoError(t, savePublicAccountImportShops(shopPath, []PublicAccountImportShop{shop}))
	setPublicProductCacheForShopTest(t, publicAccountImportProductStore{
		Version: publicAccountImportProductStoreVersion,
		Shops: map[string]publicAccountImportProductShopCache{
			shop.ID: {ShopID: shop.ID, SyncAttemptID: "attempt", Products: []PublicAccountImportProduct{{ID: "product"}}},
		},
	})

	deleted, err := deletePublicAccountImportShopWithPersistence(
		shop.ID,
		func(string, []PublicAccountImportShop) error { return errors.New("shop save failed") },
		savePublicProductCacheLocked,
	)
	require.True(t, deleted)
	require.ErrorContains(t, err, "shop save failed")
	publicProductCacheMu.RLock()
	restored := publicProductCache.Shops[shop.ID]
	publicProductCacheMu.RUnlock()
	require.Equal(t, "product", restored.Products[0].ID)

	data, err := os.ReadFile(productPath)
	require.NoError(t, err)
	var persisted publicAccountImportProductStore
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.Equal(t, "product", persisted.Shops[shop.ID].Products[0].ID)
	shops, err := loadPublicAccountImportShops(shopPath)
	require.NoError(t, err)
	require.Equal(t, []PublicAccountImportShop{shop}, shops)
}

func TestPublicAccountImportShopsRejectsUnsafeURLs(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(publicAccountImportShopLinksFileEnv, filepath.Join(t.TempDir(), "shops.json"))

	h := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/shops", h.SubmitPublicAccountImportShop)

	for _, shopURL := range []string{
		"javascript:alert(1)",
		"https://user:password@example.com/store",
		"https://example.com\\@attacker.test/store",
	} {
		recorder := performPublicShopRequest(t, router, http.MethodPost, `/shops`, PublicAccountImportShopRequest{
			Name: "Unsafe",
			URL:  shopURL,
		})
		require.Equal(t, http.StatusBadRequest, recorder.Code, shopURL)
	}
}

func performPublicShopRequest(t *testing.T, router http.Handler, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		require.NoError(t, err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func performAuthorizedPublicShopRequest(t *testing.T, router http.Handler, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+os.Getenv(publicAccountImportProductSyncTokenEnv))
	router.ServeHTTP(recorder, request)
	return recorder
}

func setPublicProductCacheForShopTest(t *testing.T, store publicAccountImportProductStore) {
	t.Helper()
	publicProductCacheMu.Lock()
	previousCache := publicProductCache
	previousLoaded := publicProductCacheLoaded
	previousLastJobAt := publicProductLastJobAt
	publicProductCache = store
	publicProductCacheLoaded = true
	err := savePublicProductCacheLocked()
	publicProductCacheMu.Unlock()
	require.NoError(t, err)
	t.Cleanup(func() {
		publicProductCacheMu.Lock()
		publicProductCache = previousCache
		publicProductCacheLoaded = previousLoaded
		publicProductLastJobAt = previousLastJobAt
		publicProductCacheMu.Unlock()
	})
}

func requireFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Mode().Perm()
}
