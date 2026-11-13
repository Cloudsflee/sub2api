package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func adaptiveScopeWorker(t *testing.T, now time.Time) {
	t.Helper()
	w := publicAccountImportProductSyncWorkerStatusFile{State: "idle", UpdatedAt: now.Format(time.RFC3339Nano), SyncConcurrency: 6, AdaptiveRatePerSecond: 2}
	for i := 0; i < 6; i++ {
		w.Lanes = append(w.Lanes, publicAccountImportProductSyncWorkerLaneStatusFile{Index: i, State: "idle", ContextReady: true, UpdatedAt: w.UpdatedAt})
	}
	data, err := json.Marshal(w)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv(publicAccountImportProductSyncStatusFileEnv), data, 0600))
}

func TestPublicAdaptiveResourceScopeAndSaveRollback(t *testing.T) {
	for _, saveFailure := range []bool{false, true} {
		name := "commit"
		if saveFailure {
			name = "save failure"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC()
			shops := []PublicAccountImportShop{
				{ID: "due", URL: "https://wzyp.cn/shop/due"},
				{ID: "isolated", URL: "https://wzyp.cn/shop/isolated"},
				{ID: "unsupported-one", URL: "https://example.org/shop/one"},
				{ID: "unsupported-two", URL: "https://example.org/shop/two"},
			}
			store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{}, Adaptive: &productAdaptiveState{Mode: "adaptive", Multiplier: 4, LastTick: now, PressureSince: now.Add(-time.Hour)}}
			for _, shop := range shops {
				store.Shops[shop.ID] = publicAccountImportProductShopCache{ShopID: shop.ID, Adaptive: &productAdaptiveShop{Tier: "empty", HandoffApplied: true, CatalogStream: productStream{Next: now.Add(-time.Hour), Cost: productCost{Samples: 1, Requests: 100000, LaneSeconds: 100000}}}}
			}
			store.Shops["due"].Adaptive.CatalogStream = productStream{Next: now.Add(-30 * time.Second)}
			store.Shops["isolated"].Adaptive.CatalogStream.Cost = productCost{}
			legacy := authoritativeProductCache("unsupported-one", now, 9).Products
			c := store.Shops["unsupported-one"]
			c.Products = legacy
			store.Shops["unsupported-one"] = c
			router := newPublicProductTestRouter(t, shops, store)
			adaptiveScopeWorker(t, now)
			control := defaultPublicAccountImportSyncControl()
			control.Mode = "adaptive"
			control.QuarantinedShops["isolated"] = "operator isolation"
			require.NoError(t, savePublicAccountImportSyncControlLocked(control))
			before := clonePublicAccountImportProductStore(publicProductCache)
			if saveFailure {
				bad := filepath.Join(t.TempDir(), "directory")
				require.NoError(t, os.Mkdir(bad, 0700))
				t.Setenv(publicAccountImportProductsFileEnv, bad)
				// The control path is derived from the product path; keep adaptive
				// mode enabled while only the product-store write is made invalid.
				require.NoError(t, savePublicAccountImportSyncControlLocked(control))
			}
			observation := productResourceObservation{At: now, WorkerID: "worker", WorkerMemory: .7, HostAvailable: .4, CPU: .3, DiskFree: 2 << 30, Due: 999, OldestDueSeconds: 9999}
			r := performPublicProductRequest(t, router, "POST", "/products/sync-resources", observation)
			if saveFailure {
				require.Equal(t, 500, r.Code, r.Body.String())
				require.Equal(t, before, publicProductCache, "failed save must restore cleanup and queue recovery epoch")
				return
			}
			require.Equal(t, 200, r.Code, r.Body.String())
			s := publicProductCache.Adaptive
			require.Equal(t, 1, s.History[0].Due, "only the supported, non-isolated due shop counts")
			require.InDelta(t, 30, s.History[0].OldestDueSeconds, 3)
			require.Equal(t, 1.0, s.RequiredMultiplier, "unsupported cost history must not inflate the capacity estimate")
			require.Nil(t, publicProductCache.Shops["unsupported-one"].Adaptive)
			require.Nil(t, publicProductCache.Shops["unsupported-two"].Adaptive)
			require.Equal(t, legacy, publicProductCache.Shops["unsupported-one"].Products)
			require.False(t, s.QueueScopeSince.IsZero())
			require.True(t, s.PressureSince.IsZero())
			require.NotEmpty(t, s.AdmissionHold)
			savedShops, err := snapshotPublicAccountImportShops()
			require.NoError(t, err)
			require.Len(t, savedShops, 4)
			savedControl, err := loadPublicAccountImportSyncControlLocked()
			require.NoError(t, err)
			require.Equal(t, "operator isolation", savedControl.QuarantinedShops["isolated"])
			publicProductCacheLoaded = false
			require.NoError(t, loadPublicProductCacheLocked())
			require.Equal(t, s.QueueScopeSince, publicProductCache.Adaptive.QueueScopeSince)
			require.Nil(t, publicProductCache.Shops["unsupported-one"].Adaptive)
		})
	}
}

