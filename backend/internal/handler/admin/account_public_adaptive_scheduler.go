package admin

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

type productAdaptiveShopStats struct {
	Tier           string      `json:"tier"`
	State          string      `json:"state"`
	EffectiveCount int         `json:"effective_count"`
	PendingQuotes  int         `json:"pending_quotes"`
	OverdueQuotes  int         `json:"overdue_quotes"`
	CatalogOverdue bool        `json:"catalog_overdue"`
	CatalogSeconds float64     `json:"catalog_seconds"`
	QuoteSeconds   float64     `json:"quote_seconds"`
	CatalogCost    productCost `json:"catalog_cost"`
	QuoteCost      productCost `json:"quote_cost"`
	NextCatalogAt  time.Time   `json:"next_catalog_at"`
	NextQuoteAt    time.Time   `json:"next_quote_at"`
	FirstCatalogAt time.Time   `json:"first_catalog_at"`
	FirstQuoteAt   time.Time   `json:"first_quote_at"`
	PauseReason    string      `json:"pause_reason,omitempty"`
	RetryAt        time.Time   `json:"retry_at"`
	ResumeAfter    time.Time   `json:"resume_after"`
}

func adaptiveStats(a *productAdaptiveShop, multiplier float64, now time.Time) *productAdaptiveShopStats {
	if a == nil {
		return nil
	}
	c, q := adaptivePeriods(a)
	s := &productAdaptiveShopStats{Tier: a.Tier, State: adaptiveSummary(a), EffectiveCount: a.EffectiveCount, CatalogSeconds: c.Seconds() * multiplier, QuoteSeconds: q.Seconds() * multiplier, CatalogCost: adaptiveCost(a, "catalog", nil), QuoteCost: adaptiveCost(a, "quote_batch", nil), NextCatalogAt: a.CatalogStream.Next, NextQuoteAt: a.QuoteStream.Next, FirstCatalogAt: a.FirstCatalogAt, FirstQuoteAt: a.FirstQuoteAt, PauseReason: a.PauseReason, RetryAt: a.RetryAt, ResumeAfter: a.ResumeAfter}
	s.CatalogOverdue = !a.CatalogStream.Next.After(now) && !a.RetryAt.After(now)
	for _, e := range a.Catalog {
		if e.Sellable {
			if !e.QuoteUnavailable && (e.QuoteVersion != e.MetadataVersion || e.Product.QuoteVerifiedAt == "") {
				s.PendingQuotes++
			}
			last := e.LastQuoteAttemptAt
			if last.IsZero() {
				last = a.CatalogAt
			}
			if q > 0 && now.Sub(last).Seconds() > s.QuoteSeconds {
				s.OverdueQuotes++
			}
		}
	}
	return s
}

func adaptiveUpstream(w PublicAccountImportProductSyncWorkerStatus, now time.Time) bool {
	return parsePublicAccountImportProductTimestamp(w.GlobalSilenceUntil).After(now) || parsePublicAccountImportProductTimestamp(w.GlobalPressureUntil).After(now) || w.GlobalPressureState == "pressured"
}

func adaptiveHistoryWindow(h []productResourceObservation, seconds float64, now time.Time, predicate func(productResourceObservation) bool) bool {
	if len(h) == 0 {
		return false
	}
	start := now.Add(-time.Duration(seconds) * time.Second)
	covered := false
	last := now
	for i := len(h) - 1; i >= 0; i-- {
		v := h[i]
		if last.Sub(v.At) > 90*time.Second || !predicate(v) {
			return false
		}
		last = v.At
		if !v.At.After(start) {
			covered = true
			break
		}
	}
	return covered
}

func adaptiveGrowing(h []productResourceObservation, now time.Time) bool {
	start := now.Add(-10 * time.Minute)
	last := -1
	first := -1
	lastIncrease := start
	covered := false
	for _, v := range h {
		if v.At.Before(start.Add(-time.Minute)) {
			continue
		}
		if first < 0 {
			first = v.Due
			covered = !v.At.After(start)
		}
		if last >= 0 && v.Due < last {
			return false
		}
		if v.Due > last {
			lastIncrease = v.At
		}
		if v.At.Sub(lastIncrease) > 2*time.Minute {
			return false
		}
		last = v.Due
	}
	return covered && last > first && last > 0
}

