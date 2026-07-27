package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPublicAccountImportProductCacheIsFresh(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		updatedAt string
		want      bool
	}{
		{name: "recent cache", updatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339), want: true},
		{name: "maximum cache age", updatedAt: now.Add(-publicAccountImportProductMaxCacheAge).Format(time.RFC3339), want: true},
		{name: "stale cache", updatedAt: now.Add(-publicAccountImportProductMaxCacheAge - time.Second).Format(time.RFC3339), want: false},
		{name: "missing timestamp", want: false},
		{name: "invalid timestamp", updatedAt: "invalid", want: false},
		{name: "future timestamp", updatedAt: now.Add(2 * time.Minute).Format(time.RFC3339), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cached := publicAccountImportProductShopCache{UpdatedAt: tt.updatedAt}
			require.Equal(t, tt.want, publicAccountImportProductCacheIsFresh(cached, now, publicAccountImportProductMaxCacheAge))
		})
	}
}

func TestSupportedPublicAccountImportProductShopsFiltersNonProductLinks(t *testing.T) {
	shops := []PublicAccountImportShop{
		{ID: "supported", URL: "https://pay.ldxp.cn/shop/shop-token"},
		{ID: "other-host", URL: "https://example.com/shop/shop-token"},
		{ID: "item", URL: "https://pay.ldxp.cn/item/goods-key"},
		{ID: "missing-token", URL: "https://pay.ldxp.cn/shop/"},
	}

	supported := supportedPublicAccountImportProductShops(shops)
	require.Equal(t, []PublicAccountImportShop{shops[0]}, supported)
}

func TestPublicAccountImportProductSnapshotKeepsLastSuccessfulStaleProducts(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "stale"}, {ID: "missing"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"stale": {
			UpdatedAt:          now.Add(-time.Hour).Format(time.RFC3339),
			RefreshRequestedAt: now.Format(time.RFC3339),
			Products: []PublicAccountImportProduct{
				{ID: "available", Name: "Available", Price: 2, Stock: 1, MinimumQuantity: 1},
				{ID: "legacy", Name: "Legacy", Price: 1, Stock: 1},
				{ID: "below-minimum", Name: "Below minimum", Price: 2, Stock: 1, MinimumQuantity: 2},
				{ID: "sold-out", Name: "Sold out", Price: 3, Stock: 0},
			},
		},
	}}

	products, pending, queued, refreshing, failed := publicAccountImportProductSnapshot(shops, store, now)
	require.Equal(t, []PublicAccountImportProduct{
		{ID: "available", Name: "Available", Price: 2, Stock: 1, MinimumQuantity: 1},
		{ID: "legacy", Name: "Legacy", Price: 1, Stock: 1, MinimumQuantity: 1},
	}, products)
	require.Equal(t, 2, pending)
	require.Equal(t, 1, queued)
	require.Zero(t, refreshing)
	require.Zero(t, failed)
}

func TestNormalizePublicProductSyncItemRequiresEnoughStockForMinimumQuantity(t *testing.T) {
	shop := PublicAccountImportShop{ID: "shop", Name: "Shop", URL: "https://pay.ldxp.cn/shop/token"}
	item := PublicAccountImportProductSyncItem{
		GoodsKey: "goods", Name: "Product", URL: "https://pay.ldxp.cn/item/goods",
		GoodsType: "card", Price: 1, Stock: 50, MinimumQuantity: 50,
	}

	product, ok := normalizePublicProductSyncItem(shop, item, "2026-07-24T00:00:00Z")
	require.True(t, ok)
	require.Equal(t, 50, product.MinimumQuantity)

	item.Stock = 49
	_, ok = normalizePublicProductSyncItem(shop, item, "2026-07-24T00:00:00Z")
	require.False(t, ok)

	item.Stock = 1
	item.MinimumQuantity = 0
	product, ok = normalizePublicProductSyncItem(shop, item, "2026-07-24T00:00:00Z")
	require.True(t, ok)
	require.Equal(t, 1, product.MinimumQuantity)
}

