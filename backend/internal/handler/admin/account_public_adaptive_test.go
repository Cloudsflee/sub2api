package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func adaptiveTestCatalog(shop string, count int, now time.Time) productCatalogSubmission {
	products := []productCatalogInput{}
	for i := 0; i < count; i++ {
		item := validPublicProductSyncRequest(shop, "", now).Products[0]
		item.GoodsKey = fmt.Sprintf("goods-%03d", i)
		item.URL = "https://wzyp.cn/item/" + item.GoodsKey
		item.PayablePrice = nil
		item.QuoteVerifiedAt = ""
		products = append(products, productCatalogInput{PublicAccountImportProductSyncItem: item, Sellable: true, SourceVersion: strings.Repeat("a", 64)})
	}
	return productCatalogSubmission{ShopID: shop, AttemptID: "catalog", SchemaVersion: 1, Complete: true, SourceCount: count, Products: products, Cost: productCost{Requests: 5, LaneSeconds: 2}}
}

func adaptiveTestCache(shop string, now time.Time) publicAccountImportProductShopCache {
	return publicAccountImportProductShopCache{ShopID: shop, SyncAttemptID: "catalog", SyncStartedAt: now.Format(time.RFC3339Nano), SyncHeartbeatAt: now.Format(time.RFC3339Nano), Adaptive: &productAdaptiveShop{Lease: &productAdaptiveLease{Kind: "catalog", Started: now, Weight: 1, Period: 3600, Estimate: productCost{Requests: 5, LaneSeconds: 2}}}}
}

func TestPublicAdaptiveTierBoundariesAndDormancy(t *testing.T) {
	now := time.Now()
	a := &productAdaptiveShop{}
	adaptiveClassify(a, 10, 10, false, now)
	require.Equal(t, "1-10", a.Tier)
	adaptiveClassify(a, 11, 11, false, now)
	require.Equal(t, "1-10", a.Tier)
	adaptiveClassify(a, 10, 10, false, now)
	adaptiveClassify(a, 11, 11, false, now)
	require.Equal(t, "1-10", a.Tier)
	adaptiveClassify(a, 11, 11, false, now)
	require.Equal(t, "11-50", a.Tier)
	adaptiveClassify(a, 0, 7, false, now)
	require.Equal(t, "11-50", a.Tier)
	require.Equal(t, now.Add(10*time.Minute), a.CatalogStream.Next)
	require.Zero(t, a.EffectiveCount)
	for i, h := range []int{2, 4, 8, 12, 12} {
		adaptiveClassify(a, 0, 7, false, now)
		period, quote := adaptivePeriods(a)
		require.Equal(t, time.Duration(h)*time.Hour, period, "step %d", i)
		require.Zero(t, quote)
	}
	adaptiveClassify(a, 800, 800, false, now)
	require.Equal(t, "201-1000", a.Tier)
	require.Equal(t, now, a.QuoteStream.Next)
	for i, h := range []int{24, 72, 168, 168} {
		adaptiveClassify(a, 0, 0, true, now)
		period, quote := adaptivePeriods(a)
		require.Equal(t, time.Duration(h)*time.Hour, period, "step %d", i)
		require.Zero(t, quote)
	}
	adaptiveClassify(a, 1, 1, false, now)
	require.Equal(t, "1-10", a.Tier)
}