func TestPublicAdaptiveScopeRecoveryWaitsForCorrectedQueue(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	shops := []PublicAccountImportShop{{ID: "paused", URL: "https://wzyp.cn/shop/paused", ImportBatch: 1}, {ID: "unsupported", URL: "https://example.org/shop/unsupported"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{
		"paused":      {Adaptive: &productAdaptiveShop{Tier: "empty", HandoffApplied: true, PauseReason: "request pressure at 4x", ResumeAfter: now.Add(-time.Minute), CatalogStream: productStream{Next: now.Add(time.Hour)}}},
		"unsupported": {Adaptive: &productAdaptiveShop{Tier: "empty", CatalogStream: productStream{Next: now.Add(-time.Hour)}}},
	}, Adaptive: &productAdaptiveState{Mode: "adaptive", Multiplier: 4, LastTick: now, PressureSince: now.Add(-time.Hour), LastScaleChange: now.Add(-time.Hour)}}
	good := func(at time.Time) productResourceObservation {
		return productResourceObservation{At: at, WorkerMemory: .7, HostAvailable: .4, CPU: .3, DiskFree: 2 << 30, Lanes: 6}
	}
	for i := -16; i < 0; i++ {
		v := good(now.Add(time.Duration(i) * time.Minute))
		v.Due, v.OldestDueSeconds = 2, 3600
		store.Adaptive.History = append(store.Adaptive.History, v)
	}
	control := defaultPublicAccountImportSyncControl()
	control.Mode = "adaptive"
	w := PublicAccountImportProductSyncWorkerStatus{UpdatedAt: now.Format(time.RFC3339Nano), AvailableLaneCount: 6, AdaptiveRatePerSecond: 2}
	adaptiveAdvance(&store, shops, control, w, now)
	require.Len(t, store.Adaptive.History, 16, "retain host and upstream evidence")
	require.Equal(t, now, store.Adaptive.QueueScopeSince)
	require.True(t, store.Adaptive.PressureSince.IsZero(), "do not carry the false backlog pressure timer")
	for i := 0; i <= 15; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		store.Adaptive.History = append(store.Adaptive.History, good(at))
		w.UpdatedAt = at.Format(time.RFC3339Nano)
		adaptiveAdvance(&store, shops, control, w, at)
		if i < 15 {
			require.NotEmpty(t, store.Shops["paused"].Adaptive.PauseReason)
			require.Equal(t, 4.0, store.Adaptive.Multiplier)
		}
	}
	require.Empty(t, store.Shops["paused"].Adaptive.PauseReason)
	require.Equal(t, "observing restored batch for 15 minutes", store.Adaptive.AdmissionHold)
	require.InDelta(t, 3.6, store.Adaptive.Multiplier, .001)
}

func TestPublicAdaptiveUnsupportedStateCannotDispatchOrBlockAdmission(t *testing.T) {
	now := time.Now().UTC()
	shops := []PublicAccountImportShop{{ID: "supported", URL: "https://wzyp.cn/shop/supported"}, {ID: "unsupported", URL: "https://example.org/shop/unsupported"}}
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{}, Adaptive: &productAdaptiveState{Mode: "adaptive", Multiplier: 1, RequestBudget: 1, LaneBudget: 4, RequestTokens: 60, LaneTokens: 240, ProbeTokens: 6, LastTick: now, History: []productResourceObservation{{At: now}}}}
	store.Shops["supported"] = publicAccountImportProductShopCache{Adaptive: &productAdaptiveShop{Tier: "empty", CatalogStream: productStream{Next: now.Add(-time.Minute)}}}
	unsupported := adaptiveTestCache("unsupported", now)
	unsupported.Adaptive.Generation = 1
	unsupported.Adaptive.PauseReason = "stale unsupported pause"
	unsupported.Adaptive.CatalogStream.Cost = productCost{Samples: 1, Requests: 100000, LaneSeconds: 100000}
	store.Shops["unsupported"] = unsupported
	// Retain historical catalogs/in-flight metadata even if the source becomes unsupported.
	require.Len(t, adaptiveReconcileShopScope(&store, shops, now), 1)
	require.Equal(t, unsupported, store.Shops["unsupported"])
	control := defaultPublicAccountImportSyncControl()
	control.Mode = "adaptive"
	jobs := adaptiveDispatch(&store, shops, control, now)
	require.Len(t, jobs, 1)
	require.Equal(t, "supported", jobs[0].ShopID)
	require.Equal(t, unsupported, store.Shops["unsupported"])
	newPublicProductTestRouter(t, shops, store)
	adaptiveScopeWorker(t, now)
	_, reason, err := adaptiveAdmission()
	require.NoError(t, err)
	require.Empty(t, reason, "an unsupported source must not block new supported shops")
}

func TestPublicAdaptiveScopePreservesRealPressureAndHardHold(t *testing.T) {
	now := time.Now().UTC()
	pressureAt := now.Add(-time.Hour)
	store := publicAccountImportProductStore{Shops: map[string]publicAccountImportProductShopCache{"unsupported": {Adaptive: &productAdaptiveShop{}}}, Adaptive: &productAdaptiveState{PressureSince: pressureAt, AutomaticHold: "new OOM", History: []productResourceObservation{{At: now, Pressure: true, WorkerOOM: 1}}}}
	adaptiveReconcileShopScope(&store, nil, now)
	require.Equal(t, pressureAt, store.Adaptive.PressureSince)
	require.Equal(t, "new OOM", store.Adaptive.AutomaticHold)
	require.True(t, store.Adaptive.History[0].Pressure)
	require.Nil(t, store.Shops["unsupported"].Adaptive)
}
