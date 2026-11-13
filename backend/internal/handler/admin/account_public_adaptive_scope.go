package admin

import "time"

// Queue observations and dispatch must use the same collectable shop set.
// Remove only empty scheduler records created by the old observation path;
// shop registration and any previously collected catalog/quotes are retained.
func adaptiveReconcileShopScope(store *publicAccountImportProductStore, shops []PublicAccountImportShop, now time.Time) []PublicAccountImportShop {
	shops = supportedPublicAccountImportProductShops(shops)
	allowed := make(map[string]bool, len(shops))
	for _, shop := range shops {
		allowed[shop.ID] = true
	}
	removed := false
	for id, cached := range store.Shops {
		a := cached.Adaptive
		if allowed[id] || a == nil || a.Generation != 0 || len(a.Catalog) != 0 || a.Lease != nil {
			continue
		}
		cached.Adaptive = nil
		store.Shops[id] = cached
		removed = true
	}
	if removed && store.Adaptive != nil {
		s := store.Adaptive
		s.QueueScopeSince = now
		s.StableSince = time.Time{}
		s.ValidatedSince = now
		s.AdmissionHold = "queue scope corrected; waiting for 15 stable minutes"
		// Keep all host/upstream evidence and hard holds. Only queue-derived
		// pressure timing is invalidated when no real upstream pressure exists.
		upstream := false
		for _, sample := range s.History {
			upstream = upstream || sample.Pressure
		}
		if !upstream {
			s.PressureSince = time.Time{}
		}
	}
	return shops
}