func TestPublicAdaptiveCatalogAtomicValidationAndCompatibility(t *testing.T) {
	now := time.Now().UTC()
	shop := PublicAccountImportShop{ID: "shop", URL: "https://wzyp.cn/shop/token"}
	cached := adaptiveTestCache(shop.ID, now)
	legacy := validPublicProductSyncRequest(shop.ID, "", now)
	p, err := normalizePublicProductSyncItem(shop, legacy.Products[0], now.Format(time.RFC3339Nano))
	require.NoError(t, err)
	cached.Products = []PublicAccountImportProduct{p}
	router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
	for _, change := range []func(*productCatalogSubmission){func(r *productCatalogSubmission) { r.Complete = false }, func(r *productCatalogSubmission) { r.SourceCount = 1001 }, func(r *productCatalogSubmission) { r.SourceCount = 2 }, func(r *productCatalogSubmission) { r.Products[0].Price = nil }, func(r *productCatalogSubmission) { r.Products = append(r.Products, r.Products[0]); r.SourceCount = 2 }} {
		req := adaptiveTestCatalog(shop.ID, 1, now)
		change(&req)
		r := performPublicProductRequest(t, router, "POST", "/products/sync-catalog", req)
		require.Equal(t, 400, r.Code, r.Body.String())
		require.Equal(t, cached, publicProductCache.Shops[shop.ID])
	}
	req := adaptiveTestCatalog(shop.ID, 2, now)
	r := performPublicProductRequest(t, router, "POST", "/products/sync-catalog", req)
	require.Equal(t, 200, r.Code, r.Body.String())
	saved := publicProductCache.Shops[shop.ID]
	require.Equal(t, uint64(1), saved.Adaptive.Generation)
	require.Len(t, adaptiveVisibleProducts(saved), 2)
	require.Nil(t, adaptiveVisibleProducts(saved)[0].PayablePrice)
	require.True(t, adaptiveVisibleProducts(saved)[0].QuotePending)
	require.Equal(t, cached.Products, saved.Products, "incomplete quotes must leave rollback snapshot intact")
	require.Equal(t, 409, performPublicProductRequest(t, router, "POST", "/products/sync-catalog", req).Code)
	publicProductCacheLoaded = false
	require.NoError(t, loadPublicProductCacheLocked())
	require.Equal(t, saved, publicProductCache.Shops[shop.ID])
	list := performPublicProductRequest(t, router, "GET", "/products", nil)
	products := decodePublicProductResponse[PublicAccountImportProductsResponse](t, list.Body.Bytes())
	require.Len(t, products.Products, 2)
}

func TestPublicAdaptiveQuotePartialLateMetadataAndNewerQuote(t *testing.T) {
	now := time.Now().UTC()
	shop := PublicAccountImportShop{ID: "shop", URL: "https://wzyp.cn/shop/token"}
	cached := adaptiveTestCache(shop.ID, now)
	require.NoError(t, applyProductCatalog(&cached, shop, adaptiveTestCatalog(shop.ID, 3, now), now))
	entries := cached.Adaptive.Catalog
	cached.SyncAttemptID = "quote"
	cached.Adaptive.Lease = &productAdaptiveLease{Kind: "quote_batch", Started: now.Add(-time.Second), Generation: 1, Weight: 1, Period: 3600, Versions: map[string]string{entries[0].GoodsKey: entries[0].MetadataVersion, entries[1].GoodsKey: entries[1].MetadataVersion, entries[2].GoodsKey: entries[2].MetadataVersion}}
	router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
	req := productQuoteSubmission{ShopID: shop.ID, AttemptID: "quote", Generation: 1, Products: []productQuoteResult{{GoodsKey: entries[0].GoodsKey, MetadataVersion: entries[0].MetadataVersion, PayablePrice: float64Pointer(9), VerifiedAt: now.Format(time.RFC3339Nano)}}, Cost: productCost{Requests: 3, Retries: 1, LaneSeconds: 4}, FailureKind: "network", Error: "network failed"}
	r := performPublicProductRequest(t, router, "POST", "/products/sync-quotes", req)
	require.Equal(t, 200, r.Code, r.Body.String())
	saved := publicProductCache.Shops[shop.ID]
	require.Equal(t, 9.0, *saved.Adaptive.Catalog[0].Product.PayablePrice)
	require.Nil(t, saved.Adaptive.Catalog[1].Product.PayablePrice)
	require.Empty(t, saved.Products)
	require.Equal(t, 1, saved.Adaptive.Failures)
	// A quote from the previous metadata version cannot replace current data.
	saved.SyncAttemptID = "late"
	saved.Error = ""
	saved.SyncStartedAt = now.Format(time.RFC3339Nano)
	saved.SyncHeartbeatAt = saved.SyncStartedAt
	saved.Adaptive.Lease = cached.Adaptive.Lease
	saved.Adaptive.Catalog[1].MetadataVersion = "changed"
	saved.Adaptive.Catalog = saved.Adaptive.Catalog[:2]
	late := req
	late.FailureKind = ""
	late.AttemptID = "late"
	late.Products = append(late.Products, productQuoteResult{GoodsKey: entries[1].GoodsKey, MetadataVersion: entries[1].MetadataVersion, PayablePrice: float64Pointer(999), VerifiedAt: now.Format(time.RFC3339Nano)}, productQuoteResult{GoodsKey: entries[2].GoodsKey, MetadataVersion: entries[2].MetadataVersion, PayablePrice: float64Pointer(888), VerifiedAt: now.Format(time.RFC3339Nano)})
	publicProductCache.Shops[shop.ID] = saved
	r = performPublicProductRequest(t, router, "POST", "/products/sync-quotes", late)
	require.Equal(t, 200, r.Code, r.Body.String())
	saved = publicProductCache.Shops[shop.ID]
	require.Len(t, saved.Adaptive.Catalog, 2)
	require.Nil(t, saved.Adaptive.Catalog[1].Product.PayablePrice)
	require.Equal(t, 9.0, *saved.Adaptive.Catalog[0].Product.PayablePrice)
}

