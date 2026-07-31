package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func RegisterPublicAccountImportRoutes(v1 *gin.RouterGroup, h *handler.Handlers, _ *redis.Client) {
	publicImport := v1.Group("/public/account-import")
	{
		publicImport.GET("/groups", h.Admin.Account.ListPublicAccountImportGroups)
		publicImport.GET("/shops", h.Admin.Account.ListPublicAccountImportShops)
		publicImport.POST("/shops", h.Admin.Account.SubmitPublicAccountImportShop)
		publicImport.GET("/products", h.Admin.Account.ListPublicAccountImportProducts)
		publicImport.GET("/products/sync-job", h.Admin.Account.GetPublicAccountImportProductSyncJob)
		publicImport.POST("/products/refresh", h.Admin.Account.RequestPublicAccountImportProductRefresh)
		publicImport.POST("/products/sync", h.Admin.Account.SubmitPublicAccountImportProductSync)
		publicImport.POST("/products/sync-failure", h.Admin.Account.FailPublicAccountImportProductSync)
		publicImport.POST("/products/sync-heartbeat", h.Admin.Account.HeartbeatPublicAccountImportProductSync)
		publicImport.POST("", h.Admin.Account.PublicImportCodexSessions)
	}
}

func registerAdminPublicAccountImportRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	shops := admin.Group("/public-account-import/shops")
	{
		shops.PATCH("/:id", h.Admin.Account.UpdatePublicAccountImportShopTrustLevel)
		shops.DELETE("/:id", h.Admin.Account.DeletePublicAccountImportShop)
	}
}
