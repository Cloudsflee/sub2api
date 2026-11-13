package admin

// The adaptive state lives in the same atomic JSON document as the legacy
// snapshot. No queue or observation endpoint publishes a partial directory.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

const adaptiveBatchSize = 20

// Operator-approved 2026-11-13: allow a modest admission trial beyond the
// forecast boundary; actual request pressure still freezes and rolls back.
const adaptiveAdmissionCapacityLimit = 4.4

type productCost struct {
	Requests            float64 `json:"requests"`
	Retries             float64 `json:"retries"`
	LaneSeconds         float64 `json:"lane_seconds"`
	VerificationSeconds float64 `json:"verification_seconds"`
	Samples             int     `json:"samples"`
}

func (c *productCost) observe(v productCost) {
	if c.Samples == 0 {
		*c = v
		c.Samples = 1
		return
	}
	c.Requests += .2 * (v.Requests - c.Requests)
	c.Retries += .2 * (v.Retries - c.Retries)
	c.LaneSeconds += .2 * (v.LaneSeconds - c.LaneSeconds)
	c.VerificationSeconds += .2 * (v.VerificationSeconds - c.VerificationSeconds)
	c.Samples++
}

type productCatalogEntry struct {
	GoodsKey           string                     `json:"goods_key"`
	Sellable           bool                       `json:"sellable"`
	MetadataVersion    string                     `json:"metadata_version"`
	QuoteVersion       string                     `json:"quote_version,omitempty"`
	Priority           bool                       `json:"priority,omitempty"`
	QuoteUnavailable   bool                       `json:"quote_unavailable,omitempty"`
	LastQuoteAttemptAt time.Time                  `json:"last_quote_attempt_at"`
	Product            PublicAccountImportProduct `json:"product"`
}

type productStream struct {
	Next         time.Time   `json:"next"`
	PhaseSeconds int         `json:"phase_seconds"`
	Finish       float64     `json:"finish"`
	Debt         float64     `json:"debt"`
	Cost         productCost `json:"cost"`
}

type productAdaptiveShop struct {
	Established       bool                  `json:"established,omitempty"`
	HandoffApplied    bool                  `json:"handoff_applied,omitempty"`
	SourceCount       int                   `json:"source_count"`
	Catalog           []productCatalogEntry `json:"catalog"`
	Generation        uint64                `json:"generation"`
	CatalogAt         time.Time             `json:"catalog_at"`
	FirstCatalogAt    time.Time             `json:"first_catalog_at"`
	FirstQuoteAt      time.Time             `json:"first_quote_at"`
	Tier              string                `json:"tier"`
	CandidateTier     string                `json:"candidate_tier,omitempty"`
	TierChecks        int                   `json:"tier_checks"`
	ZeroChecks        int                   `json:"zero_checks"`
	DormantStep       int                   `json:"dormant_step"`
	EffectiveCount    int                   `json:"effective_count"`
	CatalogStream     productStream         `json:"catalog_stream"`
	QuoteStream       productStream         `json:"quote_stream"`
	Cursor            uint64                `json:"cursor"`
	Failures          int                   `json:"failures"`
	FirstFailures     int                   `json:"first_failures"`
	NoProgressSeconds float64               `json:"no_progress_seconds"`
	RetryAt           time.Time             `json:"retry_at"`
	PauseReason       string                `json:"pause_reason,omitempty"`
	ResumeAfter       time.Time             `json:"resume_after"`
	Exception         string                `json:"exception,omitempty"`
	Lease             *productAdaptiveLease `json:"lease,omitempty"`
}

type productAdaptiveLease struct {
	DueAt      time.Time         `json:"due_at"`
	Kind       string            `json:"kind"`
	Manual     bool              `json:"manual"`
	Probe      bool              `json:"probe"`
	Generation uint64            `json:"generation"`
	Versions   map[string]string `json:"versions,omitempty"`
	Estimate   productCost       `json:"estimate"`
	CycleCost  productCost       `json:"cycle_cost"`
	Weight     float64           `json:"weight"`
	Period     float64           `json:"period"`
	Started    time.Time         `json:"started"`
}

