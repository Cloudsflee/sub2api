package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
		{ID: "category", URL: "https://pay.ldxp.cn/shop/7HZ37ZCG/g47fr5"},
		{ID: "other-host", URL: "https://example.com/shop/shop-token"},
		{ID: "item", URL: "https://pay.ldxp.cn/item/goods-key"},
		{ID: "missing-token", URL: "https://pay.ldxp.cn/shop/"},
		{ID: "too-deep", URL: "https://pay.ldxp.cn/shop/shop-token/category/extra"},
		{ID: "encoded-slash", URL: "https://pay.ldxp.cn/shop/shop-token%2Fextra"},
	}

	supported := supportedPublicAccountImportProductShops(shops)
	require.Equal(t, []PublicAccountImportShop{shops[0], shops[1]}, supported)
	token, err := publicAccountImportShopToken(shops[1].URL)
	require.NoError(t, err)
	require.Equal(t, "7HZ37ZCG", token)
}

func TestPublicAccountImportProductSnapshotKeepsSoftStaleLegacyProducts(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "stale"}, {ID: "missing"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"stale": {
			UpdatedAt:          now.Add(-20 * time.Minute).Format(time.RFC3339),
			RefreshRequestedAt: now.Format(time.RFC3339),
			Products: []PublicAccountImportProduct{
				{ID: "available", Name: "Available", Price: 2, Stock: 1, MinimumQuantity: 1},
				{ID: "legacy", Name: "Legacy", Price: 1, Stock: 1},
				{ID: "below-minimum", Name: "Below minimum", Price: 2, Stock: 1, MinimumQuantity: 2},
				{ID: "sold-out", Name: "Sold out", Price: 3, Stock: 0},
			},
		},
	}}

	products, pending, queued, refreshing, failed, expired := publicAccountImportProductSnapshot(shops, store, now)
	require.Equal(t, []PublicAccountImportProduct{
		{ID: "available", Name: "Available", Price: 2, Stock: 1, MinimumQuantity: 1},
		{ID: "legacy", Name: "Legacy", Price: 1, Stock: 1, MinimumQuantity: 1},
	}, products)
	require.Equal(t, 2, pending)
	require.Equal(t, 1, queued)
	require.Zero(t, refreshing)
	require.Zero(t, failed)
	require.Zero(t, expired)
}

func TestNormalizePublicProductSyncItemRequiresEnoughStockForMinimumQuantity(t *testing.T) {
	shop := PublicAccountImportShop{ID: "shop", Name: "Shop", URL: "https://pay.ldxp.cn/shop/token"}
	price := 1.0
	payable := 50.0
	stock := 50
	minimumQuantity := 50
	item := PublicAccountImportProductSyncItem{
		GoodsKey: "goods", Name: "Product", URL: "https://pay.ldxp.cn/item/goods",
		GoodsType: "card", Price: &price, PayablePrice: &payable, Stock: &stock, MinimumQuantity: &minimumQuantity,
		QuoteVerifiedAt: "2026-07-24T00:00:00Z",
	}

	product, err := normalizePublicProductSyncItem(shop, item, "2026-07-24T00:00:00Z")
	require.NoError(t, err)
	require.Equal(t, 50, product.MinimumQuantity)
	require.Equal(t, 50.0, *product.PayablePrice)
	require.Equal(t, 1.0, *product.UnitPrice)

	stock = 49
	_, err = normalizePublicProductSyncItem(shop, item, "2026-07-24T00:00:00Z")
	require.Error(t, err)

	stock = 1
	minimumQuantity = 0
	_, err = normalizePublicProductSyncItem(shop, item, "2026-07-24T00:00:00Z")
	require.Error(t, err)
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
			SchemaVersion: publicAccountImportProductSchemaVersion,
			UpdatedAt:     now.Add(-time.Minute).Format(time.RFC3339),
			LastAttempt:   now.Add(-time.Hour).Format(time.RFC3339),
			Products:      []PublicAccountImportProduct{},
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
		"fresh": {
			SchemaVersion: publicAccountImportProductSchemaVersion,
			UpdatedAt:     now.Add(-time.Minute).Format(time.RFC3339),
			Products:      []PublicAccountImportProduct{},
		},
		"leased": {LastAttempt: now.Add(-30 * time.Second).Format(time.RFC3339)},
	}}

	require.Nil(t, selectPublicAccountImportProductSyncShop(shops, store, now))
}

