package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPublicShopScheduleUsesConfirmedCatalogAndCurrentPeriods(t *testing.T) {
	control := defaultPublicAccountImportSyncControl()
	control.Mode = "adaptive"
	s := &productAdaptiveState{Multiplier: 3.6}
	a := &productAdaptiveShop{Tier: "11-50", EffectiveCount: 40}
	unknown := publicShopSchedule("shop", a, s, control)
	require.Equal(t, "pending", unknown.Tier)
	require.Nil(t, unknown.EffectiveCount, "legacy snapshot counts cannot classify an unprobed shop")
	a.Generation = 1
	a.CandidateTier, a.TierChecks, a.EffectiveCount = "51-200", 1, 51
	got := publicShopSchedule("shop", a, s, control)
	require.Equal(t, "11-50", got.Tier, "display the scheduler's confirmed tier during hysteresis")
	require.Equal(t, 51, *got.EffectiveCount)
	require.True(t, got.TierPending)
	require.Equal(t, 6480, got.CatalogSeconds)
	require.Equal(t, 25920, got.QuoteSeconds)
	a.EffectiveCount, a.ZeroChecks = 0, 1
	got = publicShopSchedule("shop", a, s, control)
	require.Equal(t, 600, got.CatalogSeconds)
	require.Zero(t, got.QuoteSeconds)
	require.Equal(t, "dormant", got.State)
	a.ZeroChecks, a.Tier, a.DormantStep = 2, "unavailable", 2
	got = publicShopSchedule("shop", a, s, control)
	require.Equal(t, "unavailable", got.Tier)
	require.Equal(t, 103680, got.CatalogSeconds)
	for _, mode := range []string{"legacy", "shadow"} {
		control.Mode = mode
		require.Nil(t, publicShopSchedule("shop", a, s, control))
	}
}

func TestPublicShopScheduleDoesNotAdvertisePausedWork(t *testing.T) {
	c := defaultPublicAccountImportSyncControl()
	c.Mode = "adaptive"
	a := &productAdaptiveShop{Generation: 1, Tier: "201-1000", EffectiveCount: 800, PauseReason: "private pressure details"}
	s := &productAdaptiveState{Multiplier: 4}
	got := publicShopSchedule("shop", a, s, c)
	require.Equal(t, "pressure_paused", got.State)
	require.Equal(t, "201-1000", got.Tier)
	require.Zero(t, got.CatalogSeconds)
	require.Zero(t, got.QuoteSeconds)
	s.AutomaticHold = "private host diagnostics"
	require.Equal(t, "paused", publicShopSchedule("shop", a, s, c).State)
	c.QuarantinedShops["shop"] = "private manual note"
	got = publicShopSchedule("shop", a, s, c)
	require.Equal(t, "isolated", got.State)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
}

func TestPublicShopScheduleAnonymousReadOnlyAndETag(t *testing.T) {
	now := time.Now().UTC()
	shop := PublicAccountImportShop{ID: "shop", URL: "https://wzyp.cn/shop/test"}
	cached := adaptiveTestCache(shop.ID, now)
	require.NoError(t, applyProductCatalog(&cached, shop, adaptiveTestCatalog(shop.ID, 3, now), now))
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}, Adaptive: &productAdaptiveState{Mode: "adaptive", Multiplier: 4}}
	router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, store)
	require.NoError(t, savePublicProductCacheLocked())
	before, err := os.ReadFile(publicAccountImportProductsPath())
	require.NoError(t, err)
	first := performPublicProductRequest(t, router, http.MethodGet, "/products", nil)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	result := decodePublicProductResponse[PublicAccountImportProductsResponse](t, first.Body.Bytes())
	require.Equal(t, "1-10", result.ShopSyncStatuses[0].Schedule.Tier)
	require.Equal(t, 3, *result.ShopSyncStatuses[0].Schedule.EffectiveCount, "unquoted items still count")
	require.Equal(t, 14400, result.ShopSyncStatuses[0].Schedule.QuoteSeconds)
	after, err := os.ReadFile(publicAccountImportProductsPath())
	require.NoError(t, err)
	require.Equal(t, before, after, "reading the public schedule must not persist or claim work")
	publicProductCache.Adaptive.Multiplier = 3.6
	second := performPublicProductRequest(t, router, http.MethodGet, "/products", nil)
	require.Equal(t, 12960, decodePublicProductResponse[PublicAccountImportProductsResponse](t, second.Body.Bytes()).ShopSyncStatuses[0].Schedule.QuoteSeconds)
	require.NotEqual(t, first.Header().Get("ETag"), second.Header().Get("ETag"))
}
