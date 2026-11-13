package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

type productCatalogSubmission struct {
	ShopID        string                `json:"shop_id"`
	AttemptID     string                `json:"attempt_id"`
	SchemaVersion int                   `json:"schema_version"`
	Complete      bool                  `json:"complete"`
	Closed        bool                  `json:"closed"`
	SourceCount   int                   `json:"source_product_count"`
	Products      []productCatalogInput `json:"products"`
	Cost          productCost           `json:"cost"`
}

type productCatalogInput struct {
	PublicAccountImportProductSyncItem
	Sellable      bool   `json:"sellable"`
	SourceVersion string `json:"source_version"`
}

type productQuoteResult struct {
	GoodsKey        string   `json:"goods_key"`
	MetadataVersion string   `json:"metadata_version"`
	PayablePrice    *float64 `json:"payable_price"`
	VerifiedAt      string   `json:"quote_verified_at"`
	Unavailable     bool     `json:"unavailable"`
}

type productQuoteSubmission struct {
	ShopID            string               `json:"shop_id"`
	AttemptID         string               `json:"attempt_id"`
	Generation        uint64               `json:"catalog_generation"`
	Products          []productQuoteResult `json:"products"`
	Cost              productCost          `json:"cost"`
	FailureKind       string               `json:"failure_kind,omitempty"`
	Error             string               `json:"error,omitempty"`
	RetryAfterSeconds int                  `json:"retry_after_seconds,omitempty"`
}

func adaptiveBind(c *gin.Context, value any, limit int64) bool {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return false
	}
	if !authorizePublicAccountImportProductWorker(c) {
		return false
	}
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil || d.Decode(new(any)) != io.EOF {
		response.BadRequest(c, "Invalid adaptive sync request")
		return false
	}
	return true
}

func adaptiveLease(c publicAccountImportProductShopCache, id, scope string, now time.Time) bool {
	return c.Adaptive != nil && c.Adaptive.Lease != nil && c.Adaptive.Lease.Kind == scope && c.SyncAttemptID == id && id != "" && publicAccountImportProductSyncIsActive(c, now)
}

func adaptiveFinish(store *publicAccountImportProductStore, c *publicAccountImportProductShopCache, cost productCost, completed int, now time.Time) {
	a := c.Adaptive
	l := a.Lease
	s := store.Adaptive
	if l == nil {
		return
	}
	anchor := l.DueAt
	if anchor.IsZero() || l.Started.Sub(anchor).Seconds() > l.Period || anchor.After(l.Started) {
		anchor = l.Started
	}
	stream := &a.CatalogStream
	if l.Kind == "quote_batch" {
		stream = &a.QuoteStream
	}
	if s != nil {
		stream.Debt += (adaptiveScalar(cost, s) - adaptiveScalar(l.Estimate, s)) / max(.000001, l.Weight)
		if !l.Manual {
			s.RequestTokens -= cost.Requests - l.Estimate.Requests
			s.LaneTokens -= cost.LaneSeconds - l.Estimate.LaneSeconds
			if l.Probe {
				s.ProbeTokens -= cost.Requests - l.Estimate.Requests
			}
		}
	}
	if l.Kind == "quote_batch" && completed > 0 {
		per := cost
		per.Requests = max(0, cost.Requests-1) / float64(completed)
		per.Retries /= float64(completed)
		per.LaneSeconds /= float64(completed + 1)
		per.VerificationSeconds /= float64(completed)
		stream.Cost.observe(per)
		fraction := cost.Requests / max(1, l.CycleCost.Requests)
		// Slow/expensive batches consume their share of the full cycle too.
		if l.CycleCost.LaneSeconds > 0 {
			fraction = max(fraction, cost.LaneSeconds/l.CycleCost.LaneSeconds)
		}
		stream.Next = anchor.Add(time.Duration(max(1, l.Period*fraction) * float64(time.Second)))
		a.Cursor += uint64(completed)
	} else if l.Kind == "catalog" {
		stream.Cost.observe(cost)
		period, _ := adaptivePeriods(a)
		multiplier := 1.0
		if s != nil {
			multiplier = s.Multiplier
		}
		stream.Next = anchor.Add(time.Duration(float64(period) * multiplier))
		if a.ZeroChecks == 1 && a.Tier != "closed" {
			stream.Next = now.Add(10 * time.Minute)
		}
	}
	if stream.Next.Before(now) {
		stream.Next = now.Add(time.Second)
	}
	c.SuccessCount++
	c.ConsecutiveFailures = 0
	c.Error = ""
	c.RetryNotBeforeAt = ""
	a.Failures = 0
	a.FirstFailures = 0
	if l.Kind == "quote_batch" && completed == 0 {
		a.NoProgressSeconds += cost.LaneSeconds
	} else {
		a.NoProgressSeconds = 0
	}
	a.RetryAt = time.Time{}
	a.Exception = ""
	if a.NoProgressSeconds >= 3600 {
		a.Exception = "no_progress"
		a.RetryAt = now.Add(6 * time.Hour)
	}
	if c.FirstSyncedAt == "" && a.Generation > 0 {
		c.FirstSyncedAt = now.Format(time.RFC3339Nano)
	}
	a.Lease = nil
	c.SyncAttemptID = ""
	c.SyncStartedAt = ""
	c.SyncHeartbeatAt = ""
	if l.Manual {
		complete := a.EffectiveCount == 0
		if l.Kind == "quote_batch" {
			complete = true
			requested := parsePublicAccountImportProductTimestamp(c.RefreshRequestedAt)
			for _, e := range a.Catalog {
				if e.Sellable && e.LastQuoteAttemptAt.Before(requested) {
					complete = false
					break
				}
			}
		}
		if complete {
			c.ManualRefreshCompletedAt = now.Format(time.RFC3339Nano)
			c.RefreshRequestedAt = ""
		}
	}
}