func TestSelectPublicAccountImportProductSyncShopUsesSuccessfulRefreshAndShortLease(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{
		{ID: "fresh"},
		{ID: "leased"},
		{ID: "stale"},
		{ID: "never-synced"},
	}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"fresh": {
			UpdatedAt:   now.Add(-time.Minute).Format(time.RFC3339),
			LastAttempt: now.Add(-time.Hour).Format(time.RFC3339),
		},
		"leased": {
			UpdatedAt:   now.Add(-time.Hour).Format(time.RFC3339),
			LastAttempt: now.Add(-30 * time.Second).Format(time.RFC3339),
		},
		"stale": {
			UpdatedAt:   now.Add(-time.Hour).Format(time.RFC3339),
			LastAttempt: now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		"never-synced": {
			LastAttempt: now.Add(-10 * time.Minute).Format(time.RFC3339),
		},
	}}

	selected := selectPublicAccountImportProductSyncShop(shops, store, now)
	require.NotNil(t, selected)
	require.Equal(t, "never-synced", selected.ID)
}

func TestSelectPublicAccountImportProductSyncShopReturnsNilWhenNothingIsDue(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "fresh"}, {ID: "leased"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"fresh":  {UpdatedAt: now.Add(-time.Minute).Format(time.RFC3339)},
		"leased": {LastAttempt: now.Add(-30 * time.Second).Format(time.RFC3339)},
	}}

	require.Nil(t, selectPublicAccountImportProductSyncShop(shops, store, now))
}

func TestSelectPublicAccountImportProductSyncShopKeepsTenMinuteCacheFresh(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "ten-minutes-old"}, {ID: "stale"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"ten-minutes-old": {UpdatedAt: now.Add(-10 * time.Minute).Format(time.RFC3339Nano)},
		"stale":           {UpdatedAt: now.Add(-publicAccountImportProductRefreshAge - time.Second).Format(time.RFC3339Nano)},
	}}

	selected := selectPublicAccountImportProductSyncShops(shops, store, now, 2)
	require.Equal(t, []string{"stale"}, []string{selected[0].ID})
}

func TestSelectPublicAccountImportProductSyncShopRetriesInvalidFutureTimestamp(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "future"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"future": {
			UpdatedAt:   now.Add(2 * time.Minute).Format(time.RFC3339),
			LastAttempt: now.Add(2 * time.Minute).Format(time.RFC3339),
		},
	}}

	selected := selectPublicAccountImportProductSyncShop(shops, store, now)
	require.NotNil(t, selected)
	require.Equal(t, "future", selected.ID)
}

func TestSelectPublicAccountImportProductSyncShopsLeasesDistinctBatch(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{
		{ID: "stale"},
		{ID: "requested"},
		{ID: "leased"},
		{ID: "fresh"},
	}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"stale": {
			UpdatedAt:   now.Add(-time.Hour).Format(time.RFC3339),
			LastAttempt: now.Add(-10 * time.Minute).Format(time.RFC3339),
		},
		"requested": {
			UpdatedAt:          now.Add(-time.Minute).Format(time.RFC3339),
			LastAttempt:        now.Add(-5 * time.Minute).Format(time.RFC3339),
			RefreshRequestedAt: now.Format(time.RFC3339),
		},
		"leased": {
			UpdatedAt:   now.Add(-time.Hour).Format(time.RFC3339),
			LastAttempt: now.Add(-30 * time.Second).Format(time.RFC3339),
		},
		"fresh": {UpdatedAt: now.Add(-time.Minute).Format(time.RFC3339)},
	}}

	selected := selectPublicAccountImportProductSyncShops(shops, store, now, 2)
	require.Equal(t, []string{"requested", "stale"}, []string{selected[0].ID, selected[1].ID})
}