func TestSelectPublicAccountImportProductSyncShopKeepsTenMinuteCacheFresh(t *testing.T) {
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "ten-minutes-old"}, {ID: "stale"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"ten-minutes-old": {
			SchemaVersion: publicAccountImportProductSchemaVersion,
			UpdatedAt:     now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
			Products:      []PublicAccountImportProduct{},
		},
		"stale": {
			SchemaVersion: publicAccountImportProductSchemaVersion,
			UpdatedAt:     now.Add(-publicAccountImportProductRefreshAge - time.Second).Format(time.RFC3339Nano),
			Products:      []PublicAccountImportProduct{},
		},
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
			SchemaVersion: publicAccountImportProductSchemaVersion,
			UpdatedAt:     now.Add(-time.Hour).Format(time.RFC3339),
			LastAttempt:   now.Add(-10 * time.Minute).Format(time.RFC3339),
			Products:      []PublicAccountImportProduct{},
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
		"fresh": {
			SchemaVersion: publicAccountImportProductSchemaVersion,
			UpdatedAt:     now.Add(-time.Minute).Format(time.RFC3339),
			Products:      []PublicAccountImportProduct{},
		},
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

func TestPublicAccountImportProductRefreshStatusIncludesAutomaticSyncJobs(t *testing.T) {
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
	require.Equal(t, 1, status.Refreshing)
	require.Zero(t, status.Failed)
}

func TestSelectPublicAccountImportProductSyncShopsReservesAutomaticSlot(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "active-manual"}, {ID: "queued-manual"}, {ID: "automatic"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"active-manual": {
			RefreshRequestedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			SyncStartedAt:      now.Add(-time.Minute).Format(time.RFC3339Nano), SyncHeartbeatAt: now.Format(time.RFC3339Nano),
		},
		"queued-manual": {RefreshRequestedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)},
		"automatic":     {},
	}}

	selected := selectPublicAccountImportProductSyncShops(shops, store, now, 1)
	require.Equal(t, []PublicAccountImportShop{{ID: "automatic"}}, selected)
}

func TestSelectPublicAccountImportProductSyncShopsLimitsManualWorkToOneOfSixSlots(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{
		{ID: "manual-one"}, {ID: "manual-two"},
		{ID: "automatic-one"}, {ID: "automatic-two"}, {ID: "automatic-three"}, {ID: "automatic-four"}, {ID: "automatic-five"},
	}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"manual-one":      {RefreshRequestedAt: now.Add(-2 * time.Minute).Format(time.RFC3339Nano)},
		"manual-two":      {RefreshRequestedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)},
		"automatic-one":   {},
		"automatic-two":   {},
		"automatic-three": {},
		"automatic-four":  {},
		"automatic-five":  {},
	}}

	selected := selectPublicAccountImportProductSyncShops(shops, store, now, 6)
	require.Len(t, selected, 6)
	require.Equal(t, []string{"manual-one", "automatic-one", "automatic-two", "automatic-three", "automatic-four", "automatic-five"}, []string{
		selected[0].ID, selected[1].ID, selected[2].ID, selected[3].ID, selected[4].ID, selected[5].ID,
	})
}

func TestPublicAccountImportProductRefreshStatusIncludesAutomaticFailure(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "failed"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"failed": {Error: "HTTP 429", LastAttempt: now.Format(time.RFC3339Nano)},
	}}

	status := publicAccountImportProductRefreshStatusForShops(shops, store, now)
	require.Equal(t, 1, status.Failed)
	require.Equal(t, "failed", publicAccountImportProductSyncStatusForShop(shops[0], store.Shops["failed"], now).State)
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
			LastAttempt:     now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
			SyncStartedAt:   now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
			SyncHeartbeatAt: now.Add(-time.Second).Format(time.RFC3339Nano),
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
	require.Equal(t, 6, publicAccountImportProductMaxSyncJobs)
	require.Equal(t, 4, publicAccountImportProductMaxSyncJobs-countPublicAccountImportProductActiveSyncs(shops, store, now))
}

func TestPublicAccountImportProductSyncLeaseHasThirtyMinuteAttemptLimit(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	require.Equal(t, 90*time.Second, publicAccountImportProductSyncLeaseAge)
	require.Equal(t, 30*time.Minute, publicAccountImportProductSyncMaxAge)
	require.True(t, publicAccountImportProductSyncIsActive(publicAccountImportProductShopCache{
		SyncStartedAt:   now.Add(-publicAccountImportProductSyncMaxAge).Format(time.RFC3339Nano),
		SyncHeartbeatAt: now.Format(time.RFC3339Nano),
	}, now))
	require.False(t, publicAccountImportProductSyncIsActive(publicAccountImportProductShopCache{
		SyncStartedAt:   now.Add(-publicAccountImportProductSyncMaxAge - time.Second).Format(time.RFC3339Nano),
		SyncHeartbeatAt: now.Format(time.RFC3339Nano),
	}, now))
}

