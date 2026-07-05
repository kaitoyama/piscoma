package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func registerRoutes(e *echo.Echo, cfg config) {
	api := e.Group("/api")

	// --- インフラ用ヘルスチェック（仕様書 §6 の29本には含まれない。
	//     Docker Compose の healthcheck からのみ叩かれる想定） ---
	api.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// --- 共通（認証不要） ---
	api.POST("/initialize", handleInitialize(cfg))
	api.POST("/register", handleRegister)
	api.POST("/login", handleLogin)

	// --- 視聴者向け（要認証。ロールは問わない = viewer/seller/admin いずれでも可） ---
	viewer := api.Group("", requireAuth)
	viewer.GET("/feed", handleFeed)
	viewer.GET("/lives/:id", handleLiveDetail)
	viewer.GET("/lives/:id/comments", handleListComments)
	viewer.POST("/lives/:id/comments", handlePostComment) // セラーの返信もこのエンドポイントを共用（§6 注記）
	viewer.GET("/lives/:id/products", handleShelf)
	viewer.GET("/lives/:id/notifications", handleListNotifications)
	viewer.GET("/lives/:id/purchases", handleListPurchases)
	viewer.POST("/lives/:id/reaction", handleReaction)
	viewer.POST("/coupons/:id/claim", handleClaimCoupon)
	viewer.GET("/products/:id/image", handleGetProductImage)
	viewer.GET("/users/:id/icon", handleGetUserIcon)
	viewer.POST("/icon", handleUpdateIcon)
	viewer.POST("/products/:id/cart", handleCreateCart)
	viewer.GET("/carts/:id", handleGetCart)
	viewer.DELETE("/carts/:id", handleDeleteCart)
	viewer.POST("/carts/:id/checkout", handleCheckout)
	viewer.GET("/orders", handleListOrders)

	// --- セラー向け（要認証 + role=seller） ---
	seller := api.Group("/seller", requireAuth, requireRole(RoleSeller))
	seller.GET("/sales", handleSellerSales)
	seller.POST("/products", handleSellerCreateProduct)
	seller.PUT("/products/:id", handleSellerUpdateProduct)
	seller.POST("/drops", handleSellerCreateDrop)
	seller.PUT("/lives/:id/pin", handleSellerUpdatePin)
	seller.POST("/coupons", handleSellerCreateCoupon)

	// --- 運営向け（要認証 + role=admin） ---
	admin := api.Group("/admin", requireAuth, requireRole(RoleAdmin))
	admin.GET("/rankings", handleAdminRankings)
	admin.GET("/products/search", handleAdminProductsSearch)
	admin.POST("/campaign", handleAdminCampaign)

	// --- 内部（systemd timer からのみ起動される想定。ユーザーセッションは持たないため認証ガード対象外） ---
	api.POST("/internal/sweep", handleSweep)
}
