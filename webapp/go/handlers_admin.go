package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

type rankingRow struct {
	SellerID    int64  `db:"seller_id"`
	DisplayName string `db:"display_name"`
	TotalGMV    int64  `db:"total_gmv"`
	OrderCount  int64  `db:"order_count"`
}

func handleAdminRankings(c echo.Context) error {
	db := getDB(c)

	query := `
		SELECT u.id AS seller_id, u.display_name,
		       COALESCE(SUM(o.price - o.discount), 0) AS total_gmv, COUNT(o.id) AS order_count
		FROM users u
		LEFT JOIN products p ON p.seller_id = u.id
		LEFT JOIN orders o ON o.product_id = p.id
		WHERE u.role = 'seller'
		GROUP BY u.id, u.display_name
		ORDER BY total_gmv DESC
	`
	var rows []rankingRow
	if err := db.Select(&rows, query); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	result := make([]AdminRankingEntry, 0, len(rows))
	for _, r := range rows {
		result = append(result, AdminRankingEntry{
			SellerID:    r.SellerID,
			DisplayName: r.DisplayName,
			TotalGMV:    r.TotalGMV,
			OrderCount:  r.OrderCount,
		})
	}

	return c.JSON(http.StatusOK, result)
}

var adminProductSearchSort = map[string]string{
	"price_asc":       "p.price ASC, p.id ASC",
	"price_desc":      "p.price DESC, p.id ASC",
	"created_at_desc": "p.created_at DESC, p.id ASC",
}

// productSearchRow は GET /api/admin/products/search の一覧クエリの1行。
type productSearchRow struct {
	ID       int64  `db:"id"`
	Name     string `db:"name"`
	Category string `db:"category"`
	Price    uint32 `db:"price"`
	SellerID int64  `db:"seller_id"`
	LiveID   int64  `db:"live_id"`
}

func handleAdminProductsSearch(c echo.Context) error {
	db := getDB(c)

	category := c.QueryParam("category")
	if category != "" && !validCategories[category] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "category が不正です"})
	}

	var priceMin, priceMax *uint32
	if v := c.QueryParam("price_min"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "price_min が不正です"})
		}
		pm := uint32(n)
		priceMin = &pm
	}
	if v := c.QueryParam("price_max"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "price_max が不正です"})
		}
		pm := uint32(n)
		priceMax = &pm
	}

	sort := c.QueryParam("sort")
	if sort == "" {
		sort = "created_at_desc"
	}
	orderBy, ok := adminProductSearchSort[sort]
	if !ok {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "sort が不正です"})
	}

	page := 1
	if v := c.QueryParam("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "page が不正です"})
		}
		page = n
	}

	perPage := 20
	if v := c.QueryParam("per_page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "per_page が不正です"})
		}
		perPage = n
	}

	where := make([]string, 0, 3)
	args := make([]interface{}, 0, 3)
	if category != "" {
		where = append(where, "p.category = ?")
		args = append(args, category)
	}
	if priceMin != nil {
		where = append(where, "p.price >= ?")
		args = append(args, *priceMin)
	}
	if priceMax != nil {
		where = append(where, "p.price <= ?")
		args = append(args, *priceMax)
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var totalCount int64
	countQuery := "SELECT COUNT(*) FROM products p" + whereSQL
	if err := db.Get(&totalCount, countQuery, args...); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	offset := (page - 1) * perPage
	listQuery := "SELECT p.id, p.name, p.category, p.price, p.seller_id, p.live_id FROM products p" +
		whereSQL + " ORDER BY " + orderBy + " LIMIT ? OFFSET ?"
	listArgs := append(append([]interface{}{}, args...), perPage, offset)

	var rows []productSearchRow
	if err := db.Select(&rows, listQuery, listArgs...); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	products := make([]ProductSearchItem, 0, len(rows))
	for _, r := range rows {
		products = append(products, ProductSearchItem{
			ID:       r.ID,
			Name:     r.Name,
			Category: r.Category,
			Price:    r.Price,
			SellerID: r.SellerID,
			LiveID:   r.LiveID,
		})
	}

	return c.JSON(http.StatusOK, ProductSearchResponse{
		Products:   products,
		TotalCount: totalCount,
	})
}

// campaignRequest は POST /api/admin/campaign のリクエストボディ（campaign スペック
// 「セール強度の設定」）。
type campaignRequest struct {
	Level int `json:"level"`
}

func handleAdminCampaign(c echo.Context) error {
	db := getDB(c)

	var req campaignRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if req.Level < 1 || req.Level > 4 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "level は1〜4である必要があります"})
	}

	if _, err := db.Exec(
		"UPDATE settings SET value = ? WHERE name = 'campaign_level'",
		strconv.Itoa(req.Level),
	); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, map[string]int{"level": req.Level})
}