func applyProductCatalog(c *publicAccountImportProductShopCache, shop PublicAccountImportShop, req productCatalogSubmission, now time.Time) error {
	if !req.Complete || req.SchemaVersion != 1 || req.SourceCount < 0 || req.SourceCount > 1000 || len(req.Products) != req.SourceCount || req.Products == nil || !validProductCost(req.Cost) || (req.Closed && req.SourceCount != 0) {
		return errors.New("complete catalog, valid counts and measured cost are required")
	}
	a := c.Adaptive
	old := map[string]productCatalogEntry{}
	for _, e := range a.Catalog {
		old[e.GoodsKey] = e
	}
	legacy := map[string]PublicAccountImportProduct{}
	for _, p := range c.Products {
		legacy[productGoodsKeyFromURL(p.URL)] = p
	}
	entries := make([]productCatalogEntry, 0, len(req.Products))
	seen := map[string]bool{}
	effective := 0
	changed := false
	for _, item := range req.Products {
		key := item.GoodsKey
		if key == "" || len(key) > 100 || seen[key] {
			return errors.New("duplicate or invalid catalog identity")
		}
		seen[key] = true
		if len(item.SourceVersion) != 64 || !publicAccountImportProductBodyHashPattern.MatchString(item.SourceVersion) {
			return errors.New("quote conditions digest is required")
		}
		e := productCatalogEntry{GoodsKey: key, Sellable: item.Sellable, MetadataVersion: adaptiveMetadata(item.PublicAccountImportProductSyncItem, item.SourceVersion)}
		if item.Sellable {
			// Use the common identity/inventory/URL validation without requiring a quote.
			base := item.PublicAccountImportProductSyncItem
			base.PayablePrice = float64Pointer(0)
			base.QuoteVerifiedAt = now.Format(time.RFC3339Nano)
			p, err := normalizePublicProductSyncItem(shop, base, base.QuoteVerifiedAt)
			if err != nil {
				return err
			}
			p.PayablePrice = nil
			p.UnitPrice = nil
			p.QuoteVerifiedAt = ""
			previous, exists := old[key]
			if exists {
				p.PayablePrice = previous.Product.PayablePrice
				p.UnitPrice = previous.Product.UnitPrice
				p.QuoteVerifiedAt = previous.Product.QuoteVerifiedAt
				e.QuoteVersion = previous.QuoteVersion
				e.LastQuoteAttemptAt = previous.LastQuoteAttemptAt
				e.Priority = previous.Priority
				e.QuoteUnavailable = previous.QuoteUnavailable
				if previous.MetadataVersion != e.MetadataVersion || !previous.Sellable {
					e.Priority = true
					e.QuoteUnavailable = false
					changed = true
				}
			} else {
				e.Priority = true
				changed = true
				if previous, ok := legacy[key]; ok {
					p.PayablePrice = previous.PayablePrice
					p.UnitPrice = previous.UnitPrice
					p.QuoteVerifiedAt = previous.QuoteVerifiedAt
					// Adopt the existing complete quote on first migration only
					// when all known quote conditions still match. Its verification
					// timestamp is retained, so it remains a historical quote.
					if publicAccountImportProductQuoteIsValid(previous, now) && previous.Price == p.Price && previous.MarketPrice == p.MarketPrice && previous.MinimumQuantity == p.MinimumQuantity && previous.Stock == p.Stock && previous.GoodsType == p.GoodsType {
						e.QuoteVersion = e.MetadataVersion
						e.LastQuoteAttemptAt = parsePublicAccountImportProductTimestamp(previous.QuoteVerifiedAt)
						e.Priority = false
					}
				}
			}
			e.Product = p
			effective++
		}
		entries = append(entries, e)
	}
	first := a.Generation == 0
	a.Catalog = entries
	a.SourceCount = req.SourceCount
	a.Generation++
	a.CatalogAt = now
	if a.FirstCatalogAt.IsZero() {
		a.FirstCatalogAt = now
	}
	if first {
		a.Tier = ""
	}
	adaptiveClassify(a, effective, req.SourceCount, req.Closed, now)
	if changed && effective > 0 {
		a.QuoteStream.Next = now
	}
	return nil
}