func TestPublicAdaptiveSaveFailureAndReadOnlyAuthentication(t *testing.T) {
	now := time.Now().UTC()
	shop := PublicAccountImportShop{ID: "shop", URL: "https://wzyp.cn/shop/token"}
	cached := adaptiveTestCache(shop.ID, now)
	router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
	for _, path := range []string{"/products/sync-catalog", "/products/sync-quotes", "/products/sync-resources"} {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest("POST", path, strings.NewReader("{}")))
		require.Equal(t, http.StatusUnauthorized, r.Code)
	}
	before := clonePublicAccountImportProductStore(publicProductCache)
	for range 2 {
		r := performPublicProductRequest(t, router, "GET", "/products/sync-control", nil)
		require.Equal(t, 200, r.Code)
	}
	require.Equal(t, before, publicProductCache)
	bad := filepath.Join(t.TempDir(), "directory")
	require.NoError(t, os.Mkdir(bad, 0700))
	t.Setenv(publicAccountImportProductsFileEnv, bad)
	r := performPublicProductRequest(t, router, "POST", "/products/sync-catalog", adaptiveTestCatalog(shop.ID, 1, now))
	require.Equal(t, 500, r.Code, r.Body.String())
	require.Equal(t, before, publicProductCache)
}

func TestPublicAdaptiveFairSelectionAndBudgetRecovery(t *testing.T) {
	now := time.Now().UTC()
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{}, Adaptive: &productAdaptiveState{Mode: "adaptive", Multiplier: 1, RequestBudget: 1, LaneBudget: 4, RequestTokens: 100, LaneTokens: 1000, ProbeTokens: 100}}
	shops := []PublicAccountImportShop{{ID: "small", URL: "https://wzyp.cn/shop/small"}, {ID: "large", URL: "https://wzyp.cn/shop/large"}}
	for i, shop := range shops {
		c := adaptiveTestCache(shop.ID, now)
		n := 1
		if i == 1 {
			n = 800
		}
		require.NoError(t, applyProductCatalog(&c, shop, adaptiveTestCatalog(shop.ID, n, now), now))
		c.SyncAttemptID = ""
		c.SyncStartedAt = ""
		c.SyncHeartbeatAt = ""
		c.Adaptive.Lease = nil
		c.Adaptive.FirstQuoteAt = now
		c.Adaptive.CatalogStream.Next = now.Add(time.Hour)
		c.Adaptive.QuoteStream.Next = now
		c.Adaptive.QuoteStream.Cost = productCost{Samples: 1, Requests: 1.05, LaneSeconds: 1}
		store.Shops[shop.ID] = c
	}
	control := defaultPublicAccountImportSyncControl()
	control.Mode = "adaptive"
	jobs := adaptiveDispatch(&store, shops, control, now)
	require.Len(t, jobs, 1)
	second := adaptiveDispatch(&store, shops, control, now)
	require.Len(t, second, 1)
	require.NotEqual(t, jobs[0].ShopID, second[0].ShopID)
	require.Empty(t, adaptiveDispatch(&store, shops, control, now), "at most two quote jobs and one automatic job per shop")
	for _, job := range append(jobs, second...) {
		require.Equal(t, "quote_batch", job.Scope)
		require.LessOrEqual(t, len(job.Products), 20)
	}
	// Restart credit is discarded; no missed-period replay burst.
	s := store.Adaptive
	s.LastTick = now.Add(-time.Hour)
	s.Mode = "adaptive"
	s.Multiplier = 4
	s.RequestTokens = 10000
	w := PublicAccountImportProductSyncWorkerStatus{UpdatedAt: now.Format(time.RFC3339Nano), AvailableLaneCount: 6, AdaptiveRatePerSecond: 2}
	s.History = []productResourceObservation{{At: now, WorkerMemory: .5, HostAvailable: .5, DiskFree: 2 << 30, Lanes: 6}}
	adaptiveAdvance(&store, shops, control, w, now)
	require.Zero(t, s.RequestTokens)
	require.Zero(t, s.LaneTokens)
	s.LastTick = now.Add(time.Hour)
	adaptiveAdvance(&store, shops, control, w, now)
	require.Zero(t, s.RequestTokens)
}