type productResourceObservation struct {
	At               time.Time `json:"at"`
	WorkerID         string    `json:"worker_id"`
	WorkerMemory     float64   `json:"worker_memory_ratio"`
	HostAvailable    float64   `json:"host_available_ratio"`
	CPU              float64   `json:"cpu_ratio"`
	DiskFree         int64     `json:"disk_free_bytes"`
	WorkerOOM        uint64    `json:"worker_oom_kill"`
	HostOOM          uint64    `json:"host_oom_kill"`
	UpstreamEvents   int       `json:"upstream_events"`
	Lanes            int       `json:"lanes"`
	RequestRate      float64   `json:"request_rate"`
	Due              int       `json:"due"`
	OldestDueSeconds float64   `json:"oldest_due_seconds"`
	Pressure         bool      `json:"pressure"`
}

type productAdaptiveState struct {
	QueueScopeSince        time.Time                    `json:"queue_scope_since"`
	AdmissionCapacityLimit float64                      `json:"admission_capacity_limit"`
	ShadowTasks            uint64                       `json:"shadow_tasks"`
	ShadowRequests         float64                      `json:"shadow_requests"`
	Mode                   string                       `json:"mode"`
	Multiplier             float64                      `json:"multiplier"`
	RequiredMultiplier     float64                      `json:"required_multiplier"`
	StableSince            time.Time                    `json:"stable_since"`
	LastTick               time.Time                    `json:"last_tick"`
	ShadowSince            time.Time                    `json:"shadow_since"`
	VirtualTime            float64                      `json:"virtual_time"`
	RequestBudget          float64                      `json:"request_budget"`
	LaneBudget             float64                      `json:"lane_budget"`
	RequestTokens          float64                      `json:"request_tokens"`
	LaneTokens             float64                      `json:"lane_tokens"`
	ProbeTokens            float64                      `json:"probe_tokens"`
	History                []productResourceObservation `json:"history"`
	AutomaticHold          string                       `json:"automatic_hold,omitempty"`
	AdmissionHold          string                       `json:"admission_hold,omitempty"`
	RecoverySince          time.Time                    `json:"recovery_since"`
	PressureSince          time.Time                    `json:"pressure_since"`
	LastScaleChange        time.Time                    `json:"last_scale_change"`
	LastAdmission          time.Time                    `json:"last_admission"`
	BatchCount             int                          `json:"batch_count"`
	BatchLimit             int                          `json:"batch_limit"`
	BatchNumber            int                          `json:"batch_number"`
	ValidatedSince         time.Time                    `json:"validated_since"`
}

func adaptiveTier(n int) string {
	switch {
	case n == 0:
		return "empty"
	case n <= 10:
		return "1-10"
	case n <= 50:
		return "11-50"
	case n <= 200:
		return "51-200"
	default:
		return "201-1000"
	}
}

func adaptivePeriods(a *productAdaptiveShop) (time.Duration, time.Duration) {
	switch a.Tier {
	case "1-10":
		return 15 * time.Minute, time.Hour
	case "11-50":
		return 30 * time.Minute, 2 * time.Hour
	case "51-200":
		return time.Hour, 4 * time.Hour
	case "201-1000":
		return 2 * time.Hour, 8 * time.Hour
	case "closed":
		return []time.Duration{24 * time.Hour, 72 * time.Hour, 7 * 24 * time.Hour}[min(a.DormantStep, 2)], 0
	case "unavailable":
		return []time.Duration{2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 12 * time.Hour}[min(a.DormantStep, 3)], 0
	case "empty":
		return []time.Duration{6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 48 * time.Hour}[min(a.DormantStep, 3)], 0
	default:
		return 15 * time.Minute, time.Hour
	}
}