func (h *AccountHandler) SubmitPublicAccountImportCatalog(c *gin.Context) {
	var req productCatalogSubmission
	if !adaptiveBind(c, &req, publicAccountImportProductMaxBody) {
		return
	}
	shops, err := snapshotPublicAccountImportShops()
	if err != nil {
		response.InternalError(c, "Failed to load shops")
		return
	}
	var shop *PublicAccountImportShop
	for i := range shops {
		if shops[i].ID == req.ShopID {
			shop = &shops[i]
			break
		}
	}
	if shop == nil {
		response.BadRequest(c, "Shop is not available")
		return
	}
	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to load catalog")
		return
	}
	now := time.Now().UTC()
	cached := publicProductCache.Shops[req.ShopID]
	if !adaptiveLease(cached, req.AttemptID, "catalog", now) {
		response.Error(c, http.StatusConflict, "Catalog lease expired")
		return
	}
	before := publicProductCache
	next := clonePublicAccountImportProductStore(before)
	cached = next.Shops[req.ShopID]
	if err := applyProductCatalog(&cached, *shop, req, now); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	adaptiveFinish(&next, &cached, req.Cost, len(req.Products), now)
	adaptiveUpdateCompatibility(&cached, now)
	next.Shops[req.ShopID] = cached
	publicProductCache = next
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache = before
		response.InternalError(c, "Failed to save complete catalog")
		return
	}
	response.Success(c, gin.H{"accepted": len(req.Products), "catalog_generation": cached.Adaptive.Generation})
}