func TestPublicAccountImportProductSyncLeasesSixJobsAndWithholdsTheSeventh(t *testing.T) {
	now := time.Now().UTC()
	shops := make([]PublicAccountImportShop, 0, 7)
	store := publicAccountImportProductStore{Shops: make(map[string]publicAccountImportProductShopCache, 7)}
	for index := 1; index <= 7; index++ {
		shopID := fmt.Sprintf("shop-%d", index)
		shops = append(shops, PublicAccountImportShop{
			ID: shopID, Name: shopID, URL: fmt.Sprintf("https://pay.ldxp.cn/shop/token-%d", index),
		})
		store.Shops[shopID] = publicAccountImportProductShopCache{
			SchemaVersion: publicAccountImportProductSchemaVersion,
			UpdatedAt:     now.Add(-time.Hour).Format(time.RFC3339Nano),
			Products:      []PublicAccountImportProduct{},
		}
	}
	router := newPublicProductTestRouter(t, shops, store)

	publicProductCacheMu.Lock()
	publicProductLastJobAt = time.Time{}
	publicProductCacheMu.Unlock()
	batch := performPublicProductRequest(t, router, http.MethodGet, "/products/sync-job?limit=7", nil)
	require.Equal(t, http.StatusOK, batch.Code, batch.Body.String())
	batchData := decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, batch.Body.Bytes())
	require.Len(t, batchData.Jobs, 6)
	require.Equal(t, []string{"shop-1", "shop-2", "shop-3", "shop-4", "shop-5", "shop-6"}, []string{
		batchData.Jobs[0].ShopID, batchData.Jobs[1].ShopID, batchData.Jobs[2].ShopID, batchData.Jobs[3].ShopID, batchData.Jobs[4].ShopID, batchData.Jobs[5].ShopID,
	})

	publicProductCacheMu.Lock()
	publicProductLastJobAt = time.Time{}
	publicProductCacheMu.Unlock()
	full := performPublicProductRequest(t, router, http.MethodGet, "/products/sync-job?limit=7", nil)
	fullData := decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, full.Body.Bytes())
	require.Nil(t, fullData.Job)
	require.Empty(t, fullData.Jobs)

	publicProductCacheMu.Lock()
	expired := publicProductCache.Shops["shop-1"]
	expired.SyncHeartbeatAt = time.Now().UTC().Add(-publicAccountImportProductSyncLeaseAge - time.Second).Format(time.RFC3339Nano)
	publicProductCache.Shops["shop-1"] = expired
	publicProductLastJobAt = time.Time{}
	publicProductCacheMu.Unlock()
	released := performPublicProductRequest(t, router, http.MethodGet, "/products/sync-job?limit=7", nil)
	releasedData := decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, released.Body.Bytes())
	require.Len(t, releasedData.Jobs, 1)
	require.Equal(t, "shop-7", releasedData.Jobs[0].ShopID)
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

	first := performPublicProductRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "manual"})
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

	duplicate := performPublicProductRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "manual"})
	require.Equal(t, http.StatusOK, duplicate.Code, duplicate.Body.String())
	duplicateData := decodePublicProductResponse[PublicAccountImportProductRefreshResponse](t, duplicate.Body.Bytes())
	require.False(t, duplicateData.Accepted)
	require.Equal(t, "queued", duplicateData.State)
	publicProductCacheMu.Lock()
	require.Equal(t, firstRequestedAt, publicProductCache.Shops["manual"].RefreshRequestedAt)
	publicProductCacheMu.Unlock()

	unknown := performPublicProductRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "missing"})
	require.Equal(t, http.StatusBadRequest, unknown.Code, unknown.Body.String())
	unsupported := performPublicProductRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "unsupported"})
	require.Equal(t, http.StatusBadRequest, unsupported.Code, unsupported.Body.String())

	list := performPublicProductRequest(t, router, http.MethodGet, "/products", nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	listData := decodePublicProductResponse[PublicAccountImportProductsResponse](t, list.Body.Bytes())
	require.Equal(t, 2, listData.ShopCount)
	require.Equal(t, 1, listData.QueuedShops)
	require.Zero(t, listData.RefreshingShops)
	require.Len(t, listData.ShopSyncStatuses, 2)
	require.Equal(t, []string{"manual", "automatic"}, []string{listData.ShopSyncStatuses[0].ShopID, listData.ShopSyncStatuses[1].ShopID})
	require.Equal(t, "queued", listData.ShopSyncStatuses[0].State)
	require.Equal(t, "idle", listData.ShopSyncStatuses[1].State)

	jobResponse := performPublicProductRequest(t, router, http.MethodGet, "/products/sync-job?limit=1", nil)
	require.Equal(t, http.StatusOK, jobResponse.Code, jobResponse.Body.String())
	jobData := decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, jobResponse.Body.Bytes())
	require.NotNil(t, jobData.Job)
	require.Equal(t, "manual", jobData.Job.ShopID)
	require.NotEmpty(t, jobData.Job.AttemptID)

	running := performPublicProductRequest(t, router, http.MethodGet, "/products", nil)
	runningData := decodePublicProductResponse[PublicAccountImportProductsResponse](t, running.Body.Bytes())
	require.Equal(t, "refreshing", runningData.ShopSyncStatuses[0].State)
	require.Equal(t, 1, runningData.RefreshingShops)

	failure := performPublicProductRequest(t, router, http.MethodPost, "/products/sync-failure", PublicAccountImportProductSyncFailureRequest{
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

	retryJobResponse := performPublicProductRequest(t, router, http.MethodGet, "/products/sync-job?limit=1", nil)
	require.Equal(t, http.StatusOK, retryJobResponse.Code, retryJobResponse.Body.String())
	retryJobData := decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, retryJobResponse.Body.Bytes())
	require.NotNil(t, retryJobData.Job)
	require.Equal(t, "manual", retryJobData.Job.ShopID)
	require.NotEqual(t, jobData.Job.AttemptID, retryJobData.Job.AttemptID)

	price := 1.5
	payablePrice := 1.5
	stock := 2
	minimumQuantity := 1
	sourceCount := 1
	sellableCount := 1
	unavailableCount := 0
	success := performPublicProductRequest(t, router, http.MethodPost, "/products/sync", PublicAccountImportProductSyncRequest{
		SchemaVersion: publicAccountImportProductSchemaVersion,
		ShopID:        "manual", AttemptID: retryJobData.Job.AttemptID,
		SourceProductCount: &sourceCount, SellableProductCount: &sellableCount, UnavailableProductCount: &unavailableCount,
		Products: []PublicAccountImportProductSyncItem{{
			GoodsKey: "new-product", Name: "New product", URL: "https://pay.ldxp.cn/item/new-product",
			GoodsType: "card", Price: &price, PayablePrice: &payablePrice, Stock: &stock, MinimumQuantity: &minimumQuantity,
			QuoteVerifiedAt: time.Now().UTC().Format(time.RFC3339Nano),
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

	cooldown := performPublicProductRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "manual"})
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
	afterCooldown := performPublicProductRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "manual"})
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

	shop := PublicAccountImportShop{ID: "shop", TrustLevel: publicAccountImportShopNeutral}
	status := publicAccountImportProductSyncStatusForShop(shop, cached, now)
	require.Equal(t, "failed", status.State)
	require.Zero(t, status.RetryAfterSeconds)

	cached.SyncStartedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	status = publicAccountImportProductSyncStatusForShop(shop, cached, now)
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

	result := performPublicProductRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "running"})
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

	result := performPublicProductRequest(t, router, http.MethodPost, "/products/refresh", PublicAccountImportProductRefreshRequest{ShopID: "failed"})
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
	shop := PublicAccountImportShop{TrustLevel: publicAccountImportShopNeutral}
	require.Equal(t, 180, publicAccountImportProductRefreshRetryAfter(shop, cached, now))
	require.Zero(t, publicAccountImportProductRefreshRetryAfter(shop, cached, now.Add(3*time.Minute)))
	require.Zero(t, publicAccountImportProductRefreshRetryAfter(shop, publicAccountImportProductShopCache{}, now))
}

