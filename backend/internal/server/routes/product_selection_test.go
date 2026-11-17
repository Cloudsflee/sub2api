package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/service/productselection"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProductSelectionRoutesIsolationAndValidation(t *testing.T) {
	t.Setenv("PUBLIC_ACCOUNT_IMPORT_ENABLED", "true")
	t.Setenv("PUBLIC_ACCOUNT_IMPORT_SHOP_LINKS_FILE", filepath.Join(t.TempDir(), "shops.json"))
	gin.SetMode(gin.TestMode)
	h := &handler.Handlers{Admin: &handler.AdminHandlers{Account: admin.NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)}}
	jwt := middleware.JWTAuthMiddleware(func(c *gin.Context) {
		id := int64(17)
		switch c.GetHeader("Authorization") {
		case "Bearer alice", "Bearer admin":
		case "Bearer bob":
			id = 18
		default:
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: id})
		c.Set(string(middleware.ContextKeyUserRole), service.RoleUser)
		c.Next()
	})
	router := gin.New()
	v1 := router.Group("/api/v1")
	RegisterUserRoutes(v1, h, jwt, middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), nil, &middleware.PanelRateLimiter{})
	protected := v1.Group("/admin", gin.HandlerFunc(jwt), func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer admin" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	registerAdminPublicAccountImportRoutes(protected, h)
	request := func(method, path, user string, body any) *httptest.ResponseRecorder {
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			require.NoError(t, err)
		}
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(data))
		if user != "" {
			r.Header.Set("Authorization", "Bearer "+user)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	path := "/user/product-selections"
	d := productselection.Data{Title: "private note", Stage: "review", PriceBasis: "pending", MinimumQuantity: 1}
	body := gin.H{"request_id": "test-1", "record": d}
	for _, method := range []string{"GET", "POST", "PUT"} {
		url := path
		if method == "PUT" {
			url += "/test"
		}
		require.Equal(t, http.StatusUnauthorized, request(method, url, "", body).Code)
	}
	w := request("POST", path, "alice", body)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	var saved struct {
		Data struct {
			Record productselection.Record `json:"record"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &saved))
	id := saved.Data.Record.ID
	require.NotEmpty(t, id)
	require.Equal(t, http.StatusOK, request("POST", path, "alice", body).Code)
	require.Contains(t, request("GET", path, "bob", nil).Body.String(), `"records":[]`)
	update := gin.H{"revision": 1, "record": d}
	require.Equal(t, http.StatusNotFound, request("PUT", path+"/"+id, "bob", update).Code)
	require.Equal(t, http.StatusOK, request("PUT", path+"/"+id, "alice", update).Code)
	require.Equal(t, http.StatusConflict, request("PUT", path+"/"+id, "alice", update).Code)
	for _, invalid := range []gin.H{
		{"request_id": "../../bad", "record": d},
		{"request_id": "owner-forgery", "user_id": 18, "record": d},
		{"request_id": "oversize", "record": gin.H{"title": strings.Repeat("x", productselection.MaxRequestBytes)}},
	} {
		code := request("POST", path, "alice", invalid).Code
		require.Contains(t, []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge}, code)
	}
	for _, user := range []string{"", "alice", "admin"} {
		expected := map[string]int{"": 401, "alice": 403, "admin": 200}[user]
		require.Equal(t, expected, request("GET", "/admin/public-account-import/selection-research", user, nil).Code)
	}
	require.Equal(t, 404, request("GET", "/public/account-import/selection-research", "", nil).Code)
	t.Setenv("PUBLIC_ACCOUNT_IMPORT_ENABLED", "false")
	require.Equal(t, 404, request("GET", path, "alice", nil).Code)
}
