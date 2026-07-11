package routes

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func RegisterPublicAccountImportRoutes(v1 *gin.RouterGroup, h *handler.Handlers, redisClient *redis.Client) {
	rateLimiter := middleware.NewRateLimiter(redisClient)
	publicImport := v1.Group("/public/account-import")
	{
		publicImport.GET("/groups", h.Admin.Account.ListPublicAccountImportGroups)
		publicImport.GET("/shops", h.Admin.Account.ListPublicAccountImportShops)
		publicImport.POST("/shops", h.Admin.Account.SubmitPublicAccountImportShop)
		publicImport.POST(
			"",
			rateLimiter.LimitWithOptions("public-account-import-minute", 5, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			rateLimiter.LimitWithOptions("public-account-import-hour", 30, time.Hour, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Admin.Account.PublicImportCodexSessions,
		)
	}
}