func applyProductQuotes(c *publicAccountImportProductShopCache, req productQuoteSubmission, now time.Time) (int, error) {
	a := c.Adaptive
	l := a.Lease
	if !validProductCost(req.Cost) || req.Products == nil || len(req.Products) > adaptiveBatchSize || req.Generation != l.Generation {
		return 0, errors.New("invalid quote batch or cost")
	}
	seen := map[string]bool{}
	accepted := 0
	for _, quote := range req.Products {
		if seen[quote.GoodsKey] || l.Versions[quote.GoodsKey] == "" || quote.MetadataVersion != l.Versions[quote.GoodsKey] {
			return 0, errors.New("quote identity or metadata does not match lease")
		}
		seen[quote.GoodsKey] = true
		at := parsePublicAccountImportProductTimestamp(quote.VerifiedAt)
		if at.IsZero() || at.Before(l.Started.Add(-time.Minute)) || at.After(now.Add(time.Minute)) || (!quote.Unavailable && (quote.PayablePrice == nil || !finitePublicAccountImportProductPrice(*quote.PayablePrice))) {
			return 0, errors.New("invalid quote value or verification time")
		}
		for i := range a.Catalog {
			e := &a.Catalog[i]
			if e.GoodsKey != quote.GoodsKey || !e.Sellable || e.MetadataVersion != quote.MetadataVersion {
				continue
			}
			if !at.After(e.LastQuoteAttemptAt) || (!e.Product.QuotePending && at.Before(parsePublicAccountImportProductTimestamp(e.Product.QuoteVerifiedAt))) {
				break
			}
			e.LastQuoteAttemptAt = at
			e.Priority = false
			e.QuoteUnavailable = quote.Unavailable
			if !quote.Unavailable {
				e.Product.PayablePrice = quote.PayablePrice
				e.Product.UnitPrice = float64Pointer(*quote.PayablePrice / float64(e.Product.MinimumQuantity))
				e.Product.QuoteVerifiedAt = at.Format(time.RFC3339Nano)
				e.QuoteVersion = e.MetadataVersion
			}
			accepted++
			break
		}
	}
	return accepted, nil
}

func (h *AccountHandler) SubmitPublicAccountImportQuotes(c *gin.Context) {
	var req productQuoteSubmission
	if !adaptiveBind(c, &req, publicAccountImportProductMaxBody) {
		return
	}
	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to load quotes")
		return
	}
	now := time.Now().UTC()
	cached := publicProductCache.Shops[req.ShopID]
	if !adaptiveLease(cached, req.AttemptID, "quote_batch", now) {
		response.Error(c, http.StatusConflict, "Quote lease expired")
		return
	}
	before := publicProductCache
	next := clonePublicAccountImportProductStore(before)
	cached = next.Shops[req.ShopID]
	accepted, err := applyProductQuotes(&cached, req, now)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if req.FailureKind != "" {
		// Preserve successful quotes even when the following request fails. Cost
		// and failure accounting are committed in the same atomic replacement.
		lease := cached.Adaptive.Lease
		failures, firstFailures := cached.Adaptive.Failures, cached.Adaptive.FirstFailures
		adaptiveFinish(&next, &cached, req.Cost, len(req.Products), now)
		cached.Adaptive.Lease = lease
		cached.Adaptive.Failures = failures
		cached.Adaptive.FirstFailures = firstFailures
		adaptiveRecordFailure(&cached, req.FailureKind, req.Error, now, time.Duration(min(max(0, req.RetryAfterSeconds), 3600))*time.Second)
	} else {
		adaptiveFinish(&next, &cached, req.Cost, len(req.Products), now)
	}
	adaptiveUpdateCompatibility(&cached, now)
	next.Shops[req.ShopID] = cached
	publicProductCache = next
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache = before
		response.InternalError(c, "Failed to save quote batch")
		return
	}
	response.Success(c, gin.H{"accepted": accepted})
}