func TestPublicAccountImportProductRefreshStatusSeparatesInvalidAndExpiredRequests(t *testing.T) {
	shops := []PublicAccountImportShop{{ID: "pending"}, {ID: "invalid"}, {ID: "expired"}, {ID: "done"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"pending": {RefreshRequestedAt: "2026-07-14T10:00:00Z"},
		"invalid": {RefreshRequestedAt: "invalid"},
		"expired": {RefreshRequestedAt: "2026-07-14T09:30:00Z"},
		"done":    {},
	}}

	now := time.Date(2026, 7, 14, 10, 5, 0, 0, time.UTC)
	status := publicAccountImportProductRefreshStatusForShops(shops, store, now)
	require.Equal(t, 1, status.Requested)
	require.Equal(t, 1, status.Queued)
	require.Zero(t, status.Refreshing)
	require.Equal(t, 1, status.Failed)
}

func TestPublicAccountImportProductRefreshStatusHidesAutomaticSyncJobs(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "automatic"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"automatic": {
			LastAttempt:   now.Format(time.RFC3339Nano),
			SyncStartedAt: now.Format(time.RFC3339Nano),
			SyncAttemptID: "automatic-attempt",
		},
	}}

	status := publicAccountImportProductRefreshStatusForShops(shops, store, now)
	require.Zero(t, status.Requested)
	require.Zero(t, status.Queued)
	require.Zero(t, status.Refreshing)
	require.Zero(t, status.Failed)
}

func TestPublicAccountImportProductRefreshStatusSeparatesQueueFromActiveJobs(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := make([]PublicAccountImportShop, 0, 31)
	store := publicAccountImportProductStore{Shops: make(map[string]publicAccountImportProductShopCache, 31)}
	for i := 0; i < 31; i++ {
		shopID := fmt.Sprintf("shop-%02d", i)
		shops = append(shops, PublicAccountImportShop{ID: shopID})
		cached := publicAccountImportProductShopCache{RefreshRequestedAt: now.Format(time.RFC3339Nano)}
		if i < 3 {
			cached.LastAttempt = now.Format(time.RFC3339Nano)
			cached.SyncStartedAt = now.Format(time.RFC3339Nano)
			cached.SyncAttemptID = fmt.Sprintf("attempt-%d", i)
		}
		store.Shops[shopID] = cached
	}

	status := publicAccountImportProductRefreshStatusForShops(shops, store, now)
	require.Equal(t, 31, status.Requested)
	require.Equal(t, 28, status.Queued)
	require.Equal(t, 3, status.Refreshing)
	require.Zero(t, status.Failed)
}

func TestPublicAccountImportProductRefreshStatusMovesFailedJobBackToQueue(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "failed"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"failed": {
			RefreshRequestedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			SyncStartedAt:      now.Add(-30 * time.Second).Format(time.RFC3339Nano),
			Error:              "verification required",
		},
	}}

	status := publicAccountImportProductRefreshStatusForShops(shops, store, now)
	require.Equal(t, 1, status.Queued)
	require.Zero(t, status.Refreshing)
}

func TestPublicAccountImportProductRefreshStatusKeepsExpiredActiveJobRunning(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "active"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"active": {
			RefreshRequestedAt: now.Add(-publicAccountImportProductRefreshMaxAge - time.Minute).Format(time.RFC3339Nano),
			SyncStartedAt:      now.Add(-time.Minute).Format(time.RFC3339Nano),
		},
	}}

	status := publicAccountImportProductRefreshStatusForShops(shops, store, now)
	require.Equal(t, 1, status.Requested)
	require.Equal(t, 1, status.Refreshing)
	require.Zero(t, status.Queued)
	require.Zero(t, status.Failed)
}

func TestPublicAccountImportProductRefreshIsFulfilledByNewerSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	cached := publicAccountImportProductShopCache{
		RefreshRequestedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
		UpdatedAt:          now.Format(time.RFC3339Nano),
	}

	require.False(t, publicAccountImportProductRefreshIsPending(cached, now))
}

