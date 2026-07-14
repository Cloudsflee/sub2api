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