func TestPublicAccountImportProductAutoRefreshBoundariesByTrustLevel(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		trustLevel string
		age        time.Duration
	}{
		{name: "trusted", trustLevel: publicAccountImportShopTrusted, age: 5 * time.Minute},
		{name: "neutral", trustLevel: publicAccountImportShopNeutral, age: 15 * time.Minute},
		{name: "untrusted", trustLevel: publicAccountImportShopUntrusted, age: 60 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shop := PublicAccountImportShop{ID: tt.name, TrustLevel: tt.trustLevel}
			atBoundary := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
				shop.ID: authoritativeProductCache(shop.ID, now.Add(-tt.age), 1),
			}}
			require.Empty(t, selectPublicAccountImportProductSyncShops([]PublicAccountImportShop{shop}, atBoundary, now, 1))

			pastBoundary := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
				shop.ID: authoritativeProductCache(shop.ID, now.Add(-tt.age-time.Second), 1),
			}}
			require.Equal(t, []PublicAccountImportShop{shop}, selectPublicAccountImportProductSyncShops([]PublicAccountImportShop{shop}, pastBoundary, now, 1))
		})
	}
}

func TestPublicAccountImportProductStrictExpiryBoundariesByTrustLevel(t *testing.T) {
	t.Setenv(publicAccountImportProductStrictModeEnv, "true")
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		trustLevel string
		maxAge     time.Duration
	}{
		{name: "trusted", trustLevel: publicAccountImportShopTrusted, maxAge: 30 * time.Minute},
		{name: "neutral", trustLevel: publicAccountImportShopNeutral, maxAge: 30 * time.Minute},
		{name: "untrusted", trustLevel: publicAccountImportShopUntrusted, maxAge: 120 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shop := PublicAccountImportShop{ID: tt.name, TrustLevel: tt.trustLevel}
			atBoundary := authoritativeProductCache(shop.ID, now.Add(-tt.maxAge), 1)
			require.Equal(t, "stale", publicAccountImportProductSnapshotState(shop, atBoundary, now, true))
			require.Equal(t, now.Format(time.RFC3339Nano), publicAccountImportProductSyncStatusForShop(shop, atBoundary, now).SnapshotExpiresAt)

			pastBoundary := authoritativeProductCache(shop.ID, now.Add(-tt.maxAge-time.Second), 1)
			require.Equal(t, "expired", publicAccountImportProductSnapshotState(shop, pastBoundary, now, true))
		})
	}
}

