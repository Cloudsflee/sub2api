package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPublicProductSyncControlAuthenticationReadOnlyAndLease(t *testing.T) {
	now := time.Now().UTC()
	shop, cached := productQuoteTestCache(t, now)
	router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for _, token := range []string{"", "Bearer wrong"} {
			r := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/products/sync-control", nil)
			req.Header.Set("Authorization", token)
			router.ServeHTTP(r, req)
			require.Equal(t, http.StatusUnauthorized, r.Code)
		}
	}
	before, err := snapshotPublicAccountImportProductStore()
	require.NoError(t, err)
	lastJob := publicProductLastJobAt
	for range 2 {
		r := performPublicProductRequest(t, router, http.MethodGet, "/products/sync-control", nil)
		require.Equal(t, http.StatusOK, r.Code)
		data := decodePublicProductResponse[PublicAccountImportSyncControlResponse](t, r.Body.Bytes())
		require.Equal(t, 1, data.Control.IntervalMultiplier)
		require.Equal(t, 1, data.ActiveShops)
		require.Zero(t, data.AutomaticDue)
		require.Equal(t, "no-store", r.Header().Get("Cache-Control"))
	}
	after, err := snapshotPublicAccountImportProductStore()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, lastJob, publicProductLastJobAt)
	_, err = os.Stat(publicAccountImportSyncControlPath())
	require.True(t, os.IsNotExist(err), "GET must not create the control file")
	put := func(data map[string]any) *httptest.ResponseRecorder {
		return performPublicProductRequest(t, router, http.MethodPut, "/products/sync-control", data)
	}
	r := put(map[string]any{"expected_revision": 0, "controller_id": "first", "lease_seconds": 180, "interval_multiplier": 2})
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	require.Equal(t, http.StatusConflict, put(map[string]any{"expected_revision": 1, "controller_id": "second", "lease_seconds": 180}).Code)
	require.Equal(t, http.StatusConflict, put(map[string]any{"expected_revision": 0, "controller_id": "first", "lease_seconds": 180}).Code)
	require.Equal(t, http.StatusBadRequest, put(map[string]any{"expected_revision": 1, "controller_id": "first", "interval_multiplier": 3}).Code)
	require.Equal(t, http.StatusBadRequest, put(map[string]any{"expected_revision": 1, "unknown": true}).Code)
	require.Equal(t, http.StatusOK, put(map[string]any{"expected_revision": 1, "controller_id": "first", "lease_seconds": 0}).Code)
	require.Equal(t, http.StatusOK, put(map[string]any{"expected_revision": 2, "controller_id": "second", "lease_seconds": 180}).Code)
	persisted, err := loadPublicAccountImportSyncControlLocked()
	require.NoError(t, err)
	require.Equal(t, 2, persisted.IntervalMultiplier)
	require.Equal(t, "second", persisted.ControllerID)
}

func TestPublicProductSyncControlSaveFailureKeepsPreviousConfiguration(t *testing.T) {
	_ = newPublicProductTestRouter(t, nil, publicAccountImportProductStore{})
	old := defaultPublicAccountImportSyncControl()
	old.IntervalMultiplier = 2
	require.NoError(t, savePublicAccountImportSyncControlLocked(old))
	oldBytes, err := os.ReadFile(publicAccountImportSyncControlPath())
	require.NoError(t, err)
	next := old
	next.IntervalMultiplier, next.AutomaticPaused = 4, true
	err = savePublicAccountImportSyncControlWithRename(next, func(string, string) error { return errors.New("disk failure") })
	require.Error(t, err)
	newBytes, err := os.ReadFile(publicAccountImportSyncControlPath())
	require.NoError(t, err)
	require.Equal(t, oldBytes, newBytes)
	loaded, err := loadPublicAccountImportSyncControlLocked()
	require.NoError(t, err)
	require.Equal(t, old, loaded)
}

