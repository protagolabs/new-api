package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

// SetAssetRouter registers the reference-asset endpoints.
//
// Deliberately NOT behind middleware.Distribute(): that middleware parses a
// `model` field out of the body to pick an upstream channel, and rejects
// requests without one. These endpoints talk to the vendor's control plane
// directly and carry no model, so distributing them would 400 every call.
func SetAssetRouter(router *gin.Engine) {
	assetRouter := router.Group("/v1")
	assetRouter.Use(middleware.CORS())
	assetRouter.Use(middleware.RouteTag("relay"))
	assetRouter.Use(middleware.TokenAuth())
	{
		// Creation reaches the vendor and consumes a slot in a 50-asset
		// account-wide pool, so it is rate limited per user.
		assetRouter.POST("/assets", middleware.UserCriticalRateLimit("asset-create"), controller.CreateAsset)
		assetRouter.GET("/assets", controller.ListAssets)
		assetRouter.GET("/assets/:id", controller.GetAsset)
		assetRouter.DELETE("/assets/:id", controller.DeleteAsset)
	}
}