func TestPublicAccountImportProductFailureRetryBoundariesByTrustLevel(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		trustLevel string
		retryAge   time.Duration
	}{
		{name: "trusted", trustLevel: publicAccountImportShopTrusted, retryAge: time.Minute},
		{name: "neutral", trustLevel: publicAccountImportShopNeutral, retryAge: time.Minute},
		{name: "untrusted", trustLevel: publicAccountImportShopUntrusted, retryAge: 15 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shop := PublicAccountImportShop{ID: tt.name, TrustLevel: tt.trustLevel}
			store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
				shop.ID: {Error: "failed", LastAttempt: now.Add(-tt.retryAge + time.Second).Format(time.RFC3339Nano)},
			}}
			require.Empty(t, selectPublicAccountImportProductSyncShops([]PublicAccountImportShop{shop}, store, now, 1))

			cached := store.Shops[shop.ID]
			cached.LastAttempt = now.Add(-tt.retryAge).Format(time.RFC3339Nano)
			store.Shops[shop.ID] = cached
			require.Equal(t, []PublicAccountImportShop{shop}, selectPublicAccountImportProductSyncShops([]PublicAccountImportShop{shop}, store, now, 1))
		})
	}
}

func TestPublicAccountImportProductManualRefreshCooldownBoundariesByTrustLevel(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		trustLevel string
		cooldown   time.Duration
	}{
		{name: "trusted", trustLevel: publicAccountImportShopTrusted, cooldown: 5 * time.Minute},
		{name: "neutral", trustLevel: publicAccountImportShopNeutral, cooldown: 5 * time.Minute},
		{name: "untrusted", trustLevel: publicAccountImportShopUntrusted, cooldown: 60 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shop := PublicAccountImportShop{ID: tt.name, TrustLevel: tt.trustLevel}
			cached := publicAccountImportProductShopCache{
				ManualRefreshCompletedAt: now.Add(-tt.cooldown + time.Second).Format(time.RFC3339Nano),
			}
			require.Equal(t, 1, publicAccountImportProductRefreshRetryAfter(shop, cached, now))
			cached.ManualRefreshCompletedAt = now.Add(-tt.cooldown).Format(time.RFC3339Nano)
			require.Zero(t, publicAccountImportProductRefreshRetryAfter(shop, cached, now))
		})
	}

	untrusted := PublicAccountImportShop{ID: "untrusted-success", TrustLevel: publicAccountImportShopUntrusted}
	cached := publicAccountImportProductShopCache{
		UpdatedAt:                now.Add(-30 * time.Minute).Format(time.RFC3339Nano),
		ManualRefreshCompletedAt: now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
	}
	require.Equal(t, 30*60, publicAccountImportProductRefreshRetryAfter(untrusted, cached, now))
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