func TestPublicProductSyncControlUnifiedIntervalsAndManualRules(t *testing.T) {
	now := time.Now().UTC()
	for _, trust := range []string{"trusted", "neutral", "untrusted"} {
		shop := PublicAccountImportShop{ID: "old", TrustLevel: trust}
		base := publicAccountImportProductRefreshAgeForShop(shop)
		cached := publicAccountImportProductShopCache{SchemaVersion: 2, Products: []PublicAccountImportProduct{}, UpdatedAt: now.Add(-base - time.Second).Format(time.RFC3339Nano)}
		store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{"old": cached, "new": cached}}
		shops := []PublicAccountImportShop{shop, {ID: "new", TrustLevel: trust}}
		control := defaultPublicAccountImportSyncControl()
		require.Len(t, selectPublicAccountImportProductSyncShopsWithControl(shops, store, now, 6, control), 2)
		control.IntervalMultiplier = 2
		require.Empty(t, selectPublicAccountImportProductSyncShopsWithControl(shops, store, now, 6, control))
		cached.UpdatedAt = now.Add(-base*2 - time.Second).Format(time.RFC3339Nano)
		store.Shops["old"], store.Shops["new"] = cached, cached
		require.Len(t, selectPublicAccountImportProductSyncShopsWithControl(shops, store, now, 6, control), 2)
		control.IntervalMultiplier = 4
		require.Empty(t, selectPublicAccountImportProductSyncShopsWithControl(shops, store, now, 6, control))
		// A missing first snapshot remains eligible at any multiplier.
		delete(store.Shops, "new")
		require.Equal(t, "new", selectPublicAccountImportProductSyncShopsWithControl(shops, store, now, 6, control)[0].ID)
		control.AutomaticPaused = true
		control.QuarantinedShops["old"] = "test"
		cached.RefreshRequestedAt = now.Format(time.RFC3339Nano)
		store.Shops["old"] = cached
		selected := selectPublicAccountImportProductSyncShopsWithControl(shops, store, now, 6, control)
		require.Len(t, selected, 1)
		require.Equal(t, "old", selected[0].ID)
		cached.RetryNotBeforeAt = now.Add(time.Minute).Format(time.RFC3339Nano)
		store.Shops["old"] = cached
		require.Empty(t, selectPublicAccountImportProductSyncShopsWithControl(shops, store, now, 6, control))
	}
}

func TestPublicProductSyncControlPausePreservesInflightAndFailureEvidence(t *testing.T) {
	now := time.Now().UTC()
	shop, cached := productQuoteTestCache(t, now)
	router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
	r := performPublicProductRequest(t, router, http.MethodPut, "/products/sync-control", map[string]any{
		"expected_revision": 0, "automatic_paused": true, "quarantined_shops": map[string]string{shop.ID: "test isolation"}})
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	quote := performPublicProductRequest(t, router, http.MethodPost, "/products/sync-one", productQuoteTestRequest(cached, now))
	require.Equal(t, http.StatusOK, quote.Code, quote.Body.String())
	saved := performPublicProductRequest(t, router, http.MethodPost, "/products/sync", validPublicProductSyncRequest(shop.ID, "shop-attempt", now))
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	state, err := snapshotPublicAccountImportProductStore()
	require.NoError(t, err)
	require.Equal(t, 1, state.Shops[shop.ID].SuccessCount)
	require.NotEmpty(t, state.Shops[shop.ID].FirstSyncedAt)
	publicProductLastJobAt = time.Time{}
	job := performPublicProductRequest(t, router, http.MethodGet, "/products/sync-job", nil)
	require.Nil(t, decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, job.Body.Bytes()).Job)
	// An accepted failure is counted exactly once; retries cannot erase quotes.
	cache := state.Shops[shop.ID]
	cache.SyncAttemptID, cache.SyncStartedAt, cache.SyncHeartbeatAt = "failure-attempt", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)
	publicProductCache.Shops[shop.ID] = cache
	request := PublicAccountImportProductSyncFailureRequest{ShopID: shop.ID, AttemptID: "failure-attempt", Kind: "shop_closed", Error: "closed"}
	for range 2 {
		r = performPublicProductRequest(t, router, http.MethodPost, "/products/sync-failure", request)
		require.Equal(t, http.StatusOK, r.Code)
	}
	state, err = snapshotPublicAccountImportProductStore()
	require.NoError(t, err)
	require.Equal(t, 1, state.Shops[shop.ID].FailureCount)
	require.Equal(t, 1, state.Shops[shop.ID].ConsecutiveFailures)
	require.Equal(t, "shop_closed", state.Shops[shop.ID].LastFailureKind)
	require.Equal(t, cache.Products, state.Shops[shop.ID].Products)
}

func TestPublicShopPlatformIdentityPreservesExistingRecord(t *testing.T) {
	shop := PublicAccountImportShop{ID: "legacy-id", Name: "Preserve", URL: "https://pay.ldxp.cn/shop/EE8LWO61/category", TrustLevel: "trusted"}
	_ = newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{})
	router := gin.New()
	router.POST("/shops", (&AccountHandler{}).SubmitPublicAccountImportShop)
	for _, url := range []string{"https://wzyp.cn/shop/EE8LWO61", "http://pay.ldxp.cn/shop/EE8LWO61/another?q=1"} {
		r := performPublicShopRequest(t, router, http.MethodPost, "/shops", PublicAccountImportShopRequest{Name: "Overwrite", URL: url})
		require.Equal(t, http.StatusOK, r.Code)
		result := decodePublicProductResponse[PublicAccountImportShopSubmission](t, r.Body.Bytes())
		require.False(t, result.Created)
		require.Equal(t, shop, result.Shop)
	}
}