func TestPublicAdaptivePriorityQuotaAndCostEMA(t *testing.T) {
	a := &productAdaptiveShop{}
	now := time.Now()
	for i := 0; i < 100; i++ {
		a.Catalog = append(a.Catalog, productCatalogEntry{GoodsKey: fmt.Sprint(i), Sellable: true, Priority: i < 70, LastQuoteAttemptAt: now.Add(time.Duration(i) * time.Second)})
	}
	selected := adaptiveSelectProducts(a, 20, now)
	priority := 0
	for _, i := range selected {
		if a.Catalog[i].Priority {
			priority++
		}
	}
	require.Equal(t, 10, priority)
	for i := range a.Catalog {
		a.Catalog[i].Priority = true
	}
	require.Len(t, adaptiveSelectProducts(a, 20, now), 20)
	c := productCost{}
	c.observe(productCost{Requests: 10, LaneSeconds: 100})
	c.observe(productCost{Requests: 20, LaneSeconds: 200})
	require.Equal(t, 12.0, c.Requests)
	require.Equal(t, 120.0, c.LaneSeconds)
}

func TestPublicAdaptiveOldWorkerAndShadowDoNotClaimNewJobs(t *testing.T) {
	shop := PublicAccountImportShop{ID: "shop", URL: "https://wzyp.cn/shop/token"}
	router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{})
	control := defaultPublicAccountImportSyncControl()
	control.Mode = "adaptive"
	require.NoError(t, savePublicAccountImportSyncControlLocked(control))
	r := performPublicProductRequest(t, router, "GET", "/products/sync-job", nil)
	require.Equal(t, 200, r.Code)
	out := decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, r.Body.Bytes())
	require.Nil(t, out.Job)
	require.Empty(t, publicProductCache.Shops)
	control.Mode = "shadow"
	require.NoError(t, savePublicAccountImportSyncControlLocked(control))
	publicProductCache.Adaptive = &productAdaptiveState{Mode: "shadow", History: []productResourceObservation{{At: time.Now().UTC()}}}
	publicProductLastJobAt = time.Time{}
	r = performPublicProductRequest(t, router, "GET", "/products/sync-job?capabilities=catalog,quote_batch", nil)
	out = decodePublicProductResponse[PublicAccountImportProductSyncJobResponse](t, r.Body.Bytes())
	require.NotNil(t, out.Job)
	require.Empty(t, out.Job.Scope)
	encoded, err := json.Marshal(publicProductCache)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "catalog_stream")
}

func TestPublicAdaptiveResourceHoldsRecoveryAndMultiplier(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{}, Adaptive: &productAdaptiveState{Mode: "adaptive", Multiplier: 4, LastTick: now}}
	control := defaultPublicAccountImportSyncControl()
	control.Mode = "adaptive"
	w := PublicAccountImportProductSyncWorkerStatus{AvailableLaneCount: 6, AdaptiveRatePerSecond: 2}
	step := func(v productResourceObservation) {
		w.UpdatedAt = v.At.Format(time.RFC3339Nano)
		store.Adaptive.History = append(store.Adaptive.History, v)
		if len(store.Adaptive.History) > 32 {
			store.Adaptive.History = store.Adaptive.History[1:]
		}
		adaptiveAdvance(&store, nil, control, w, v.At)
	}
	good := func(at time.Time) productResourceObservation {
		return productResourceObservation{At: at, WorkerMemory: .7, HostAvailable: .4, CPU: .3, DiskFree: 2 << 30, Lanes: 6}
	}
	bad := good(now)
	bad.WorkerMemory = .96
	step(bad)
	bad.At = now.Add(time.Minute)
	step(bad)
	require.Contains(t, store.Adaptive.AutomaticHold, "95%")
	for i := 2; i <= 17; i++ {
		step(good(now.Add(time.Duration(i) * time.Minute)))
	}
	require.Empty(t, store.Adaptive.AutomaticHold)
	before := store.Adaptive.Multiplier
	for i := 18; i <= 33; i++ {
		step(good(now.Add(time.Duration(i) * time.Minute)))
	}
	require.GreaterOrEqual(t, store.Adaptive.Multiplier, before*.9)
	require.Less(t, store.Adaptive.Multiplier, before)
	bad = good(now.Add(34 * time.Minute))
	bad.HostAvailable = .09
	step(bad)
	require.Contains(t, store.Adaptive.AutomaticHold, "10%")
	for i := 35; i <= 50; i++ {
		step(good(now.Add(time.Duration(i) * time.Minute)))
	}
	require.Empty(t, store.Adaptive.AutomaticHold)
	// A short four-lane dip freezes admission but does not discard the memory
	// and upstream recovery window; the following five-lane sample can reopen.
	w.AvailableLaneCount = 4
	step(good(now.Add(51 * time.Minute)))
	require.NotEmpty(t, store.Adaptive.AdmissionHold)
	w.AvailableLaneCount = 5
	step(good(now.Add(52 * time.Minute)))
	require.Empty(t, store.Adaptive.AdmissionHold)
	adaptiveAdvance(&store, nil, control, w, now.Add(56*time.Minute))
	require.Contains(t, store.Adaptive.AutomaticHold, "stale")
}