func adaptiveAdvance(store *publicAccountImportProductStore, shops []PublicAccountImportShop, control PublicAccountImportSyncControl, w PublicAccountImportProductSyncWorkerStatus, now time.Time) {
	if store.Adaptive == nil {
		store.Adaptive = &productAdaptiveState{Multiplier: 4, BatchLimit: 2}
	}
	shops = adaptiveReconcileShopScope(store, shops, now)
	s := store.Adaptive
	s.AdmissionCapacityLimit = adaptiveAdmissionCapacityLimit
	if s.Mode != control.Mode {
		s.Mode = control.Mode
		s.Multiplier = 4
		s.StableSince = time.Time{}
		s.ValidatedSince = now
		if control.Mode == "shadow" {
			s.ShadowSince = now
		}
		// Switching from shadow never carries simulated leases or accrued tokens.
		s.RequestTokens = 0
		s.LaneTokens = 0
		s.ProbeTokens = 0
		s.LastTick = now
		if control.Mode == "adaptive" {
			s.VirtualTime = 0
			for _, shop := range shops {
				c := store.Shops[shop.ID]
				if c.Adaptive != nil && c.Adaptive.Generation == 0 {
					c.Adaptive.CatalogStream.Next = time.Time{}
					c.Adaptive.QuoteStream.Next = time.Time{}
					c.Adaptive.CatalogStream.Finish = 0
					c.Adaptive.QuoteStream.Finish = 0
					store.Shops[shop.ID] = c
				}
			}
		}
	}
	for _, shop := range shops {
		c := store.Shops[shop.ID]
		if c.Adaptive == nil {
			c.Adaptive = &productAdaptiveShop{Established: len(c.Products) > 0, EffectiveCount: len(c.Products), SourceCount: c.SourceProductCount, Tier: adaptiveTier(len(c.Products))}
			store.Shops[shop.ID] = c
		}
		if control.Mode == "shadow" && c.Adaptive.Generation == 0 {
			c.Adaptive.EffectiveCount = len(c.Products)
			c.Adaptive.SourceCount = c.SourceProductCount
			c.Adaptive.Tier = adaptiveTier(len(c.Products))
		}
		if control.Mode == "adaptive" && !c.Adaptive.HandoffApplied {
			c.Adaptive.HandoffApplied = true
			c.Adaptive.PauseReason = control.PressurePausedShops[shop.ID]
			if c.Adaptive.PauseReason != "" {
				c.Adaptive.ResumeAfter = now.Add(15 * time.Minute)
			}
		}
		store.Adaptive.BatchNumber = max(store.Adaptive.BatchNumber, adaptiveShopBatch(shop, control))
	}
	rate := w.AdaptiveRatePerSecond
	s.RequestBudget = max(0, rate*.7)
	lanes := 0.0
	weight := 0.0
	for i, v := range s.History {
		if v.At.Before(now.Add(-15 * time.Minute)) {
			continue
		}
		end := now
		if i+1 < len(s.History) {
			end = s.History[i+1].At
		}
		seconds := min(90.0, max(0, end.Sub(v.At).Seconds()))
		lanes += float64(v.Lanes) * seconds
		weight += seconds
	}
	if weight > 0 {
		lanes /= weight
	} else {
		lanes = float64(w.AvailableLaneCount)
	}
	s.LaneBudget = lanes * .8
	history := adaptiveCostHistory(*store, shops)
	requests, occupancy := 0.0, 0.0
	quoteOccupancy := 0.0
	for _, shop := range shops {
		a := store.Shops[shop.ID].Adaptive
		if _, isolated := control.QuarantinedShops[shop.ID]; isolated || a.PauseReason != "" {
			continue
		}
		c, q := adaptivePeriods(a)
		for kind, period := range map[string]time.Duration{"catalog": c, "quote_batch": q} {
			if period <= 0 || (kind == "quote_batch" && a.EffectiveCount == 0) {
				continue
			}
			cost := adaptiveCost(a, kind, history)
			requests += cost.Requests / period.Seconds()
			occupancy += cost.LaneSeconds / period.Seconds()
			if kind == "quote_batch" {
				quoteOccupancy += cost.LaneSeconds / period.Seconds()
			}
		}
	}
	s.RequiredMultiplier = max(1, float64(control.IntervalMultiplier), requests/max(.001, s.RequestBudget), occupancy/max(.001, s.LaneBudget), quoteOccupancy/max(.001, min(2, s.LaneBudget)))
	target := min(4, s.RequiredMultiplier)
	old := max(1, s.Multiplier)
	upstream := adaptiveUpstream(w, now)
	stableSample := false
	if len(s.History) > 0 {
		v := s.History[len(s.History)-1]
		stableSample = !v.At.Before(s.QueueScopeSince) && now.Sub(v.At) <= 90*time.Second && !v.Pressure && v.WorkerMemory < .88 && v.HostAvailable >= .2 && v.CPU <= .85 && v.OldestDueSeconds < 120 && w.AvailableLaneCount >= 5
	}
	if target > old {
		s.Multiplier = target
		s.StableSince = time.Time{}
		s.ValidatedSince = now
	} else if target < old && !upstream && stableSample && s.AutomaticHold == "" {
		if s.StableSince.IsZero() {
			s.StableSince = now
		}
		if now.Sub(s.StableSince) >= 15*time.Minute {
			s.Multiplier = max(target, old*.9)
			s.StableSince = now
			s.ValidatedSince = now
		}
	} else {
		s.StableSince = time.Time{}
	}
	if s.Multiplier < 1 {
		s.Multiplier = 1
	}
	elapsed := max(0, now.Sub(s.LastTick).Seconds())
	gap := s.LastTick.IsZero() || elapsed > 120 || now.Before(s.LastTick)
	if gap {
		elapsed = 0
		s.RequestTokens = 0
		s.LaneTokens = 0
		s.ProbeTokens = 0
		s.StableSince = time.Time{}
		s.ValidatedSince = now
	}
	// Credit at most one minute. Real requests are separately throttled in Worker.
	s.RequestTokens = min(s.RequestBudget*60, s.RequestTokens+elapsed*s.RequestBudget)
	s.LaneTokens = min(s.LaneBudget*60, s.LaneTokens+elapsed*s.LaneBudget)
	s.ProbeTokens = min(s.RequestBudget*6, s.ProbeTokens+elapsed*s.RequestBudget*.1)
	s.LastTick = now
	adaptiveInitialize(store, shops, now)
	for _, shop := range shops {
		c := store.Shops[shop.ID]
		a := c.Adaptive
		if a.Lease != nil && !publicAccountImportProductSyncIsActive(c, now) {
			adaptiveRecordFailure(&c, "lease_expired", "adaptive job lease expired", now, time.Minute)
			store.Shops[shop.ID] = c
		}
		if old != s.Multiplier || gap {
			for _, stream := range []*productStream{&a.CatalogStream, &a.QuoteStream} {
				if stream.Next.IsZero() {
					continue
				}
				remaining := stream.Next.Sub(now)
				if remaining > 0 && !gap {
					stream.Next = now.Add(time.Duration(float64(remaining) * s.Multiplier / old))
				}
				if gap && remaining < 0 {
					stream.Next = now.Add(time.Duration(stream.PhaseSeconds%60) * time.Second)
				}
			}
		}
		if s.Mode == "shadow" {
			catalog, quote := adaptivePeriods(a)
			for kind, period := range map[string]time.Duration{"catalog": catalog, "quote_batch": quote} {
				stream := &a.CatalogStream
				if kind == "quote_batch" {
					stream = &a.QuoteStream
				}
				if period <= 0 || stream.Next.After(now) || (kind == "quote_batch" && a.EffectiveCount == 0) {
					continue
				}
				cost := adaptiveCost(a, kind, history)
				fraction := 1.0
				if kind == "quote_batch" {
					fraction = float64(min(adaptiveQuoteLimit(a), a.EffectiveCount)) / float64(max(1, a.EffectiveCount))
				}
				stream.Finish += period.Seconds() * s.Multiplier * fraction
				stream.Next = now.Add(time.Duration(float64(period) * s.Multiplier * fraction))
				s.ShadowTasks++
				s.ShadowRequests += cost.Requests * fraction
			}
		}
	}
	stale := len(s.History) == 0 || now.Sub(s.History[len(s.History)-1].At) > 2*time.Minute || now.Sub(parsePublicAccountImportProductTimestamp(w.UpdatedAt)) > 2*time.Minute
	if stale {
		s.AutomaticHold = "resource or Worker observation stale"
		s.AdmissionHold = s.AutomaticHold
		s.RecoverySince = time.Time{}
		return
	}
	v := s.History[len(s.History)-1]
	prev := v
	if len(s.History) > 1 {
		prev = s.History[len(s.History)-2]
	}
	heavy := ""
	switch {
	case v.WorkerOOM > prev.WorkerOOM || v.HostOOM > prev.HostOOM:
		heavy = "new OOM"
	case v.WorkerMemory >= .95 && prev.WorkerMemory >= .95:
		heavy = "working set >=95% twice"
	case v.HostAvailable < .1:
		heavy = "host available memory <10%"
	case v.DiskFree < 1<<30:
		heavy = "disk free <1 GiB"
	}
	if heavy != "" {
		s.AutomaticHold = heavy
		s.RecoverySince = time.Time{}
	}
	recovery := adaptiveHistoryWindow(s.History, 900, now, func(v productResourceObservation) bool {
		return !v.Pressure && v.WorkerMemory < .88 && v.HostAvailable >= .2 && v.CPU <= .85 && v.DiskFree >= 1<<30
	})
	if recovery && w.AvailableLaneCount >= 5 && heavy == "" {
		s.AutomaticHold = ""
	}
	queueHistory := s.History
	for len(queueHistory) > 0 && queueHistory[0].At.Before(s.QueueScopeSince) {
		queueHistory = queueHistory[1:]
	}
	growing := adaptiveGrowing(queueHistory, now)
	queueRecovered := adaptiveHistoryWindow(queueHistory, 900, now, func(v productResourceObservation) bool { return !v.Pressure && v.OldestDueSeconds < 120 })
	cpuHigh := adaptiveHistoryWindow(s.History, 300, now, func(v productResourceObservation) bool { return v.CPU > .85 })
	light := upstream || v.Pressure || w.AvailableLaneCount < 5 || v.WorkerMemory >= .9 || cpuHigh || growing
	requestPressure := upstream || v.Pressure || growing || (!v.At.Before(s.QueueScopeSince) && v.Due > 0 && v.OldestDueSeconds > 120)
	if s.AutomaticHold != "" || requestPressure || v.WorkerMemory >= .9 || cpuHigh {
		s.ValidatedSince = now
	}
	if requestPressure {
		if s.PressureSince.IsZero() {
			s.PressureSince = now
		}
		s.ValidatedSince = now
	} else if queueRecovered {
		s.PressureSince = time.Time{}
	}
	if light || s.AutomaticHold != "" {
		s.AdmissionHold = "pressure recovery requires 15 stable minutes"
		s.RecoverySince = time.Time{}
	} else if recovery && queueRecovered {
		s.AdmissionHold = ""
	}
	if s.RequiredMultiplier > adaptiveAdmissionCapacityLimit {
		s.AdmissionHold = "capacity exceeds 4.4x admission trial boundary"
	}
	if s.Mode == "adaptive" && requestPressure && s.Multiplier >= 4 && !s.PressureSince.IsZero() && now.Sub(s.PressureSince) >= 20*time.Minute && (s.LastScaleChange.IsZero() || now.Sub(s.LastScaleChange) >= 20*time.Minute) {
		batch := 0
		for _, shop := range shops {
			if a := store.Shops[shop.ID].Adaptive; a.PauseReason == "" && adaptiveShopBatch(shop, control) > batch {
				batch = adaptiveShopBatch(shop, control)
			}
		}
		if batch > 0 {
			for _, shop := range shops {
				a := store.Shops[shop.ID].Adaptive
				if adaptiveShopBatch(shop, control) == batch {
					a.PauseReason = "request pressure at 4x; waiting for stable capacity"
					a.ResumeAfter = now.Add(15 * time.Minute)
				}
			}
			s.LastScaleChange = now
			s.ValidatedSince = now
		}
	}
	if s.Mode == "adaptive" && recovery && s.AdmissionHold == "" && now.Sub(s.LastScaleChange) >= 15*time.Minute {
		batch := math.MaxInt
		for _, shop := range shops {
			a := store.Shops[shop.ID].Adaptive
			if a.PauseReason != "" && !a.ResumeAfter.After(now) && adaptiveShopBatch(shop, control) < batch {
				batch = adaptiveShopBatch(shop, control)
			}
		}
		if batch != math.MaxInt {
			for _, shop := range shops {
				a := store.Shops[shop.ID].Adaptive
				if adaptiveShopBatch(shop, control) == batch {
					a.PauseReason = ""
					a.CatalogStream.Next = now.Add(time.Duration(a.CatalogStream.PhaseSeconds%60) * time.Second)
				}
			}
			s.LastScaleChange = now
			s.AdmissionHold = "observing restored batch for 15 minutes"
			s.ValidatedSince = now
		}
	}
}

