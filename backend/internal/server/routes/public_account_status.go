package routes

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func RegisterPublicAccountStatusRoutes(v1 *gin.RouterGroup, h *handler.Handlers, redisClient *redis.Client) {
	status := v1.Group("/public/account-status")
	if redisClient != nil {
		status.Use(middleware.NewRateLimiter(redisClient).Limit("public-account-status", 120, time.Minute))
	}
	status.GET("/groups", h.PublicAccountStatus.ListGroups)
	status.GET("/groups/:group_id/accounts", h.PublicAccountStatus.ListAccounts)
}
