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
		publicImport.POST("", h.Admin.Account.PublicImportCodexSessions)
	}
}
