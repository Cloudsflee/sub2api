package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Discrete event replay uses the production dispatcher and cost reconciliation.
// Upstream is deterministic and synthetic; this test never performs HTTP calls.
func TestPublicAdaptiveDiscreteEventReplay(t *testing.T) {
	var fixture struct {
		Counts []int `json:"counts"`
	}
	data, err := os.ReadFile("testdata/adaptive-shop-distribution.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	synthetic := []int{}
	for i := 0; i < 500; i++ {
		synthetic = append(synthetic, []int{0, 1, 3, 7, 10, 11, 25, 50, 51, 100, 200, 201, 400, 800, 1000}[i%15])
	}
	for _, scenario := range []struct {
		name    string
		counts  []int
		disrupt bool
	}{{"actual", fixture.Counts, false}, {"500_mixed", synthetic, false}, {"recovery_restart_manual", fixture.Counts, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
			now := start
			store := publicAccountImportProductStore{Version: 1, Shops: map[string]publicAccountImportProductShopCache{}, Adaptive: &productAdaptiveState{Mode: "adaptive", Multiplier: 4, BatchLimit: 2, LastTick: start, ValidatedSince: start}}
			shops := []PublicAccountImportShop{}
			for i, n := range scenario.counts {
				shop := PublicAccountImportShop{ID: fmt.Sprintf("shop-%03d", i), URL: fmt.Sprintf("https://wzyp.cn/shop/%d", i)}
				shops = append(shops, shop)
				c := adaptiveTestCache(shop.ID, now)
				require.NoError(t, applyProductCatalog(&c, shop, adaptiveTestCatalog(shop.ID, n, now), now))
				c.SyncAttemptID = ""
				c.SyncStartedAt = ""
				c.SyncHeartbeatAt = ""
				a := c.Adaptive
				a.Lease = nil
				a.FirstQuoteAt = start
				a.HandoffApplied = true
				a.CatalogStream.Cost = productCost{Samples: 1, Requests: float64(5 + (n / 100)), LaneSeconds: float64(5 + (n / 100))}
				a.QuoteStream.Cost = productCost{Samples: 1, Requests: 1.05, LaneSeconds: 1.05}
				a.CatalogStream.Next = time.Time{}
				a.QuoteStream.Next = time.Time{}
				if n == 0 {
					adaptiveClassify(a, 0, 0, false, now)
				}
				for k := range a.Catalog {
					a.Catalog[k].Priority = false
					a.Catalog[k].LastQuoteAttemptAt = start
					a.Catalog[k].QuoteVersion = a.Catalog[k].MetadataVersion
					a.Catalog[k].Product.PayablePrice = float64Pointer(4)
					a.Catalog[k].Product.UnitPrice = float64Pointer(2)
					a.Catalog[k].Product.QuoteVerifiedAt = start.Format(time.RFC3339Nano)
				}
				store.Shops[shop.ID] = c
			}
			control := defaultPublicAccountImportSyncControl()
			control.Mode = "adaptive"
			control.IntervalMultiplier = 4
			seen := map[string]int{}
			quoteRequests := 0.0
			requestBudget := 0.0
			catalogJobs := 0
			quoteJobs := 0
			maxActive := 0
			type running struct {
				job  PublicAccountImportProductSyncJob
				done time.Time
				cost productCost
			}
			active := []running{}
			for step := 0; step < 3*24*360; step++ {
				now = start.Add(time.Duration(step*10) * time.Second)
				w := PublicAccountImportProductSyncWorkerStatus{UpdatedAt: now.Format(time.RFC3339Nano), AvailableLaneCount: 6, AdaptiveRatePerSecond: 2}
				if len(scenario.counts) == 500 {
					w.AdaptiveRatePerSecond = 3
				}
				if scenario.disrupt && step >= 360 && step < 390 {
					w.AvailableLaneCount = 3
					w.AdaptiveRatePerSecond = .5
					w.GlobalPressureUntil = now.Add(time.Minute).Format(time.RFC3339Nano)
				}
				if step%6 == 0 {
					obs := productResourceObservation{At: now, WorkerID: "worker", WorkerMemory: .7, HostAvailable: .4, CPU: .3, DiskFree: 4 << 30, Lanes: w.AvailableLaneCount, Pressure: adaptiveUpstream(w, now)}
					store.Adaptive.History = append(store.Adaptive.History, obs)
					if len(store.Adaptive.History) > 32 {
						store.Adaptive.History = store.Adaptive.History[1:]
					}
				}
				if scenario.disrupt && step == 500 {
					encoded, e := json.Marshal(store)
					require.NoError(t, e)
					require.NoError(t, json.Unmarshal(encoded, &store))
					store.Adaptive.LastTick = now.Add(-time.Hour)
				}
				if scenario.disrupt && step == 700 {
					store.Adaptive.LastTick = now.Add(time.Hour)
				}
				if scenario.disrupt && step == 900 {
					for _, shop := range shops[:min(8, len(shops))] {
						c := store.Shops[shop.ID]
						c.RefreshRequestedAt = now.Format(time.RFC3339Nano)
						store.Shops[shop.ID] = c
					}
				}
				remaining := []running{}
				for _, run := range active {
					if run.done.After(now) {
						remaining = append(remaining, run)
						continue
					}
					c := store.Shops[run.job.ShopID]
					a := c.Adaptive
					if run.job.Scope == "quote_batch" {
						req := productQuoteSubmission{Generation: run.job.CatalogGeneration, Cost: run.cost, Products: []productQuoteResult{}}
						for _, p := range run.job.Products {
							req.Products = append(req.Products, productQuoteResult{GoodsKey: p.GoodsKey, MetadataVersion: p.MetadataVersion, PayablePrice: float64Pointer(4), VerifiedAt: now.Format(time.RFC3339Nano)})
						}
						_, e := applyProductQuotes(&c, req, now)
						require.NoError(t, e)
						seen[run.job.ShopID] += len(req.Products)
						quoteJobs++
					} else {
						a.CatalogAt = now
						catalogJobs++
						if scenario.disrupt && step > 1000 && a.EffectiveCount == 0 {
							r := adaptiveTestCatalog(run.job.ShopID, 3, now)
							require.NoError(t, applyProductCatalog(&c, PublicAccountImportShop{ID: run.job.ShopID, URL: "https://wzyp.cn/shop/recovered"}, r, now))
						}
					}
					adaptiveFinish(&store, &c, run.cost, len(run.job.Products), now)
					store.Shops[run.job.ShopID] = c
				}
				active = remaining
				adaptiveAdvance(&store, shops, control, w, now)
				requestBudget += store.Adaptive.RequestBudget * 10
				for range 6 {
					jobs := adaptiveDispatch(&store, shops, control, now)
					if len(jobs) == 0 {
						break
					}
					job := jobs[0]
					a := store.Shops[job.ShopID].Adaptive
					requests := 5.0
					if job.Scope == "quote_batch" {
						require.Greater(t, a.EffectiveCount, 0, "dormant shops must not be quoted")
						requests = float64(len(job.Products) + 1)
					}
					cost := productCost{Requests: requests, LaneSeconds: requests}
					if job.Automatic {
						quoteRequests += requests
					}
					active = append(active, running{job, now.Add(time.Duration(requests) * time.Second), cost})
				}
				maxActive = max(maxActive, len(active))
				require.LessOrEqual(t, len(active), 6)
				// Estimated credits can be corrected after a request completes; the
				// bounded initial credit and one set of in-flight jobs are explicit.
				require.LessOrEqual(t, quoteRequests, requestBudget+126)
			}
			for _, shop := range shops {
				a := store.Shops[shop.ID].Adaptive
				if a.EffectiveCount > 0 {
					require.Greater(t, seen[shop.ID], 0, "starved shop %s", shop.ID)
				}
			}
			t.Logf("shops=%d catalog_jobs=%d quote_batches=%d requests=%.0f budget=%.0f multiplier=%.3f required=%.3f max_active=%d", len(shops), catalogJobs, quoteJobs, quoteRequests, requestBudget, store.Adaptive.Multiplier, store.Adaptive.RequiredMultiplier, maxActive)
			if !scenario.disrupt {
				for _, shop := range shops {
					a := store.Shops[shop.ID].Adaptive
					if a.EffectiveCount == 0 {
						continue
					}
					_, period := adaptivePeriods(a)
					for _, e := range a.Catalog {
						require.LessOrEqual(t, now.Sub(e.LastQuoteAttemptAt), time.Duration(float64(period)*store.Adaptive.Multiplier)+5*time.Minute, "quote coverage %s", shop.ID)
					}
				}
			}
		})
	}
}