func adaptiveClaimLocked(c *gin.Context, shops []PublicAccountImportShop, control PublicAccountImportSyncControl, now time.Time) {
	if !strings.Contains(","+c.Query("capabilities")+",", ",catalog,") || !strings.Contains(","+c.Query("capabilities")+",", ",quote_batch,") {
		response.Success(c, PublicAccountImportProductSyncJobResponse{})
		return
	}
	previous := publicProductCache
	publicProductCache = clonePublicAccountImportProductStore(previous)
	w := publicAccountImportProductSyncWorkerStatus(now)
	adaptiveAdvance(&publicProductCache, shops, control, w, now)
	jobs := adaptiveDispatch(&publicProductCache, shops, control, now)
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache = previous
		response.InternalError(c, "Failed to save adaptive dispatch")
		return
	}
	var job *PublicAccountImportProductSyncJob
	if len(jobs) > 0 {
		job = &jobs[0]
	}
	response.Success(c, PublicAccountImportProductSyncJobResponse{Job: job, Jobs: jobs})
}

func adaptiveDispatch(store *publicAccountImportProductStore, shops []PublicAccountImportShop, control PublicAccountImportSyncControl, now time.Time) []PublicAccountImportProductSyncJob {
	shops = supportedPublicAccountImportProductShops(shops)
	s := store.Adaptive
	active, quotes, first := 0, 0, 0
	for _, shop := range shops {
		cached := store.Shops[shop.ID]
		if publicAccountImportProductSyncIsActive(cached, now) {
			active++
			if a := cached.Adaptive; a != nil && a.Lease != nil {
				if a.Lease.Kind == "quote_batch" {
					quotes++
				}
				if a.Lease.Kind == "catalog" && a.Generation == 0 {
					first++
				}
			}
		}
		for _, rec := range cached.ProductRefreshes {
			if publicAccountImportProductRefreshRecordActive(rec, now) {
				quotes++
				active++
			}
		}
	}
	type candidate struct {
		shop                   PublicAccountImportShop
		kind                   string
		manual, probe          bool
		cost, cycle            productCost
		period, finish, weight float64
		indices                []int
	}
	var chosen *candidate
	history := adaptiveCostHistory((*store), shops)
	if active < publicAccountImportProductMaxSyncJobs {
		for _, shop := range shops {
			cached := (*store).Shops[shop.ID]
			a := cached.Adaptive
			if publicAccountImportProductSyncIsActive(cached, now) || a.RetryAt.After(now) {
				continue
			}
			manual := publicAccountImportProductRefreshIsPending(cached, now)
			_, isolated := control.QuarantinedShops[shop.ID]
			if !manual && (isolated || control.AutomaticPaused || s.AutomaticHold != "" || a.PauseReason != "" || s.RequestBudget <= 0 || s.LaneBudget <= 0) {
				continue
			}
			catalog, quote := adaptivePeriods(a)
			for kind, base := range map[string]time.Duration{"catalog": catalog, "quote_batch": quote} {
				stream := &a.CatalogStream
				if kind == "quote_batch" {
					stream = &a.QuoteStream
				}
				if !manual && stream.Next.After(now) {
					continue
				}
				if kind == "quote_batch" && (a.Generation == 0 || a.EffectiveCount == 0 || a.ZeroChecks > 0 || quotes >= 2) {
					continue
				}
				if kind == "catalog" && a.Generation == 0 && first >= 2 {
					continue
				}
				if manual && kind == "quote_batch" && a.CatalogAt.Before(parsePublicAccountImportProductTimestamp(cached.RefreshRequestedAt)) {
					continue
				}
				if manual && kind == "catalog" && !a.CatalogAt.Before(parsePublicAccountImportProductTimestamp(cached.RefreshRequestedAt)) {
					continue
				}
				cycle := adaptiveCost(a, kind, history)
				cost := cycle
				indices := []int{}
				if kind == "quote_batch" {
					limit := min(adaptiveQuoteLimit(a), a.EffectiveCount-int(a.Cursor%uint64(a.EffectiveCount)))
					// Defer sorting product ages until this flow wins the fair queue.
					indices = make([]int, min(limit, a.EffectiveCount))
					if len(indices) == 0 {
						continue
					}
					units := float64(a.EffectiveCount + (a.EffectiveCount+adaptiveQuoteLimit(a)-1)/adaptiveQuoteLimit(a))
					fraction := float64(len(indices)+1) / units
					cost.Requests = cycle.Requests * fraction
					cost.LaneSeconds = cycle.LaneSeconds * fraction
				}
				probe := (kind == "quote_batch" && a.FirstQuoteAt.IsZero() && !a.Established) || a.Exception != ""
				// Reserve a request and a fair share of channel time. Large complete
				// catalogs may borrow against future credit, never against real limits.
				if !manual && (s.RequestTokens < min(cost.Requests, s.RequestBudget*60) || s.LaneTokens < min(cost.LaneSeconds, s.LaneBudget*60) || (probe && s.ProbeTokens < min(cost.Requests, s.RequestBudget*6))) {
					continue
				}
				period := base.Seconds() * s.Multiplier
				weight := adaptiveScalar(cycle, s) / max(1, period)
				finish := max(stream.Finish, s.VirtualTime) + adaptiveScalar(cost, s)/max(.000001, weight) + stream.Debt
				v := candidate{shop, kind, manual, probe, cost, cycle, period, finish, weight, indices}
				if chosen == nil || (manual && !chosen.manual) || (manual == chosen.manual && (finish < chosen.finish || (finish == chosen.finish && shop.ID < chosen.shop.ID))) {
					chosen = &v
				}
			}
		}
	}
	jobs := []PublicAccountImportProductSyncJob{}
	if chosen != nil {
		v := chosen
		c := (*store).Shops[v.shop.ID]
		a := c.Adaptive
		if v.kind == "quote_batch" {
			v.indices = adaptiveSelectProducts(a, len(v.indices), now)
		}
		token, _ := publicAccountImportShopToken(v.shop.URL)
		id := publicAccountImportProductSyncAttemptID(v.shop.ID, now)
		c.ShopID = v.shop.ID
		c.Error = ""
		c.RetryNotBeforeAt = ""
		c.SyncAttemptID = id
		c.SyncStartedAt = now.Format(time.RFC3339Nano)
		c.SyncHeartbeatAt = c.SyncStartedAt
		c.LastAttempt = c.SyncStartedAt
		if c.FirstStartedAt == "" {
			c.FirstStartedAt = c.SyncStartedAt
		}
		lease := &productAdaptiveLease{Kind: v.kind, Manual: v.manual, Probe: v.probe, Generation: a.Generation, Estimate: v.cost, CycleCost: v.cycle, Weight: v.weight, Period: v.period, Started: now, Versions: map[string]string{}}
		lease.DueAt = a.CatalogStream.Next
		if v.kind == "quote_batch" {
			lease.DueAt = a.QuoteStream.Next
		}
		if v.manual {
			lease.DueAt = now
		}
		job := PublicAccountImportProductSyncJob{ShopID: v.shop.ID, ShopName: v.shop.Name, ShopURL: v.shop.URL, Token: token, AttemptID: id, Scope: v.kind, Automatic: !v.manual, Probe: v.probe, CatalogGeneration: a.Generation, MaxSeconds: 30}
		for _, i := range v.indices {
			e := a.Catalog[i]
			job.Products = append(job.Products, adaptiveJobItem(e))
			lease.Versions[e.GoodsKey] = e.MetadataVersion
		}
		a.Lease = lease
		stream := &a.CatalogStream
		if v.kind == "quote_batch" {
			stream = &a.QuoteStream
		}
		stream.Finish = v.finish
		stream.Debt = 0
		s.VirtualTime = max(s.VirtualTime, v.finish-v.period)
		if !v.manual {
			s.RequestTokens -= v.cost.Requests
			s.LaneTokens -= v.cost.LaneSeconds
			if v.probe {
				s.ProbeTokens -= v.cost.Requests
			}
		}
		(*store).Shops[v.shop.ID] = c
		jobs = append(jobs, job)
	}
	return jobs
}

