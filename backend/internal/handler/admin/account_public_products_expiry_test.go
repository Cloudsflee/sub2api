package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPublicProductExpiredSnapshotsRemainVisibleByTrustLevel(t *testing.T) {
	t.Setenv(publicAccountImportProductStrictModeEnv, "true")
	updatedAt := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, trust := range []struct {
		level string
		age   time.Duration
	}{
		{publicAccountImportShopTrusted, 30 * time.Minute},
		{publicAccountImportShopNeutral, 30 * time.Minute},
		{publicAccountImportShopUntrusted, 2 * time.Hour},
	} {
		t.Run(trust.level, func(t *testing.T) {
			shop := PublicAccountImportShop{ID: "shop", TrustLevel: trust.level}
			cached := authoritativeProductCache(shop.ID, updatedAt, 2)
			for _, boundary := range []struct {
				name    string
				elapsed time.Duration
				state   string
				visible int
			}{
				{"before expiry", trust.age - time.Nanosecond, "stale", 1},
				{"at expiry", trust.age, "stale", 1},
				{"after expiry", trust.age + time.Nanosecond, "expired", 1},
				{"at former grace end", trust.age + 3*time.Hour, "expired", 1},
				{"after former grace end", trust.age + 3*time.Hour + time.Nanosecond, "expired", 1},
				{"one year after expiry", trust.age + 365*24*time.Hour, "expired", 1},
			} {
				t.Run(boundary.name, func(t *testing.T) {
					now := updatedAt.Add(boundary.elapsed)
					// Even an active failure backoff cannot move the original expiry.
					cached.RetryNotBeforeAt = now.Add(time.Hour).Format(time.RFC3339Nano)
					cached.Error = "proxy backoff"
					store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}}
					products, pending, _, _, _, expired := publicAccountImportProductSnapshot([]PublicAccountImportShop{shop}, store, now)
					require.Len(t, products, boundary.visible)
					require.Equal(t, 1, pending)
					if boundary.state == "expired" {
						require.Equal(t, 1, expired)
					} else {
						require.Zero(t, expired)
					}
					status := publicAccountImportProductSyncStatusForShop(shop, cached, now)
					require.Equal(t, boundary.state, status.SnapshotState)
					require.Equal(t, updatedAt.Format(time.RFC3339Nano), status.SnapshotUpdatedAt)
					require.Equal(t, updatedAt.Add(trust.age).Format(time.RFC3339Nano), status.SnapshotExpiresAt)
					require.Equal(t, 3600, status.RetryAfterSeconds)
					require.Equal(t, cached, store.Shops[shop.ID])
				})
			}
		})
	}
}

func TestPublicProductRetentionNeverRestoresInvalidSnapshots(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	shop := PublicAccountImportShop{ID: "shop"}
	for _, tc := range []struct {
		name   string
		mutate func(*publicAccountImportProductShopCache)
	}{
		{"missing time", func(c *publicAccountImportProductShopCache) { c.UpdatedAt = "" }},
		{"invalid time", func(c *publicAccountImportProductShopCache) { c.UpdatedAt = "invalid" }},
		{"future time", func(c *publicAccountImportProductShopCache) {
			c.UpdatedAt = now.Add(2 * time.Minute).Format(time.RFC3339Nano)
		}},
		{"unknown schema", func(c *publicAccountImportProductShopCache) { c.SchemaVersion++ }},
		{"missing products", func(c *publicAccountImportProductShopCache) { c.Products = nil }},
		{"incomplete counts", func(c *publicAccountImportProductShopCache) { c.SourceProductCount++ }},
		{"negative count", func(c *publicAccountImportProductShopCache) { c.SourceProductCount = 0; c.UnavailableProductCount = -1 }},
		{"incomplete price", func(c *publicAccountImportProductShopCache) { c.Products[0].UnitPrice = nil }},
		{"invalid quote time", func(c *publicAccountImportProductShopCache) { c.Products[0].QuoteVerifiedAt = "invalid" }},
		{"future quote", func(c *publicAccountImportProductShopCache) {
			c.Products[0].QuoteVerifiedAt = now.Format(time.RFC3339Nano)
		}},
		{"sold out", func(c *publicAccountImportProductShopCache) { c.Products[0].Stock = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, age := range []time.Duration{time.Hour, 365 * 24 * time.Hour} {
				cached := authoritativeProductCache(shop.ID, now.Add(-age), 1)
				cached.RetryNotBeforeAt = now.Add(time.Hour).Format(time.RFC3339Nano)
				tc.mutate(&cached)
				for _, mode := range []string{"true", "false"} {
					t.Setenv(publicAccountImportProductStrictModeEnv, mode)
					products, _, _, _, _, _ := publicAccountImportProductSnapshot([]PublicAccountImportShop{shop}, publicAccountImportProductStore{
						Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached},
					}, now)
					require.Empty(t, products, "age=%s strict=%s", age, mode)
				}
			}
		})
	}
}

