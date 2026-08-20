package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUserAccountImportUpstreamRouteIsBehindJWTForRegularUsers(t *testing.T) {
	t.Setenv("PUBLIC_ACCOUNT_IMPORT_ENABLED", "false")
	gin.SetMode(gin.TestMode)

	accountHandler := admin.NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Account: accountHandler}}
	jWT := middleware.JWTAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer user-token" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 17})
		c.Set(string(middleware.ContextKeyUserRole), service.RoleUser)
		c.Next()
	})
	audit := middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })

	router := gin.New()
	RegisterUserRoutes(
		router.Group("/api/v1"),
		handlers,
		jWT,
		audit,
		nil,
		&middleware.PanelRateLimiter{},
	)

	unauthenticated := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/user/account-import/upstream", nil)
	router.ServeHTTP(unauthenticated, request)
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code)

	authenticated := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/user/account-import/upstream", nil)
	request.Header.Set("Authorization", "Bearer user-token")
	router.ServeHTTP(authenticated, request)
	require.Equal(t, http.StatusNotFound, authenticated.Code)
	require.Contains(t, authenticated.Body.String(), "Public account import is disabled")
}