func TestPublicAccountImportProductSnapshotAppliesSoftAndHardExpiryPerShop(t *testing.T) {
	t.Setenv(publicAccountImportProductStrictModeEnv, "true")
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "fresh"}, {ID: "stale"}, {ID: "expired"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"fresh":   authoritativeProductCache("fresh", now.Add(-5*time.Minute), 1),
		"stale":   authoritativeProductCache("stale", now.Add(-20*time.Minute), 2),
		"expired": authoritativeProductCache("expired", now.Add(-31*time.Minute), 3),
	}}

	products, pending, _, _, _, expired := publicAccountImportProductSnapshot(shops, store, now)
	require.Equal(t, []string{"stale-product", "fresh-product"}, []string{products[0].ID, products[1].ID})
	require.Equal(t, 2, pending)
	require.Equal(t, 1, expired)

	statuses := publicAccountImportProductSyncStatuses(shops, store, now)
	require.Equal(t, []string{"fresh", "stale", "expired"}, []string{
		statuses[0].SnapshotState, statuses[1].SnapshotState, statuses[2].SnapshotState,
	})
	require.Equal(t, store.Shops["fresh"].UpdatedAt, statuses[0].SnapshotUpdatedAt)
	require.Equal(t, now.Add(25*time.Minute).Format(time.RFC3339Nano), statuses[0].SnapshotExpiresAt)
}

func TestPublicAccountImportProductCompatibilityModeKeepsPastDueAuthoritativeSnapshots(t *testing.T) {
	t.Setenv(publicAccountImportProductStrictModeEnv, "false")
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "past-due"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"past-due": authoritativeProductCache("past-due", now.Add(-31*time.Minute), 1),
	}}

	products, pending, _, _, _, expired := publicAccountImportProductSnapshot(shops, store, now)
	require.Len(t, products, 1)
	require.Equal(t, 1, pending)
	require.Zero(t, expired)
	require.Equal(t, "stale", publicAccountImportProductSyncStatuses(shops, store, now)[0].SnapshotState)
}

func TestPublicAccountImportProductStrictModeHidesLegacySnapshots(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "legacy"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"legacy": {
			UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano),
			Products:  []PublicAccountImportProduct{{ID: "legacy-product", Stock: 1, MinimumQuantity: 1}},
		},
	}}

	t.Setenv(publicAccountImportProductStrictModeEnv, "false")
	products, _, _, _, _, expired := publicAccountImportProductSnapshot(shops, store, now)
	require.Len(t, products, 1)
	require.Zero(t, expired)
	require.Equal(t, "legacy", publicAccountImportProductSyncStatuses(shops, store, now)[0].SnapshotState)

	t.Setenv(publicAccountImportProductStrictModeEnv, "true")
	products, _, _, _, _, expired = publicAccountImportProductSnapshot(shops, store, now)
	require.Empty(t, products)
	require.Equal(t, 1, expired)
	require.Equal(t, "expired", publicAccountImportProductSyncStatuses(shops, store, now)[0].SnapshotState)
}

func TestSubmitPublicAccountImportProductSyncRejectsIncompleteSnapshotAtomically(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PublicAccountImportProductSyncRequest)
	}{
		{name: "missing quote", mutate: func(req *PublicAccountImportProductSyncRequest) {
			req.Products[0].PayablePrice = nil
		}},
		{name: "duplicate product", mutate: func(req *PublicAccountImportProductSyncRequest) {
			req.Products = append(req.Products, req.Products[0])
			*req.SourceProductCount = 2
			*req.SellableProductCount = 2
		}},
		{name: "illegal stock", mutate: func(req *PublicAccountImportProductSyncRequest) {
			stock := 0
			req.Products[0].Stock = &stock
		}},
		{name: "mismatched counts", mutate: func(req *PublicAccountImportProductSyncRequest) {
			*req.SourceProductCount = 2
		}},
		{name: "too many source products", mutate: func(req *PublicAccountImportProductSyncRequest) {
			*req.SourceProductCount = publicAccountImportProductMaxProducts + 1
			*req.UnavailableProductCount = publicAccountImportProductMaxProducts
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now().UTC()
			shops := []PublicAccountImportShop{{ID: "shop", Name: "Shop", URL: "https://pay.ldxp.cn/shop/token"}}
			store := publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{
				"shop": {
					ShopID: "shop", SyncStartedAt: now.Format(time.RFC3339Nano), SyncHeartbeatAt: now.Format(time.RFC3339Nano), SyncAttemptID: "attempt",
					UpdatedAt: "2026-07-27T00:00:00Z", Products: []PublicAccountImportProduct{{ID: "previous", Stock: 1, MinimumQuantity: 1}},
				},
			}}
			router := newPublicProductTestRouter(t, shops, store)
			req := validPublicProductSyncRequest("shop", "attempt", now)
			tt.mutate(&req)

			result := performPublicProductRequest(t, router, http.MethodPost, "/products/sync", req)
			require.Equal(t, http.StatusBadRequest, result.Code, result.Body.String())
			publicProductCacheMu.RLock()
			cached := publicProductCache.Shops["shop"]
			publicProductCacheMu.RUnlock()
			require.Equal(t, "2026-07-27T00:00:00Z", cached.UpdatedAt)
			require.Equal(t, "previous", cached.Products[0].ID)
			require.Equal(t, "attempt", cached.SyncAttemptID)
		})
	}
}

