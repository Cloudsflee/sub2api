package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service/productselection"
	"github.com/gin-gonic/gin"
)

func solutionLibrary(c *gin.Context) (productselection.Library, bool) {
	c.Header("Cache-Control", "private, no-store")
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return productselection.Library{}, false
	}
	return productselection.Library{Dir: filepath.Join(filepath.Dir(publicAccountImportShopLinksPath()), "solution-library")}, true
}

func solutionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, productselection.ErrReport), errors.Is(err, productselection.ErrLimit):
		response.BadRequest(c, err.Error())
	case errors.Is(err, productselection.ErrConflict):
		response.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, productselection.ErrNotFound):
		response.NotFound(c, "Solution or report not found")
	default:
		response.InternalError(c, "Solution library storage unavailable")
	}
}

func decodeSolutionJSON(c *gin.Context, dst any, size int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, size)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil && dec.Decode(new(any)) == io.EOF {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		response.Error(c, http.StatusRequestEntityTooLarge, "Request is too large")
	} else {
		response.BadRequest(c, "Invalid solution JSON")
	}
	return false
}

func (h *AccountHandler) ListSolutions(c *gin.Context) {
	l, ok := solutionLibrary(c)
	if !ok {
		return
	}
	list, err := l.List()
	if err != nil {
		solutionError(c, err)
		return
	}
	response.Success(c, list)
}

type solutionAvailability struct {
	State   string   `json:"state"`
	QuoteAt string   `json:"quote_at,omitempty"`
	Cost    *float64 `json:"cost,omitempty"`
}

func solutionAvailabilityFor(offers []productselection.Offer) map[string]solutionAvailability {
	current := map[string]PublicAccountImportProduct{}
	shops, shopErr := snapshotPublicAccountImportShops()
	store, storeErr := snapshotPublicAccountImportProductStore()
	readOK := shopErr == nil && storeErr == nil
	if readOK {
		rows, _, _, _, _, _ := publicAccountImportProductSnapshot(supportedPublicAccountImportProductShops(shops), store, time.Now().UTC())
		for _, p := range rows {
			current[p.ID] = p
		}
	}
	result := make(map[string]solutionAvailability, len(offers))
	for _, o := range offers {
		v := solutionAvailability{State: "absent"}
		p, exists := current[o.ID]
		switch {
		case !readOK:
			v.State = "unknown"
		case !exists || p.ShopID != o.ShopID:
		case p.QuotePending || p.PayablePrice == nil || p.QuoteVerifiedAt == "":
			v.State = "unpriced"
		default:
			v.State = "listed"
			v.Cost = p.PayablePrice
			v.QuoteAt = p.QuoteVerifiedAt
			if o.Cost == nil || *o.Cost != *v.Cost || p.MinimumQuantity != o.Minimum {
				v.State = "changed"
			}
		}
		result[o.ID] = v
	}
	return result
}

func (h *AccountHandler) GetSolution(c *gin.Context) {
	l, ok := solutionLibrary(c)
	if !ok {
		return
	}
	d, err := l.Detail(c.Param("id"))
	if err != nil {
		solutionError(c, err)
		return
	}
	response.Success(c, gin.H{"detail": d, "availability": solutionAvailabilityFor(d.Offers)})
}

func (h *AccountHandler) EditSolution(c *gin.Context) {
	l, ok := solutionLibrary(c)
	if !ok {
		return
	}
	var req productselection.SolutionEdit
	if !decodeSolutionJSON(c, &req, 4<<20) {
		return
	}
	if err := l.Edit(c.Param("id"), req); err != nil {
		solutionError(c, err)
		return
	}
	response.Success(c, gin.H{"revision": req.Revision + 1})
}

func (h *AccountHandler) GetSolutionAudit(c *gin.Context) {
	l, ok := solutionLibrary(c)
	if !ok {
		return
	}
	r, err := l.ActiveReport(c.Query("version"))
	if err != nil {
		solutionError(c, err)
		return
	}
	response.Success(c, r.Audit())
}

// Offer dispositions remain inspectable even when an item did not qualify for a solution.
func (h *AccountHandler) GetSolutionOffers(c *gin.Context) {
	l, ok := solutionLibrary(c)
	if !ok {
		return
	}
	r, err := l.ActiveReport(c.Query("version"))
	if err != nil {
		solutionError(c, err)
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > 1000000 {
		response.BadRequest(c, "Invalid page")
		return
	}
	rows := []productselection.Offer{}
	total := 0
	for _, o := range r.Offers {
		if typ := c.Query("type"); typ != "" && o.Type != typ {
			continue
		}
		if use := c.Query("disposition"); use != "" && o.Disposition != use {
			continue
		}
		if total/50 == page-1 {
			rows = append(rows, o)
		}
		total++
	}
	response.Success(c, gin.H{"total": total, "offers": rows})
}

func (h *AccountHandler) ProcessSolutionReport(c *gin.Context) {
	l, ok := solutionLibrary(c)
	if !ok {
		return
	}
	var r productselection.SolutionReport
	if !decodeSolutionJSON(c, &r, productselection.MaxReportBytes) {
		return
	}
	for _, o := range r.Offers {
		u, err := url.Parse(o.URL)
		key := productGoodsKeyFromURL(o.URL)
		if err != nil || u.Scheme != "https" || u.User != nil || !publicAccountImportProductShopHostnameSupported(u.Hostname()) ||
			key == "" || publicAccountImportProductID(o.ShopID, key) != o.ID {
			response.BadRequest(c, "Product identity does not match its catalog link")
			return
		}
	}
	switch c.Param("action") {
	case "validate":
		if err := r.Validate(); err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		response.Success(c, r.Audit())
	case "import":
		added, err := l.Import(&r)
		if err != nil {
			solutionError(c, err)
			return
		}
		response.Success(c, gin.H{"imported": added, "version": r.Version})
	default:
		response.NotFound(c, "Unknown report action")
	}
}