func adaptiveClassify(a *productAdaptiveShop, n, source int, closed bool, now time.Time) {
	tier := adaptiveTier(n)
	if n == 0 && source > 0 {
		tier = "unavailable"
	}
	if closed {
		tier = "closed"
	}
	a.EffectiveCount = n
	if n == 0 {
		a.ZeroChecks++
		if a.ZeroChecks == 1 && !closed {
			a.CatalogStream.Next = now.Add(10 * time.Minute)
			return
		}
		if a.Tier == tier {
			a.DormantStep++
		} else {
			a.DormantStep = 0
		}
		a.Tier, a.CandidateTier, a.TierChecks = tier, "", 0
		return
	}
	wasDormant := a.ZeroChecks > 0 || a.Tier == "closed" || a.Tier == "empty" || a.Tier == "unavailable"
	a.ZeroChecks, a.DormantStep = 0, 0
	if a.Tier == "" || wasDormant {
		a.Tier = tier
		a.QuoteStream.Next = now
		a.CandidateTier = ""
		a.TierChecks = 0
		return
	}
	if tier == a.Tier {
		a.CandidateTier, a.TierChecks = "", 0
		return
	}
	if tier == a.CandidateTier {
		a.TierChecks++
	} else {
		a.CandidateTier, a.TierChecks = tier, 1
	}
	if a.TierChecks >= 2 {
		a.Tier, a.CandidateTier, a.TierChecks = tier, "", 0
	}
}