func TestPublicProductFailureDoesNotRenewSnapshot(t *testing.T) {
	for _, kind := range []string{"captcha", "proxy", "refresh_failed", "shop_closed"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			shop := PublicAccountImportShop{ID: "shop", URL: "https://wzyp.cn/shop/shop"}
			cached := authoritativeProductCache(shop.ID, now.Add(-90*24*time.Hour), 1)
			cached.SyncAttemptID = "attempt"
			cached.SyncStartedAt = now.Format(time.RFC3339Nano)
			cached.SyncHeartbeatAt = cached.SyncStartedAt
			router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{
				Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached},
			})
			t.Setenv(publicAccountImportProductStrictModeEnv, "true")
			failed := performPublicProductRequest(t, router, http.MethodPost, "/products/sync-failure", PublicAccountImportProductSyncFailureRequest{
				ShopID: shop.ID, AttemptID: "attempt", Error: kind, Kind: kind, RetryAfterSeconds: 3600,
			})
			require.Equal(t, http.StatusOK, failed.Code, failed.Body.String())
			store, err := snapshotPublicAccountImportProductStore()
			require.NoError(t, err)
			require.Equal(t, cached.UpdatedAt, store.Shops[shop.ID].UpdatedAt)
			require.Equal(t, cached.Products, store.Shops[shop.ID].Products)
			status := publicAccountImportProductSyncStatusForShop(shop, store.Shops[shop.ID], now)
			require.Equal(t, "expired", status.SnapshotState)
			require.Equal(t, cached.UpdatedAt, status.SnapshotUpdatedAt)
			require.Equal(t, now.Add(-90*24*time.Hour+30*time.Minute).Format(time.RFC3339Nano), status.SnapshotExpiresAt)
			products, _, _, _, _, _ := publicAccountImportProductSnapshot([]PublicAccountImportShop{shop}, store, now)
			require.Len(t, products, 1)
		})
	}
}

