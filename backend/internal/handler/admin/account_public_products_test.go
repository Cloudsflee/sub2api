package admin

import (
	"fmt"
	"testing"
	"time"

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
				{ID: "available", Name: "Available", Price: 2, Stock: 1},
				{ID: "sold-out", Name: "Sold out", Price: 3, Stock: 0},
			},
		},
	}}

	products, pending, queued, refreshing, failed := publicAccountImportProductSnapshot(shops, store, now)
	require.Equal(t, []string{"available"}, []string{products[0].ID})
	require.Equal(t, 2, pending)
	require.Equal(t, 1, queued)
	require.Zero(t, refreshing)
	require.Zero(t, failed)
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
	require.Equal(t, []string{"stale", "requested"}, []string{selected[0].ID, selected[1].ID})
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
