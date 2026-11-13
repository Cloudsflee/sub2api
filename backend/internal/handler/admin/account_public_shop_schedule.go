package admin

import "math"

// Only the public classification and current target periods leave the protected
// scheduler. Reading this projection never advances a stream or claims a job.
type PublicAccountImportShopSchedule struct {
	Tier           string `json:"tier"`
	State          string `json:"state"`
	EffectiveCount *int   `json:"effective_count"`
	CatalogSeconds int    `json:"catalog_seconds"`
	QuoteSeconds   int    `json:"quote_seconds"`
	TierPending    bool   `json:"tier_pending"`
}

func publicShopSchedule(shopID string, a *productAdaptiveShop, s *productAdaptiveState, control PublicAccountImportSyncControl) *PublicAccountImportShopSchedule {
	if control.Mode != "adaptive" {
		return nil
	}
	out := &PublicAccountImportShopSchedule{Tier: "pending", State: "pending"}
	if a != nil && a.Generation > 0 {
		count := a.EffectiveCount
		out.EffectiveCount = &count
		out.Tier = a.Tier
		out.State = "active"
		out.TierPending = a.TierChecks > 0 || (a.ZeroChecks == 1 && a.Tier != "closed")
		if out.Tier == "" {
			out.Tier = "pending"
		}
		catalog, quote := adaptivePeriods(a)
		multiplier := 4.0
		if s != nil {
			multiplier = max(1, s.Multiplier)
		}
		out.CatalogSeconds = int(math.Ceil(catalog.Seconds() * multiplier))
		out.QuoteSeconds = int(math.Ceil(quote.Seconds() * multiplier))
		if a.ZeroChecks == 1 && a.Tier != "closed" {
			out.CatalogSeconds = 600
		}
		if count == 0 {
			out.State = "dormant"
			out.QuoteSeconds = 0
		}
	}
	if a != nil && a.Exception != "" {
		out.State = "exception"
	}
	if a != nil && a.PauseReason != "" {
		out.State = "pressure_paused"
	}
	if control.AutomaticPaused || (s != nil && s.AutomaticHold != "") {
		out.State = "paused"
	}
	if _, isolated := control.QuarantinedShops[shopID]; isolated {
		out.State = "isolated"
	}
	if out.State != "active" && out.State != "dormant" {
		out.CatalogSeconds, out.QuoteSeconds = 0, 0
	}
	return out
}