func (h *AccountHandler) SubmitPublicAccountImportResources(c *gin.Context) {
	var observation productResourceObservation
	if !adaptiveBind(c, &observation, publicAccountImportProductSyncStatusMaxBody) {
		return
	}
	now := time.Now().UTC()
	if observation.At.Before(now.Add(-2*time.Minute)) || observation.At.After(now.Add(10*time.Second)) || observation.WorkerID == "" || len(observation.WorkerID) > 100 || observation.WorkerMemory < 0 || observation.WorkerMemory > 2 || observation.HostAvailable < 0 || observation.HostAvailable > 1 || observation.CPU < 0 || observation.CPU > 1 || observation.DiskFree < 0 || observation.UpstreamEvents < 0 {
		response.BadRequest(c, "Invalid or stale resource observation")
		return
	}
	shops, err := snapshotPublicAccountImportShops()
	if err != nil {
		response.InternalError(c, "Failed to load shops")
		return
	}
	publicProductCacheMu.Lock()
	defer publicProductCacheMu.Unlock()
	if err := loadPublicProductCacheLocked(); err != nil {
		response.InternalError(c, "Failed to load scheduler")
		return
	}
	control, err := loadPublicAccountImportSyncControlLocked()
	if err != nil {
		response.InternalError(c, "Failed to load control")
		return
	}
	if control.Mode == "legacy" {
		response.Success(c, gin.H{"accepted": false, "mode": "legacy"})
		return
	}
	before := publicProductCache
	next := clonePublicAccountImportProductStore(before)
	if next.Adaptive == nil {
		next.Adaptive = &productAdaptiveState{Multiplier: 4, BatchLimit: 2}
	}
	s := next.Adaptive
	if len(s.History) > 0 {
		last := s.History[len(s.History)-1]
		if !observation.At.After(last.At) {
			response.Success(c, gin.H{"accepted": false})
			return
		}
		if observation.HostOOM > last.HostOOM || (observation.WorkerID == last.WorkerID && observation.WorkerOOM > last.WorkerOOM) {
			s.AutomaticHold = "new OOM"
		}
		if observation.At.Sub(last.At) > 2*time.Minute || last.WorkerID != observation.WorkerID {
			s.History = nil
			s.StableSince = time.Time{}
			s.ValidatedSince = now
			s.AdmissionHold = "observation gap or Worker restart; waiting for recovery"
		}
	}
	w := publicAccountImportProductSyncWorkerStatus(now)
	observation.Lanes = w.AvailableLaneCount
	observation.RequestRate = w.AdaptiveRatePerSecond
	observation.Pressure = observation.UpstreamEvents > 0 || adaptiveUpstream(w, now)
	observation.Due = 0
	observation.OldestDueSeconds = 0
	for _, shop := range shops {
		cached := next.Shops[shop.ID]
		a := cached.Adaptive
		if a == nil || a.PauseReason != "" || control.QuarantinedShops[shop.ID] != "" || publicAccountImportProductSyncIsActive(cached, now) {
			continue
		}
		stats := adaptiveStats(a, max(1, s.Multiplier), now)
		quoteDue := a.Generation > 0 && a.EffectiveCount > 0 && a.ZeroChecks == 0 && !a.QuoteStream.Next.After(now) && !a.RetryAt.After(now)
		if stats.CatalogOverdue || quoteDue {
			observation.Due++
			at := a.CatalogStream.Next
			if quoteDue && (!stats.CatalogOverdue || a.QuoteStream.Next.Before(at)) {
				at = a.QuoteStream.Next
			}
			if !at.IsZero() {
				observation.OldestDueSeconds = max(observation.OldestDueSeconds, now.Sub(at).Seconds())
			}
		}
	}
	s.History = append(s.History, observation)
	if len(s.History) > 32 {
		s.History = s.History[len(s.History)-32:]
	}
	adaptiveAdvance(&next, shops, control, w, now)
	publicProductCache = next
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache = before
		response.InternalError(c, "Failed to save scheduler observation")
		return
	}
	response.Success(c, gin.H{"accepted": true, "mode": control.Mode, "multiplier": s.Multiplier, "admission_hold": s.AdmissionHold})
}