func TestSelectPublicAccountImportProductSyncShopHonorsActiveLease(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "active"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"active": {
			LastAttempt:   now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
			SyncStartedAt: now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
		},
	}}

	require.Nil(t, selectPublicAccountImportProductSyncShop(shops, store, now))
}

func TestCountPublicAccountImportProductActiveSyncsUsesValidLeasesOnly(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "one"}, {ID: "two"}, {ID: "failed"}, {ID: "expired"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"one": {SyncStartedAt: now.Format(time.RFC3339Nano)},
		"two": {SyncStartedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)},
		"failed": {
			SyncStartedAt: now.Format(time.RFC3339Nano),
			Error:         "failed",
		},
		"expired": {SyncStartedAt: now.Add(-publicAccountImportProductSyncLeaseAge - time.Second).Format(time.RFC3339Nano)},
	}}

	require.Equal(t, 2, countPublicAccountImportProductActiveSyncs(shops, store, now))
	require.Equal(t, 2, publicAccountImportProductMaxSyncJobs)
	require.Zero(t, publicAccountImportProductMaxSyncJobs-countPublicAccountImportProductActiveSyncs(shops, store, now))
}

func TestPublicAccountImportProductSingleShopRefreshFlow(t *testing.T) {
	now := time.Now().UTC()
	shops := []PublicAccountImportShop{
		{ID: "manual", Name: "Manual", URL: "https://pay.ldxp.cn/shop/manual-token"},
		{ID: "automatic", Name: "Automatic", URL: "https://pay.ldxp.cn/shop/automatic-token"},
		{ID: "unsupported", Name: "Unsupported", URL: "https://example.com/shop"},
	}
	store := publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{
		"manual": {
			ShopID:      "manual",
			UpdatedAt:   now.Add(-time.Minute).Format(time.RFC3339Nano),
			LastAttempt: now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
			Products: []PublicAccountImportProduct{
				{ID: "last-success", ShopID: "manual", Name: "Last success", Stock: 1, MinimumQuantity: 1},
			},
		},
		"automatic": {
			ShopID:      "automatic",
			UpdatedAt:   now.Add(-time.Hour).Format(time.RFC3339Nano),
			LastAttempt: now.Add(-time.Hour).Format(time.RFC3339Nano),
		},
	}}
	router := newPublicProductTestRouter(t, shops, store)

	first := performPublicShopRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "manual"})
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	firstData := decodePublicProductResponse[PublicAccountImportProductRefreshResponse](t, first.Body.Bytes())
	require.True(t, firstData.Accepted)
	require.Equal(t, "manual", firstData.ShopID)
	require.Equal(t, "queued", firstData.State)
	require.Zero(t, firstData.RetryAfterSeconds)
	publicProductCacheMu.Lock()
	firstRequestedAt := publicProductCache.Shops["manual"].RefreshRequestedAt
	require.NotEmpty(t, firstRequestedAt)
	require.Empty(t, publicProductCache.Shops["automatic"].RefreshRequestedAt)
	require.True(t, publicProductLastJobAt.IsZero())
	publicProductCacheMu.Unlock()

	duplicate := performPublicShopRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "manual"})
	require.Equal(t, http.StatusOK, duplicate.Code, duplicate.Body.String())
	duplicateData := decodePublicProductResponse[PublicAccountImportProductRefreshResponse](t, duplicate.Body.Bytes())
	require.False(t, duplicateData.Accepted)
	require.Equal(t, "queued", duplicateData.State)
	publicProductCacheMu.Lock()
	require.Equal(t, firstRequestedAt, publicProductCache.Shops["manual"].RefreshRequestedAt)
	publicProductCacheMu.Unlock()

	unknown := performPublicShopRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "missing"})
	require.Equal(t, http.StatusBadRequest, unknown.Code, unknown.Body.String())
	unsupported := performPublicShopRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "unsupported"})
	require.Equal(t, http.StatusBadRequest, unsupported.Code, unsupported.Body.String())

	list := performPublicShopRequest(t, router, http.MethodGet, "/products", nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	listData := decodePublicProductResponse[PublicAccountImportProductsResponse](t, list.Body.Bytes())
	require.Equal(t, 2, listData.ShopCount)
	require.Equal(t, 1, listData.QueuedShops)
	require.Zero(t, listData.RefreshingShops)
	require.Len(t, listData.ShopSyncStatuses, 2)
	require.Equal(t, []string{"manual", "automatic"}, []string{listData.ShopSyncStatuses[0].ShopID, listData.ShopSyncStatuses[1].ShopID})
	require.Equal(t, "queued", listData.ShopSyncStatuses[0].State)
	require.Equal(t, "idle", listData.ShopSyncStatuses[1].State)

	jobResponse := performPublicShopRequest(t, router, http.MethodGet, "/products/sync-job?limit=1", nil)
	require.Equal(t, http.StatusOK, jobResponse.Code, jobResponse.Body.String())
	jobData := decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, jobResponse.Body.Bytes())
	require.NotNil(t, jobData.Job)
	require.Equal(t, "manual", jobData.Job.ShopID)
	require.NotEmpty(t, jobData.Job.AttemptID)

	running := performPublicShopRequest(t, router, http.MethodGet, "/products", nil)
	runningData := decodePublicProductResponse[PublicAccountImportProductsResponse](t, running.Body.Bytes())
	require.Equal(t, "refreshing", runningData.ShopSyncStatuses[0].State)
	require.Equal(t, 1, runningData.RefreshingShops)

	failure := performPublicShopRequest(t, router, http.MethodPost, "/products/sync-failure", PublicAccountImportProductSyncFailureRequest{
		ShopID: "manual", AttemptID: jobData.Job.AttemptID, Error: "temporary failure",
	})
	require.Equal(t, http.StatusOK, failure.Code, failure.Body.String())
	publicProductCacheMu.Lock()
	failedCache := publicProductCache.Shops["manual"]
	require.Equal(t, "temporary failure", failedCache.Error)
	require.Equal(t, "last-success", failedCache.Products[0].ID)
	failedCache.LastAttempt = time.Now().UTC().Add(-publicAccountImportProductRetryAge - time.Second).Format(time.RFC3339Nano)
	publicProductCache.Shops["manual"] = failedCache
	publicProductLastJobAt = time.Time{}
	publicProductCacheMu.Unlock()

	retryJobResponse := performPublicShopRequest(t, router, http.MethodGet, "/products/sync-job?limit=1", nil)
	require.Equal(t, http.StatusOK, retryJobResponse.Code, retryJobResponse.Body.String())
	retryJobData := decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, retryJobResponse.Body.Bytes())
	require.NotNil(t, retryJobData.Job)
	require.Equal(t, "manual", retryJobData.Job.ShopID)
	require.NotEqual(t, jobData.Job.AttemptID, retryJobData.Job.AttemptID)

	success := performPublicShopRequest(t, router, http.MethodPost, "/products/sync", PublicAccountImportProductSyncRequest{
		ShopID: "manual", AttemptID: retryJobData.Job.AttemptID,
		Products: []PublicAccountImportProductSyncItem{{
			GoodsKey: "new-product", Name: "New product", URL: "https://pay.ldxp.cn/item/new-product",
			GoodsType: "card", Price: 1.5, Stock: 2, MinimumQuantity: 1,
		}},
	})
	require.Equal(t, http.StatusOK, success.Code, success.Body.String())
	publicProductCacheMu.Lock()
	completedCache := publicProductCache.Shops["manual"]
	require.NotEmpty(t, completedCache.ManualRefreshCompletedAt)
	require.Empty(t, completedCache.RefreshRequestedAt)
	require.Empty(t, completedCache.Error)
	require.Len(t, completedCache.Products, 1)
	publicProductCacheMu.Unlock()

	cooldown := performPublicShopRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "manual"})
	cooldownData := decodePublicProductResponse[PublicAccountImportProductRefreshResponse](t, cooldown.Body.Bytes())
	require.False(t, cooldownData.Accepted)
	require.Equal(t, "idle", cooldownData.State)
	require.Greater(t, cooldownData.RetryAfterSeconds, 0)
	require.LessOrEqual(t, cooldownData.RetryAfterSeconds, int(publicAccountImportProductRefreshCooldown.Seconds()))

	publicProductCacheMu.Lock()
	completedCache = publicProductCache.Shops["manual"]
	completedCache.ManualRefreshCompletedAt = time.Now().UTC().Add(-publicAccountImportProductRefreshCooldown - time.Second).Format(time.RFC3339Nano)
	publicProductCache.Shops["manual"] = completedCache
	publicProductCacheMu.Unlock()
	afterCooldown := performPublicShopRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "manual"})
	afterCooldownData := decodePublicProductResponse[PublicAccountImportProductRefreshResponse](t, afterCooldown.Body.Bytes())
	require.True(t, afterCooldownData.Accepted)
	require.Equal(t, "queued", afterCooldownData.State)
}

