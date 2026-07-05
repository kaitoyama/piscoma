package main

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

// orderListRow は GET /api/orders の JOIN 結果（1オーダー1行。products と JOIN して
// 商品名も同時に引く）。
type orderListRow struct {
	OrderID     int64     `db:"order_id"`
	Price       uint32    `db:"price"`
	Discount    uint32    `db:"discount"`
	CreatedAt   time.Time `db:"created_at"`
	ProductID   int64     `db:"product_id"`
	ProductName string    `db:"product_name"`
}

// handleListOrders は GET /api/orders。
// orders スペック「自分の注文履歴」の実装（新しい順・他ユーザーの注文は含めない）。
func handleListOrders(c echo.Context) error {
	db := getDB(c)

	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	var rows []orderListRow
	query := `
		SELECT o.id AS order_id, o.price, o.discount, o.created_at,
		       p.id AS product_id, p.name AS product_name
		FROM orders o
		JOIN products p ON p.id = o.product_id
		WHERE o.user_id = ?
		ORDER BY o.created_at DESC, o.id DESC
	`
	if err := db.Select(&rows, query, userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	result := make([]OrderListItem, 0, len(rows))
	for _, r := range rows {
		result = append(result, OrderListItem{
			OrderID:   r.OrderID,
			Product:   OrderProduct{ID: r.ProductID, Name: r.ProductName},
			Price:     r.Price,
			Discount:  r.Discount,
			Total:     r.Price - r.Discount,
			CreatedAt: r.CreatedAt,
		})
	}

	return c.JSON(http.StatusOK, result)
}

type sellerSalesRow struct {
	ProductID int64  `db:"id"`
	Name      string `db:"name"`
	SoldCount int64  `db:"sold_count"`
	GMV       int64  `db:"gmv"`
}

func handleSellerSales(c echo.Context) error {
	db := getDB(c)

	sellerID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	var rows []sellerSalesRow
	query := `
		SELECT p.id, p.name, COUNT(o.id) AS sold_count, COALESCE(SUM(o.price - o.discount), 0) AS gmv
		FROM products p LEFT JOIN orders o ON o.product_id = p.id
		WHERE p.seller_id = ?
		GROUP BY p.id, p.name
	`
	if err := db.Select(&rows, query, sellerID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	byProduct := make([]SellerSalesProduct, 0, len(rows))
	var totalGMV int64
	var orderCount int64
	for _, r := range rows {
		byProduct = append(byProduct, SellerSalesProduct{
			ProductID: r.ProductID,
			Name:      r.Name,
			SoldCount: r.SoldCount,
			GMV:       r.GMV,
		})
		totalGMV += r.GMV
		orderCount += r.SoldCount
	}

	return c.JSON(http.StatusOK, SellerSalesResponse{
		TotalGMV:   totalGMV,
		OrderCount: orderCount,
		ByProduct:  byProduct,
	})
}