func TestSubmitPublicAccountImportProductSyncPublishesVerifiedEmptySnapshot(t *testing.T) {
	now := time.Now().UTC()
	shops := []PublicAccountImportShop{{ID: "shop", Name: "Shop", URL: "https://pay.ldxp.cn/shop/token"}}
	store := publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{
		"shop": {
			ShopID: "shop", SyncStartedAt: now.Format(time.RFC3339Nano), SyncHeartbeatAt: now.Format(time.RFC3339Nano), SyncAttemptID: "attempt",
			UpdatedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), Products: []PublicAccountImportProduct{{ID: "previous", Stock: 1, MinimumQuantity: 1}},
		},
	}}
	router := newPublicProductTestRouter(t, shops, store)
	sourceCount := 3
	sellableCount := 0
	unavailableCount := 3

	result := performPublicProductRequest(t, router, http.MethodPost, "/products/sync", PublicAccountImportProductSyncRequest{
		SchemaVersion: publicAccountImportProductSchemaVersion,
		ShopID:        "shop", AttemptID: "attempt",
		SourceProductCount: &sourceCount, SellableProductCount: &sellableCount, UnavailableProductCount: &unavailableCount,
		Products: []PublicAccountImportProductSyncItem{},
	})
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	publicProductCacheMu.RLock()
	cached := publicProductCache.Shops["shop"]
	publicProductCacheMu.RUnlock()
	require.Empty(t, cached.Products)
	require.Equal(t, 3, cached.SourceProductCount)
	require.Equal(t, 3, cached.UnavailableProductCount)
	require.True(t, publicAccountImportProductSnapshotIsAuthoritative(cached))
}

func TestPublicAccountImportProductWorkerRoutesRequireBearerAuthentication(t *testing.T) {
	router := newPublicProductTestRouter(t, nil, publicAccountImportProductStore{})

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/products/sync-job", nil))
	require.Equal(t, http.StatusUnauthorized, missing.Code)
	require.NotEmpty(t, missing.Header().Get("WWW-Authenticate"))

	wrong := httptest.NewRecorder()
	wrongRequest := httptest.NewRequest(http.MethodGet, "/products/sync-job", nil)
	wrongRequest.Header.Set("Authorization", "Bearer wrong")
	router.ServeHTTP(wrong, wrongRequest)
	require.Equal(t, http.StatusUnauthorized, wrong.Code)

	authorized := performPublicProductRequest(t, router, http.MethodGet, "/products/sync-job", nil)
	require.Equal(t, http.StatusOK, authorized.Code, authorized.Body.String())
}

