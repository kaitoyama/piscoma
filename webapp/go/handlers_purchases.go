package main

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

type purchaseOrderRow struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	ProductID int64     `db:"product_id"`
	CreatedAt time.Time `db:"created_at"`
}

// PurchaseBuyer は購入速報の buyer フィールド（display_name のみ。email 等の
// 個人情報は含めない。§5.4）。
type PurchaseBuyer struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
	IconBase64  string `json:"icon_base64"`
}

// PurchaseProduct は購入速報の product フィールド（id, name のみ）。
type PurchaseProduct struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// PurchaseEntry は GET /api/lives/:id/purchases の1要素
// （purchase-feed スペック「購入速報一覧の取得」）。
type PurchaseEntry struct {
	OrderID        int64           `json:"order_id"`
	Buyer          PurchaseBuyer   `json:"buyer"`
	Product        PurchaseProduct `json:"product"`
	RemainingStock uint32          `json:"remaining_stock"`
	CreatedAt      time.Time       `json:"created_at"`
}

func handleListPurchases(c echo.Context) error {
	db := getDB(c)

	liveID, err := parseLiveID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	lv, err := findLive(db, liveID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if lv == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "live not found"})
	}

	var orders []purchaseOrderRow
	err = db.Select(&orders,
		"SELECT id, user_id, product_id, created_at FROM orders WHERE live_id = ? ORDER BY created_at DESC, id DESC",
		liveID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	result := make([]PurchaseEntry, 0, len(orders))
	for _, o := range orders {
		// 1. 購入者の表示名のみ（email は SELECT しない。§5.4 個人情報混入の構造的防止）。
		var displayName string
		if err := db.Get(&displayName, "SELECT display_name FROM users WHERE id = ?", o.UserID); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}

		var iconImage []byte
		err := db.Get(&iconImage, "SELECT image FROM user_icons WHERE user_id = ?", o.UserID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				iconImage = placeholderImage
			} else {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
		}

		// 3. 商品名。
		var productName string
		if err := db.Get(&productName, "SELECT name FROM products WHERE id = ?", o.ProductID); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}

		// 4. 残り在庫（§8: 取得後2秒以内のキャッシュは許容するが、初期実装は都度引く）。
		var qty uint32
		err = db.Get(&qty, "SELECT quantity FROM stocks WHERE product_id = ?", o.ProductID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				qty = 0
			} else {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
		}

		result = append(result, PurchaseEntry{
			OrderID: o.ID,
			Buyer: PurchaseBuyer{
				ID:          o.UserID,
				DisplayName: displayName,
				IconBase64:  base64.StdEncoding.EncodeToString(iconImage),
			},
			Product: PurchaseProduct{
				ID:   o.ProductID,
				Name: productName,
			},
			RemainingStock: qty,
			CreatedAt:      o.CreatedAt,
		})
	}

	return c.JSON(http.StatusOK, result)
}