func TestPublicAdaptiveCapacityBoundaryAndFailureIsolation(t *testing.T) {
	now := time.Now().UTC()
	shops := []PublicAccountImportShop{}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{}, Adaptive: &productAdaptiveState{Mode: "adaptive", Multiplier: 4, LastTick: now, History: []productResourceObservation{{At: now, WorkerMemory: .7, HostAvailable: .4, DiskFree: 2 << 30, Lanes: 6}}}}
	for i := 0; i < 500; i++ {
		id := fmt.Sprint(i)
		shops = append(shops, PublicAccountImportShop{ID: id})
		store.Shops[id] = publicAccountImportProductShopCache{Adaptive: &productAdaptiveShop{Tier: "201-1000", EffectiveCount: 1000, SourceCount: 1000, QuoteStream: productStream{Cost: productCost{Samples: 1, Requests: 2, LaneSeconds: 20}}}}
	}
	c := defaultPublicAccountImportSyncControl()
	c.Mode = "adaptive"
	w := PublicAccountImportProductSyncWorkerStatus{UpdatedAt: now.Format(time.RFC3339Nano), AvailableLaneCount: 6, AdaptiveRatePerSecond: .5}
	adaptiveAdvance(&store, shops, c, w, now)
	require.Equal(t, 4.0, store.Adaptive.Multiplier)
	require.Greater(t, store.Adaptive.RequiredMultiplier, 4.0)
	require.Contains(t, store.Adaptive.AdmissionHold, "boundary")
	cache := adaptiveTestCache("new", now)
	adaptiveRecordFailure(&cache, "network", "timeout", now, time.Minute)
	require.Empty(t, cache.Adaptive.Exception)
	adaptiveRecordFailure(&cache, "verification", "challenge", now, time.Minute)
	require.Equal(t, "verification", cache.Adaptive.Exception)
	require.Equal(t, now.Add(6*time.Hour), cache.Adaptive.RetryAt)
}

func TestPublicAdaptiveMigrationRetainsMatchingHistoricalQuote(t *testing.T) {
	now := time.Now().UTC()
	shop := PublicAccountImportShop{ID: "shop", URL: "https://wzyp.cn/shop/token"}
	cached := adaptiveTestCache(shop.ID, now)
	oldTime := now.Add(-2 * time.Hour)
	item := validPublicProductSyncRequest(shop.ID, "", now).Products[0]
	p, err := normalizePublicProductSyncItem(shop, item, now.Format(time.RFC3339Nano))
	require.NoError(t, err)
	p.QuoteVerifiedAt = oldTime.Format(time.RFC3339Nano)
	cached.Products = []PublicAccountImportProduct{p}
	req := adaptiveTestCatalog(shop.ID, 1, now)
	req.Products[0].PublicAccountImportProductSyncItem = item
	require.NoError(t, applyProductCatalog(&cached, shop, req, now))
	e := cached.Adaptive.Catalog[0]
	require.Equal(t, e.MetadataVersion, e.QuoteVersion)
	require.Equal(t, oldTime, e.LastQuoteAttemptAt)
	require.Equal(t, p.QuoteVerifiedAt, e.Product.QuoteVerifiedAt)
	adaptiveUpdateCompatibility(&cached, now)
	require.Equal(t, p.QuoteVerifiedAt, cached.Products[0].QuoteVerifiedAt)
	_ = newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached}})
	changed := clonePublicAccountImportProductShopCache(cached)
	changed.Adaptive.Catalog[0].Product.Name = "new catalog"
	require.Equal(t, "new catalog", adaptiveVisibleProducts(changed)[0].Name)
	control := defaultPublicAccountImportSyncControl()
	require.NoError(t, savePublicAccountImportSyncControlLocked(control))
	require.Equal(t, cached.Products, adaptiveVisibleProducts(changed), "legacy mode reads the rollback snapshot")
}