func TestPublicAccountImportProductHeartbeatRenewsLeaseWithoutRefreshingSnapshot(t *testing.T) {
	now := time.Now().UTC()
	shops := []PublicAccountImportShop{{ID: "shop", Name: "Shop", URL: "https://pay.ldxp.cn/shop/token"}}
	store := publicAccountImportProductStore{Version: publicAccountImportProductStoreVersion, Shops: map[string]publicAccountImportProductShopCache{
		"shop": {
			ShopID: "shop", SyncStartedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), SyncHeartbeatAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			SyncAttemptID: "attempt", UpdatedAt: "2026-07-27T00:00:00Z", Products: []PublicAccountImportProduct{},
		},
	}}
	router := newPublicProductTestRouter(t, shops, store)

	result := performPublicProductRequest(t, router, http.MethodPost, "/products/sync-heartbeat", PublicAccountImportProductSyncHeartbeatRequest{
		ShopID: "shop", AttemptID: "attempt",
	})
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	publicProductCacheMu.RLock()
	cached := publicProductCache.Shops["shop"]
	publicProductCacheMu.RUnlock()
	require.Equal(t, "2026-07-27T00:00:00Z", cached.UpdatedAt)
	require.WithinDuration(t, time.Now().UTC(), parsePublicAccountImportProductTimestamp(cached.SyncHeartbeatAt), 2*time.Second)

	publicProductCacheMu.Lock()
	cached.SyncHeartbeatAt = time.Now().UTC().Add(-publicAccountImportProductSyncLeaseAge - time.Second).Format(time.RFC3339Nano)
	publicProductCache.Shops["shop"] = cached
	publicProductCacheMu.Unlock()
	expired := performPublicProductRequest(t, router, http.MethodPost, "/products/sync-heartbeat", PublicAccountImportProductSyncHeartbeatRequest{
		ShopID: "shop", AttemptID: "attempt",
	})
	require.Equal(t, http.StatusConflict, expired.Code, expired.Body.String())
}

func TestListPublicAccountImportProductsSupportsETag(t *testing.T) {
	router := newPublicProductTestRouter(t, nil, publicAccountImportProductStore{})
	first := performPublicProductRequest(t, router, http.MethodGet, "/products", nil)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/products", nil)
	request.Header.Set("If-None-Match", etag)
	router.ServeHTTP(second, request)
	require.Equal(t, http.StatusNotModified, second.Code, second.Body.String())
	require.Equal(t, etag, second.Header().Get("ETag"))
}

func authoritativeProductCache(shopID string, updatedAt time.Time, unitPrice float64) publicAccountImportProductShopCache {
	payablePrice := unitPrice
	return publicAccountImportProductShopCache{
		ShopID: shopID, SchemaVersion: publicAccountImportProductSchemaVersion,
		SourceProductCount: 1, SellableProductCount: 1,
		UpdatedAt: updatedAt.Format(time.RFC3339Nano),
		Products: []PublicAccountImportProduct{{
			ID: shopID + "-product", ShopID: shopID, Price: unitPrice, PayablePrice: &payablePrice, UnitPrice: &unitPrice,
			Stock: 1, MinimumQuantity: 1, QuoteVerifiedAt: updatedAt.Format(time.RFC3339Nano), UpdatedAt: updatedAt.Format(time.RFC3339Nano),
		}},
	}
}

func validPublicProductSyncRequest(shopID, attemptID string, quoteTime time.Time) PublicAccountImportProductSyncRequest {
	price := 2.0
	marketPrice := 3.0
	payablePrice := 4.0
	stock := 5
	minimumQuantity := 2
	sourceCount := 1
	sellableCount := 1
	unavailableCount := 0
	return PublicAccountImportProductSyncRequest{
		SchemaVersion: publicAccountImportProductSchemaVersion,
		ShopID:        shopID, AttemptID: attemptID,
		SourceProductCount: &sourceCount, SellableProductCount: &sellableCount, UnavailableProductCount: &unavailableCount,
		Products: []PublicAccountImportProductSyncItem{{
			GoodsKey: "goods", Name: "Product", URL: "https://pay.ldxp.cn/item/goods", GoodsType: "card",
			Price: &price, MarketPrice: &marketPrice, PayablePrice: &payablePrice, Stock: &stock, MinimumQuantity: &minimumQuantity,
			QuoteVerifiedAt: quoteTime.Format(time.RFC3339Nano),
		}},
	}
}

func newPublicProductTestRouter(t *testing.T, shops []PublicAccountImportShop, store publicAccountImportProductStore) http.Handler {
	t.Helper()
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(publicAccountImportProductSyncTokenEnv, "test-product-sync-token")
	t.Setenv(publicAccountImportProductStrictModeEnv, "false")
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
	router.POST("/products/sync-heartbeat", h.HeartbeatPublicAccountImportProductSync)
	return router
}

func performPublicProductRequest(t *testing.T, router http.Handler, method, path string, payload any) *httptest.ResponseRecorder {
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
	if token := strings.TrimSpace(os.Getenv(publicAccountImportProductSyncTokenEnv)); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodePublicProductResponse[T any](t *testing.T, body []byte) T {
	t.Helper()
	var payload struct {
		Data T `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &payload), string(body))
	return payload.Data
}