func TestPublicAccountImportProductSyncStatusAllowsExpiredRequestRetry(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	cached := publicAccountImportProductShopCache{
		UpdatedAt:          now.Add(-time.Hour).Format(time.RFC3339Nano),
		RefreshRequestedAt: now.Add(-publicAccountImportProductRefreshMaxAge - time.Second).Format(time.RFC3339Nano),
	}

	status := publicAccountImportProductSyncStatusForShop("shop", cached, now)
	require.Equal(t, "failed", status.State)
	require.Zero(t, status.RetryAfterSeconds)

	cached.SyncStartedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	status = publicAccountImportProductSyncStatusForShop("shop", cached, now)
	require.Equal(t, "refreshing", status.State)
}

func TestPublicAccountImportProductRefreshMergesRunningShopRequest(t *testing.T) {
	now := time.Now().UTC()
	shops := []PublicAccountImportShop{{ID: "running", Name: "Running", URL: "https://pay.ldxp.cn/shop/running-token"}}
	store := publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{
		"running": {
			ShopID:        "running",
			SyncStartedAt: now.Format(time.RFC3339Nano),
			SyncAttemptID: "existing-attempt",
		},
	}}
	router := newPublicProductTestRouter(t, shops, store)

	result := performPublicShopRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "running"})
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	data := decodePublicProductResponse[PublicAccountImportProductRefreshResponse](t, result.Body.Bytes())
	require.False(t, data.Accepted)
	require.Equal(t, "refreshing", data.State)
	publicProductCacheMu.Lock()
	require.Equal(t, "existing-attempt", publicProductCache.Shops["running"].SyncAttemptID)
	require.Empty(t, publicProductCache.Shops["running"].RefreshRequestedAt)
	publicProductCacheMu.Unlock()
}