func TestPublicProductAPIExpiryChangesETagButRetentionDoesNot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now().UTC()
		shop := PublicAccountImportShop{ID: "shop", URL: "https://wzyp.cn/shop/shop"}
		cached := authoritativeProductCache(shop.ID, now.Add(-30*time.Minute), 1)
		router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{
			Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached},
		})
		t.Setenv(publicAccountImportProductStrictModeEnv, "true")
		publicProductCacheMu.Lock()
		err := savePublicProductCacheLocked()
		publicProductCacheMu.Unlock()
		require.NoError(t, err)
		stored, err := os.ReadFile(publicAccountImportProductsPath())
		require.NoError(t, err)
		etag := ""
		for _, step := range []struct {
			advance          time.Duration
			state            string
			visible, expired int
			code             int
		}{
			{0, "stale", 1, 0, http.StatusOK},
			{time.Nanosecond, "expired", 1, 1, http.StatusOK},
			{3*time.Hour - time.Nanosecond, "", 0, 0, http.StatusNotModified},
			{time.Nanosecond, "", 0, 0, http.StatusNotModified},
			{365 * 24 * time.Hour, "", 0, 0, http.StatusNotModified},
		} {
			time.Sleep(step.advance)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/products", nil)
			request.Header.Set("If-None-Match", etag)
			router.ServeHTTP(recorder, request)
			require.Equal(t, step.code, recorder.Code, recorder.Body.String())
			if step.code == http.StatusOK {
				result := decodePublicProductResponse[PublicAccountImportProductsResponse](t, recorder.Body.Bytes())
				require.Len(t, result.Products, step.visible)
				require.Equal(t, step.expired, result.ExpiredShops)
				require.Equal(t, step.state, result.ShopSyncStatuses[0].SnapshotState)
				require.Equal(t, now.Format(time.RFC3339Nano), result.ShopSyncStatuses[0].SnapshotExpiresAt)
				require.NotEqual(t, etag, recorder.Header().Get("ETag"))
			} else {
				require.Equal(t, etag, recorder.Header().Get("ETag"))
				require.Empty(t, recorder.Body.Bytes())
				// A client without a cached response still receives the old products.
				full := performPublicProductRequest(t, router, http.MethodGet, "/products", nil)
				require.Equal(t, http.StatusOK, full.Code)
				require.Equal(t, etag, full.Header().Get("ETag"))
				result := decodePublicProductResponse[PublicAccountImportProductsResponse](t, full.Body.Bytes())
				require.Len(t, result.Products, 1)
				require.Equal(t, 1, result.ExpiredShops)
				require.Equal(t, "expired", result.ShopSyncStatuses[0].SnapshotState)
				require.Equal(t, cached.UpdatedAt, result.ShopSyncStatuses[0].SnapshotUpdatedAt)
				require.Equal(t, now.Format(time.RFC3339Nano), result.ShopSyncStatuses[0].SnapshotExpiresAt)
			}
			etag = recorder.Header().Get("ETag")
		}
		after, err := os.ReadFile(publicAccountImportProductsPath())
		require.NoError(t, err)
		require.Equal(t, stored, after)
		store, err := snapshotPublicAccountImportProductStore()
		require.NoError(t, err)
		require.Equal(t, cached.Products, store.Shops[shop.ID].Products)

		// A successful full refresh replaces the old catalog and renews its deadlines.
		jobResponse := performPublicProductRequest(t, router, http.MethodGet, "/products/sync-job", nil)
		require.Equal(t, http.StatusOK, jobResponse.Code, jobResponse.Body.String())
		job := decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, jobResponse.Body.Bytes()).Job
		require.NotNil(t, job)
		refreshedAt := time.Now().UTC()
		synced := performPublicProductRequest(t, router, http.MethodPost, "/products/sync",
			validPublicProductSyncRequest(shop.ID, job.AttemptID, refreshedAt))
		require.Equal(t, http.StatusOK, synced.Code, synced.Body.String())
		fresh := performPublicProductRequest(t, router, http.MethodGet, "/products", nil)
		result := decodePublicProductResponse[PublicAccountImportProductsResponse](t, fresh.Body.Bytes())
		require.Len(t, result.Products, 1)
		require.NotEqual(t, cached.Products[0].ID, result.Products[0].ID)
		require.Zero(t, result.ExpiredShops)
		require.Equal(t, "fresh", result.ShopSyncStatuses[0].SnapshotState)
		require.Equal(t, refreshedAt.Format(time.RFC3339Nano), result.ShopSyncStatuses[0].SnapshotUpdatedAt)
		require.Equal(t, refreshedAt.Add(30*time.Minute).Format(time.RFC3339Nano), result.ShopSyncStatuses[0].SnapshotExpiresAt)
		require.NotEqual(t, etag, fresh.Header().Get("ETag"))
	})
}