func failAdaptiveJob(c *gin.Context, cached publicAccountImportProductShopCache, req PublicAccountImportProductSyncFailureRequest, now time.Time) {
	if !adaptiveLease(cached, req.AttemptID, req.Scope, now) {
		response.Success(c, gin.H{"accepted": false})
		return
	}
	before := publicProductCache
	next := clonePublicAccountImportProductStore(before)
	cached = next.Shops[req.ShopID]
	a := cached.Adaptive
	l := a.Lease
	if req.Cost != nil {
		if !validProductCost(*req.Cost) {
			response.BadRequest(c, "Invalid failure cost")
			return
		}
		if s := next.Adaptive; s != nil && !l.Manual {
			s.RequestTokens -= req.Cost.Requests - l.Estimate.Requests
			s.LaneTokens -= req.Cost.LaneSeconds - l.Estimate.LaneSeconds
			if l.Probe {
				s.ProbeTokens -= req.Cost.Requests - l.Estimate.Requests
			}
		}
		stream := &a.CatalogStream
		if l.Kind == "quote_batch" {
			stream = &a.QuoteStream
		}
		if next.Adaptive != nil {
			stream.Debt += (adaptiveScalar(*req.Cost, next.Adaptive) - adaptiveScalar(l.Estimate, next.Adaptive)) / max(.000001, l.Weight)
		}
	}
	adaptiveRecordFailure(&cached, req.Kind, req.Error, now, time.Duration(min(max(0, req.RetryAfterSeconds), 3600))*time.Second)
	next.Shops[req.ShopID] = cached
	publicProductCache = next
	if err := savePublicProductCacheLocked(); err != nil {
		publicProductCache = before
		response.InternalError(c, "Failed to save adaptive failure")
		return
	}
	response.Success(c, gin.H{"accepted": true})
}

func submitAdaptiveSingleQuote(c *gin.Context, shop PublicAccountImportShop, cached publicAccountImportProductShopCache, rec publicAccountImportProductRefreshRecord, req PublicAccountImportProductSyncOneRequest, now time.Time) {
	if req.Product.GoodsKey == "" {
		req.Product.GoodsKey = rec.GoodsKey
	}
	if req.Product.GoodsKey != rec.GoodsKey {
		response.BadRequest(c, "Product identity does not match")
		return
	}
	quote, err := normalizePublicProductSyncItem(shop, req.Product, now.Format(time.RFC3339Nano))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	rec.State = "superseded"
	for i := range cached.Adaptive.Catalog {
		e := &cached.Adaptive.Catalog[i]
		if !e.Sellable || e.GoodsKey != rec.GoodsKey || e.MetadataVersion != rec.MetadataVersion {
			continue
		}
		if quote.MinimumQuantity != e.Product.MinimumQuantity || quote.GoodsType != e.Product.GoodsType || quote.Stock != e.Product.Stock {
			cached.Adaptive.CatalogStream.Next = now
			break
		}
		at := parsePublicAccountImportProductTimestamp(quote.QuoteVerifiedAt)
		if at.After(parsePublicAccountImportProductTimestamp(e.Product.QuoteVerifiedAt)) && at.After(e.LastQuoteAttemptAt) {
			e.Product.PayablePrice = quote.PayablePrice
			e.Product.UnitPrice = quote.UnitPrice
			e.Product.QuoteVerifiedAt = quote.QuoteVerifiedAt
			e.QuoteVersion = e.MetadataVersion
			e.Priority = false
			e.LastQuoteAttemptAt = at
			e.QuoteUnavailable = false
		}
		rec.State = "succeeded"
		break
	}
	rec.CompletedAt = now.Format(time.RFC3339Nano)
	rec.SyncAttemptID = ""
	rec.SyncHeartbeatAt = ""
	rec.SyncStartedAt = ""
	rec.RetryNotBeforeAt = now.Add(publicAccountImportSingleProductCooldown).Format(time.RFC3339Nano)
	cached.ProductRefreshes[req.ProductID] = rec
	adaptiveUpdateCompatibility(&cached, now)
	if err := savePublicProductShopCacheLocked(req.ShopID, cached); err != nil {
		response.InternalError(c, "Failed to save product quote")
		return
	}
	response.Success(c, gin.H{"accepted": rec.State == "succeeded", "state": rec.State})
}