func adaptiveMetadata(item PublicAccountImportProductSyncItem, upstream string) string {
	// Include all quote conditions; descriptive fields do not invalidate a quote.
	b, _ := json.Marshal([]any{item.GoodsKey, item.GoodsType, item.Price, item.MarketPrice, item.Stock, item.MinimumQuantity, upstream})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func adaptiveCost(a *productAdaptiveShop, kind string, history map[string]productCost) productCost {
	if kind == "catalog" && a.CatalogStream.Cost.Samples > 0 {
		return a.CatalogStream.Cost
	}
	n := max(1, a.EffectiveCount)
	if kind == "quote_batch" && a.QuoteStream.Cost.Samples > 0 {
		c := a.QuoteStream.Cost // Per product plus one channel lookup per batch.
		batches := float64((n + adaptiveQuoteLimit(a) - 1) / adaptiveQuoteLimit(a))
		c.Requests = c.Requests*float64(n) + batches
		c.LaneSeconds *= float64(n) + batches
		return c
	}
	r := float64(1 + 4 + max(0, (a.SourceCount-1)/100))
	if kind == "quote_batch" {
		r = float64(n + (n+adaptiveBatchSize-1)/adaptiveBatchSize)
	}
	seconds := 10.0
	if c := history[a.Tier+":"+kind]; c.Requests > 0 {
		seconds = max(seconds, c.LaneSeconds/c.Requests)
	}
	return productCost{Requests: r, LaneSeconds: r * seconds}
}

func adaptiveQuoteLimit(a *productAdaptiveShop) int {
	seconds := 10.0
	if a.QuoteStream.Cost.Samples > 0 {
		seconds = max(.1, a.QuoteStream.Cost.LaneSeconds)
	}
	return min(adaptiveBatchSize, max(1, int(30/seconds)-1))
}

func adaptiveCostHistory(store publicAccountImportProductStore, shops []PublicAccountImportShop) map[string]productCost {
	h := map[string]productCost{}
	for _, shop := range shops {
		c := store.Shops[shop.ID]
		if a := c.Adaptive; a != nil {
			for kind, cost := range map[string]productCost{"catalog": a.CatalogStream.Cost, "quote_batch": a.QuoteStream.Cost} {
				key := a.Tier + ":" + kind
				if cost.Samples > 0 && (h[key].Requests == 0 || cost.LaneSeconds/cost.Requests > h[key].LaneSeconds/h[key].Requests) {
					h[key] = cost
				}
			}
		}
	}
	return h
}

func adaptiveScalar(c productCost, s *productAdaptiveState) float64 {
	return max(c.Requests/max(.001, s.RequestBudget), c.LaneSeconds/max(.001, s.LaneBudget))
}

// Allocate heavy streams first to the least loaded minute. Stable hashes break
// ties; phases and virtual debt survive process restarts.
func adaptiveInitialize(store *publicAccountImportProductStore, shops []PublicAccountImportShop, now time.Time) {
	needed := false
	for _, shop := range shops {
		a := store.Shops[shop.ID].Adaptive
		if a == nil || a.CatalogStream.Next.IsZero() || (a.EffectiveCount > 0 && a.QuoteStream.Next.IsZero()) {
			needed = true
			break
		}
	}
	if !needed {
		return
	}
	type pending struct {
		id     string
		kind   string
		period int
		cost   float64
		hash   string
	}
	queue := []pending{}
	loads := make([]float64, 4*7*24*60)
	for _, shop := range shops {
		c := store.Shops[shop.ID]
		if c.Adaptive == nil {
			c.Adaptive = &productAdaptiveShop{}
			store.Shops[shop.ID] = c
		}
		a := c.Adaptive
		catalog, quote := adaptivePeriods(a)
		for kind, period := range map[string]time.Duration{"catalog": catalog, "quote_batch": quote} {
			if kind == "quote_batch" && a.EffectiveCount == 0 {
				continue
			}
			p := max(1, int(period.Minutes()*max(1, store.Adaptive.Multiplier)))
			stream := &a.CatalogStream
			if kind == "quote_batch" {
				stream = &a.QuoteStream
			}
			cost := adaptiveScalar(adaptiveCost(a, kind, nil), store.Adaptive)
			if stream.Next.IsZero() {
				hash := sha256.Sum256([]byte(shop.ID + kind))
				queue = append(queue, pending{shop.ID, kind, p, cost, hex.EncodeToString(hash[:])})
			} else {
				for slot := stream.PhaseSeconds / 60; slot < len(loads); slot += p {
					loads[slot] += cost
				}
			}
		}
	}
	sort.Slice(queue, func(i, j int) bool {
		if queue[i].cost != queue[j].cost {
			return queue[i].cost > queue[j].cost
		}
		return queue[i].hash < queue[j].hash
	})
	for _, v := range queue {
		best := 0
		seed := 0
		for _, b := range []byte(v.hash[:8]) {
			seed = seed*31 + int(b)
		}
		seed &= 0x7fffffff
		for k := 0; k < v.period; k++ {
			slot := (seed + k) % v.period
			if k == 0 || loads[slot] < loads[best] {
				best = slot
			}
		}
		for k := best; k < len(loads); k += v.period {
			loads[k] += v.cost
		}
		a := store.Shops[v.id].Adaptive
		stream := &a.CatalogStream
		if v.kind == "quote_batch" {
			stream = &a.QuoteStream
		}
		stream.PhaseSeconds = best*60 + seed%60
		stream.Next = now.Add(time.Duration(stream.PhaseSeconds) * time.Second)
		// An unprobed shop waits at most one minute; admission itself is gated.
		if a.Generation == 0 {
			stream.Next = now.Add(time.Duration(seed%60) * time.Second)
		}
	}
}

func adaptiveSelectProducts(a *productAdaptiveShop, limit int, now time.Time) []int {
	priority, old := []int{}, []int{}
	for i, e := range a.Catalog {
		if e.Sellable {
			if e.Priority {
				priority = append(priority, i)
			} else {
				old = append(old, i)
			}
		}
	}
	less := func(list []int) {
		sort.Slice(list, func(i, j int) bool {
			l, r := a.Catalog[list[i]], a.Catalog[list[j]]
			lt, rt := l.LastQuoteAttemptAt, r.LastQuoteAttemptAt
			if lt.Equal(rt) {
				return l.GoodsKey < r.GoodsKey
			}
			return lt.Before(rt)
		})
	}
	less(priority)
	less(old)
	p := min(len(priority), limit/2)
	o := min(len(old), limit-p)
	p = min(len(priority), limit-o)
	return append(append([]int{}, priority[:p]...), old[:o]...)
}

func adaptiveVisibleProducts(c publicAccountImportProductShopCache) []PublicAccountImportProduct {
	if c.Adaptive == nil || c.Adaptive.Generation == 0 || !adaptiveProjectionEnabled() {
		return c.Products
	}
	products := []PublicAccountImportProduct{}
	for _, e := range c.Adaptive.Catalog {
		if e.Sellable && !e.QuoteUnavailable {
			p := e.Product
			p.MetadataVersion = e.MetadataVersion
			p.CatalogUpdatedAt = c.Adaptive.CatalogAt.Format(time.RFC3339Nano)
			p.QuotePending = e.QuoteVersion != e.MetadataVersion || p.QuoteVerifiedAt == ""
			products = append(products, p)
		}
	}
	return products
}

func adaptiveProjectionEnabled() bool {
	control, err := loadPublicAccountImportSyncControlLocked()
	return err == nil && control.Mode == "adaptive"
}

func adaptiveUpdateCompatibility(c *publicAccountImportProductShopCache, now time.Time) {
	a := c.Adaptive
	if a == nil || a.Generation == 0 {
		return
	}
	products := []PublicAccountImportProduct{}
	for _, e := range a.Catalog {
		if e.Sellable && !e.QuoteUnavailable {
			if e.QuoteVersion != e.MetadataVersion || !publicAccountImportProductQuoteIsValid(e.Product, now) {
				return
			}
			p := e.Product
			p.QuotePending = false
			p.CatalogUpdatedAt = ""
			products = append(products, p)
		}
	}
	c.Products = products
	c.SchemaVersion = 2
	c.SourceProductCount = len(a.Catalog)
	c.SellableProductCount = len(products)
	c.UnavailableProductCount = len(a.Catalog) - len(products)
	c.UpdatedAt = a.CatalogAt.Format(time.RFC3339Nano)
	if a.EffectiveCount > 0 && a.FirstQuoteAt.IsZero() {
		a.FirstQuoteAt = now
	}
}

func adaptiveRecordFailure(c *publicAccountImportProductShopCache, kind, message string, now time.Time, retry time.Duration) {
	a := c.Adaptive
	if a == nil {
		return
	}
	a.Failures++
	c.FailureCount++
	c.ConsecutiveFailures++
	c.LastFailureKind = kind
	c.LastFailureAt = now.Format(time.RFC3339Nano)
	c.Error = publicAccountImportProductSyncError(message)
	if a.FirstCatalogAt.IsZero() {
		a.FirstFailures++
	}
	if a.Lease != nil {
		a.NoProgressSeconds += max(0, now.Sub(a.Lease.Started).Seconds())
	}
	if a.FirstFailures >= 2 || a.Failures >= 3 || a.NoProgressSeconds >= 3600 || kind == "catalog_limit" {
		a.Exception = kind
		retry = max(retry, []time.Duration{6 * time.Hour, 12 * time.Hour, 24 * time.Hour}[min(max(a.Failures-2, 0), 2)])
	}
	a.RetryAt = now.Add(max(time.Minute, retry))
	a.Lease = nil
	c.SyncAttemptID = ""
	c.SyncStartedAt = ""
	c.SyncHeartbeatAt = ""
}

func validProductCost(c productCost) bool {
	for _, v := range []float64{c.Requests, c.Retries, c.LaneSeconds, c.VerificationSeconds} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100000 {
			return false
		}
	}
	return c.Requests >= 1 && c.Retries <= c.Requests && c.VerificationSeconds <= c.LaneSeconds
}

func adaptiveScope(scope string) bool { return scope == "catalog" || scope == "quote_batch" }

func adaptiveJobItem(e productCatalogEntry) PublicAccountImportProductSyncItem {
	p := e.Product
	return PublicAccountImportProductSyncItem{GoodsKey: e.GoodsKey, Name: p.Name, URL: p.URL, Image: p.Image, Category: p.Category, GoodsType: p.GoodsType, Price: &p.Price, MarketPrice: &p.MarketPrice, Stock: &p.Stock, MinimumQuantity: &p.MinimumQuantity, MetadataVersion: e.MetadataVersion}
}

func adaptiveSummary(a *productAdaptiveShop) string {
	if a == nil {
		return "pending"
	}
	if a.PauseReason != "" {
		return "pressure_paused"
	}
	if a.Exception != "" {
		return "exception"
	}
	if a.Generation == 0 {
		return "pending"
	}
	if a.EffectiveCount == 0 {
		return "dormant"
	}
	return fmt.Sprintf("%s", a.Tier)
}