func TestPublicAccountImportProductRefreshRequeuesExpiredRequest(t *testing.T) {
	now := time.Now().UTC()
	shops := []PublicAccountImportShop{{ID: "failed", Name: "Failed", URL: "https://pay.ldxp.cn/shop/failed-token"}}
	store := publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{
		"failed": {
			ShopID:             "failed",
			RefreshRequestedAt: now.Add(-publicAccountImportProductRefreshMaxAge - time.Minute).Format(time.RFC3339Nano),
			Error:              "last attempt failed",
		},
	}}
	router := newPublicProductTestRouter(t, shops, store)

	result := performPublicShopRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "failed"})
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	data := decodePublicProductResponse[PublicAccountImportProductRefreshResponse](t, result.Body.Bytes())
	require.True(t, data.Accepted)
	require.Equal(t, "queued", data.State)
	publicProductCacheMu.Lock()
	requestedAt := parsePublicAccountImportProductTimestamp(publicProductCache.Shops["failed"].RefreshRequestedAt)
	publicProductCacheMu.Unlock()
	require.WithinDuration(t, time.Now().UTC(), requestedAt, 2*time.Second)
}

func TestPublicAccountImportProductRefreshRetryAfterUsesPerShopCompletion(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	cached := publicAccountImportProductShopCache{ManualRefreshCompletedAt: now.Add(-2*time.Minute - 500*time.Millisecond).Format(time.RFC3339Nano)}
	require.Equal(t, 180, publicAccountImportProductRefreshRetryAfter(cached, now))
	require.Zero(t, publicAccountImportProductRefreshRetryAfter(cached, now.Add(3*time.Minute)))
	require.Zero(t, publicAccountImportProductRefreshRetryAfter(publicAccountImportProductShopCache{}, now))
}

