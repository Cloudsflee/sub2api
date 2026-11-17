package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service/productselection"
	"github.com/gin-gonic/gin"
)

func selectionOwner(c *gin.Context) (int64, bool) {
	c.Header("Cache-Control", "private, no-store")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return 0, false
	}
	return subject.UserID, true
}

func selectionStore() productselection.Store {
	return productselection.Store{Dir: filepath.Join(filepath.Dir(publicAccountImportShopLinksPath()), "product-selections")}
}

func (h *AccountHandler) ListProductSelections(c *gin.Context) {
	owner, ok := selectionOwner(c)
	if !ok {
		return
	}
	records, err := selectionStore().List(owner)
	if err != nil {
		response.InternalError(c, "Failed to load selection records")
		return
	}
	response.Success(c, gin.H{"records": records})
}

func (h *AccountHandler) GetProductSelectionResearch(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}
	// This handler is registered on the existing admin-authenticated route group.
	response.Success(c, json.RawMessage(productselection.Research))
}

type productSelectionRequest struct {
	RequestID string                `json:"request_id"`
	Revision  int64                 `json:"revision"`
	Record    productselection.Data `json:"record"`
}

func decodeSelection(c *gin.Context) (productSelectionRequest, bool) {
	var req productSelectionRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, productselection.MaxRequestBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			response.Error(c, http.StatusRequestEntityTooLarge, "Selection record is too large")
		} else {
			response.BadRequest(c, "Invalid selection record")
		}
		return req, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		response.BadRequest(c, "Invalid selection record")
		return req, false
	}
	if err := productselection.Validate(&req.Record); err != nil {
		response.BadRequest(c, err.Error())
		return req, false
	}
	return req, true
}

func (h *AccountHandler) CreateProductSelection(c *gin.Context) {
	owner, ok := selectionOwner(c)
	if !ok {
		return
	}
	req, ok := decodeSelection(c)
	if !ok {
		return
	}
	if len(req.RequestID) == 0 || len(req.RequestID) > 100 {
		response.BadRequest(c, "Request ID is required")
		return
	}
	record, created, err := selectionStore().Create(owner, req.RequestID, req.Record)
	if err != nil {
		selectionError(c, err)
		return
	}
	result := gin.H{"record": record, "created": created}
	if created {
		response.Created(c, result)
	} else {
		response.Success(c, result)
	}
}

func (h *AccountHandler) UpdateProductSelection(c *gin.Context) {
	owner, ok := selectionOwner(c)
	if !ok {
		return
	}
	req, ok := decodeSelection(c)
	if !ok {
		return
	}
	record, err := selectionStore().Update(owner, c.Param("id"), req.Revision, req.Record)
	if err != nil {
		selectionError(c, err)
		return
	}
	response.Success(c, record)
}

func selectionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, productselection.ErrConflict):
		response.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, productselection.ErrNotFound):
		response.NotFound(c, err.Error())
	case errors.Is(err, productselection.ErrLimit):
		response.BadRequest(c, err.Error())
	case errors.Is(err, productselection.ErrRequestID):
		response.BadRequest(c, err.Error())
	default:
		response.InternalError(c, "Failed to save selection record")
	}
}