// Admission is decided by the backend for every submit, including importers.
// Call before taking the shop-file lock: handlers never invert the lock order.
func adaptiveAdmission() (int, string, error) {
	shops, err := snapshotPublicAccountImportShops()
	if err != nil {
		return 0, "", err
	}
	shops = supportedPublicAccountImportProductShops(shops)
	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	control, err := loadPublicAccountImportSyncControlLocked()
	if err != nil {
		return 0, "", err
	}
	if control.Mode != "adaptive" {
		return 0, "", nil
	}
	if err := loadPublicProductCacheLocked(); err != nil {
		return 0, "", err
	}
	s := publicProductCache.Adaptive
	now := time.Now().UTC()
	w := publicAccountImportProductSyncWorkerStatus(now)
	if s == nil || len(s.History) == 0 || now.Sub(s.History[len(s.History)-1].At) > 2*time.Minute {
		return 0, "resource observation unavailable", nil
	}
	if control.AutomaticPaused || s.AutomaticHold != "" || s.AdmissionHold != "" || adaptiveUpstream(w, now) || w.AvailableLaneCount < 5 {
		return 0, "new admission frozen by capacity protection", nil
	}
	for _, shop := range shops {
		c := publicProductCache.Shops[shop.ID]
		if c.Adaptive != nil && c.Adaptive.PauseReason != "" {
			return 0, "restore pressure-paused shops first", nil
		}
	}
	if now.Sub(s.LastScaleChange) < 15*time.Minute {
		return 0, "observing restored batch", nil
	}
	if s.BatchCount >= s.BatchLimit && now.Sub(s.LastAdmission) < 15*time.Minute {
		return 0, "observing admission batch", nil
	}
	before := publicProductCache
	publicProductCache = clonePublicAccountImportProductStore(before)
	s = publicProductCache.Adaptive
	if s.BatchCount >= s.BatchLimit {
		s.BatchCount = 0
		if s.BatchLimit < 5 {
			s.BatchLimit = 5
		} else {
			s.BatchLimit = 10
		}
	}
	if s.BatchCount == 0 {
		s.BatchNumber++
	}
	s.BatchCount++
	s.LastAdmission = now
	s.ValidatedSince = now
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache = before
		return 0, "", err
	}
	return s.BatchNumber, "", nil
}

func adaptiveSortedShops(shops []PublicAccountImportShop) {
	sort.Slice(shops, func(i, j int) bool { return shops[i].ID < shops[j].ID })
}

func adaptiveShopBatch(shop PublicAccountImportShop, control PublicAccountImportSyncControl) int {
	return max(shop.ImportBatch, control.ImportedBatches[shop.ID])
}

func adaptiveShadowHold(s *productAdaptiveState, now time.Time) bool {
	return s == nil || s.AutomaticHold != "" || len(s.History) == 0 || now.Sub(s.History[len(s.History)-1].At) > 2*time.Minute
}
