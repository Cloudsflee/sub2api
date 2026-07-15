package admin

import (
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

	products, pending, refreshing := publicAccountImportProductSnapshot(shops, store, now)
	require.Equal(t, []string{"available"}, []string{products[0].ID})
	require.Equal(t, 2, pending)
	require.Equal(t, 1, refreshing)
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

func TestCountPublicAccountImportProductRefreshesIgnoresInvalidAndExpiredRequests(t *testing.T) {
	shops := []PublicAccountImportShop{{ID: "pending"}, {ID: "invalid"}, {ID: "expired"}, {ID: "done"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"pending": {RefreshRequestedAt: "2026-07-14T10:00:00Z"},
		"invalid": {RefreshRequestedAt: "invalid"},
		"expired": {RefreshRequestedAt: "2026-07-14T09:30:00Z"},
		"done":    {},
	}}

	now := time.Date(2026, 7, 14, 10, 5, 0, 0, time.UTC)
	require.Equal(t, 1, countPublicAccountImportProductRefreshes(shops, store, now))
}