func TestLoadPublicAccountImportProductStoreVersionOneWithoutManualCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "products.json")
	t.Setenv(publicAccountImportProductsFileEnv, path)
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"shops":{"legacy":{"shop_id":"legacy","last_attempt":"","updated_at":"2026-07-14T09:00:00Z","products":[]}}}`), 0o600))

	publicProductCacheMu.Lock()
	previousCache := publicProductCache
	previousLoaded := publicProductCacheLoaded
	publicProductCache = publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{}}
	publicProductCacheLoaded = false
	require.NoError(t, loadPublicProductCacheLocked())
	require.Equal(t, publicAccountImportProductStoreVersion, publicProductCache.Version)
	require.Equal(t, "2026-07-14T09:00:00Z", publicProductCache.Shops["legacy"].UpdatedAt)
	require.Empty(t, publicProductCache.Shops["legacy"].ManualRefreshCompletedAt)
	publicProductCache = previousCache
	publicProductCacheLoaded = previousLoaded
	publicProductCacheMu.Unlock()
}

func newPublicProductTestRouter(t *testing.T, shops []PublicAccountImportShop, store publicAccountImportProductStore) http.Handler {
	t.Helper()
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(publicAccountImportShopLinksFileEnv, filepath.Join(t.TempDir(), "shops.json"))
	t.Setenv(publicAccountImportProductsFileEnv, filepath.Join(t.TempDir(), "products.json"))
	require.NoError(t, savePublicAccountImportShops(publicAccountImportShopLinksPath(), shops))

	publicProductCacheMu.Lock()
	previousCache := publicProductCache
	previousLoaded := publicProductCacheLoaded
	previousLastJobAt := publicProductLastJobAt
	if store.Version == 0 {
		store.Version = publicAccountImportProductStoreVersion
	}
	if store.Shops == nil {
		store.Shops = map[string]publicAccountImportProductShopCache{}
	}
	publicProductCache = store
	publicProductCacheLoaded = true
	publicProductLastJobAt = time.Now().UTC()
	publicProductCacheMu.Unlock()
	t.Cleanup(func() {
		publicProductCacheMu.Lock()
		publicProductCache = previousCache
		publicProductCacheLoaded = previousLoaded
		publicProductLastJobAt = previousLastJobAt
		publicProductCacheMu.Unlock()
	})

	h := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/products", h.ListPublicAccountImportProducts)
	router.GET("/products/sync-job", h.GetPublicAccountImportProductSyncJob)
	router.POST("/products/refresh", h.RequestPublicAccountImportProductRefresh)
	router.POST("/products/sync", h.SubmitPublicAccountImportProductSync)
	router.POST("/products/sync-failure", h.FailPublicAccountImportProductSync)
	return router
}

func decodePublicProductResponse[T any](t *testing.T, body []byte) T {
	t.Helper()
	var payload struct {
		Data T `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &payload), string(body))
	return payload.Data
}
