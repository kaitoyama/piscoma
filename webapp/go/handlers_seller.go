package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

const placeholderImageHex = "89504E470D0A1A0A0000000D4948445200000001000000010804000000B51C0C020000000B4944415478DA6364F80F00010501012718E3660000000049454E44AE426082"

// placeholderImage / placeholderImageHash はプレースホルダPNGのバイト列とその
// SHA-256ハッシュ（hex）。パッケージ初期化時に一度だけデコード・計算しておく
// （seed.sql の image / image_hash と同一の値になる）。
var (
	placeholderImage     []byte
	placeholderImageHash string
)

func init() {
	b, err := hex.DecodeString(placeholderImageHex)
	if err != nil {
		panic("invalid placeholder image hex: " + err.Error())
	}
	placeholderImage = b
	sum := sha256.Sum256(b)
	placeholderImageHash = hex.EncodeToString(sum[:])
}

const (
	dropDefaultDurationSeconds = 60
	dropMinDurationSeconds     = 10
	dropMaxDurationSeconds     = 600
	dropMinLeadSeconds         = 15
)

// sellerProductCreateRequest は POST /api/seller/products のリクエストボディ
// （seller-products スペック「商品の追加と在庫投入」）。
type sellerProductCreateRequest struct {
	LiveID      int64  `json:"live_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Price       uint32 `json:"price"`
	Quantity    uint32 `json:"quantity"`
}

func handleSellerCreateProduct(c echo.Context) error {
	db := getDB(c)

	sellerID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	var req sellerProductCreateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name は必須です"})
	}
	if !validCategories[req.Category] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "category が不正です"})
	}

	lv, err := findLive(db, req.LiveID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if lv == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "live not found"})
	}
	// seller-products スペック「他セラーの配信への追加」: live_id が自分の配信でない場合は4xx。
	if lv.SellerID != sellerID {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
	}

	tx, err := db.Beginx()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.Exec(
		`INSERT INTO products (seller_id, live_id, name, description, category, price, image, image_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sellerID, req.LiveID, req.Name, req.Description, req.Category, req.Price,
		placeholderImage, placeholderImageHash,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	productID, err := res.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if _, err := tx.Exec("INSERT INTO stocks (product_id, quantity) VALUES (?, ?)", productID, req.Quantity); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed = true

	return c.JSON(http.StatusCreated, SellerProductCreateResponse{ProductID: productID})
}

type sellerProductUpdateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Price       *uint32 `json:"price"`
	AddQuantity *int64  `json:"add_quantity"`
}

func handleSellerUpdateProduct(c echo.Context) error {
	db := getDB(c)

	sellerID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	productID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	var req sellerProductUpdateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if req.AddQuantity != nil && *req.AddQuantity <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "add_quantity は正の整数である必要があります"})
	}

	tx, err := db.Beginx()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var ownerID int64
	err = tx.Get(&ownerID, "SELECT seller_id FROM products WHERE id = ?", productID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if ownerID != sellerID {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
	}

	if _, err := tx.Exec(
		`UPDATE products
		 SET name = COALESCE(?, name),
		     description = COALESCE(?, description),
		     price = COALESCE(?, price)
		 WHERE id = ?`,
		req.Name, req.Description, req.Price, productID,
	); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if req.AddQuantity != nil {
		if _, err := tx.Exec(
			"UPDATE stocks SET quantity = quantity + ? WHERE product_id = ?",
			*req.AddQuantity, productID,
		); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed = true

	return c.JSON(http.StatusOK, SellerProductCreateResponse{ProductID: productID})
}

type sellerDropCreateRequest struct {
	LiveID          int64  `json:"live_id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Category        string `json:"category"`
	Price           uint32 `json:"price"`
	Quantity        uint32 `json:"quantity"`
	StartsAt        string `json:"starts_at"`
	DurationSeconds *int   `json:"duration_seconds"`
}

func handleSellerCreateDrop(c echo.Context) error {
	db := getDB(c)

	sellerID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	var req sellerDropCreateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name は必須です"})
	}
	if !validCategories[req.Category] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "category が不正です"})
	}

	startsAt, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "starts_at はRFC3339形式である必要があります"})
	}

	duration := dropDefaultDurationSeconds
	if req.DurationSeconds != nil {
		duration = *req.DurationSeconds
	}
	if duration < dropMinDurationSeconds || duration > dropMaxDurationSeconds {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "duration_seconds は10〜600の範囲である必要があります"})
	}

	lv, err := findLive(db, req.LiveID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if lv == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "live not found"})
	}
	if lv.SellerID != sellerID {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
	}

	var nowTs time.Time
	if err := db.Get(&nowTs, "SELECT NOW(3)"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !startsAt.After(nowTs.Add(dropMinLeadSeconds * time.Second)) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "starts_at は現在時刻より15秒以上先である必要があります"})
	}

	endsAt := startsAt.Add(time.Duration(duration) * time.Second)

	tx, err := db.Beginx()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.Exec(
		`INSERT INTO products (seller_id, live_id, name, description, category, price, image, image_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sellerID, req.LiveID, req.Name, req.Description, req.Category, req.Price,
		placeholderImage, placeholderImageHash,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	productID, err := res.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if _, err := tx.Exec("INSERT INTO stocks (product_id, quantity) VALUES (?, ?)", productID, req.Quantity); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	dropRes, err := tx.Exec(
		`INSERT INTO drops (live_id, product_id, scheduled_at, ends_at, status, announced)
		 VALUES (?, ?, ?, ?, 'scheduled', 0)`,
		req.LiveID, productID, startsAt, endsAt,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	dropID, err := dropRes.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed = true

	return c.JSON(http.StatusCreated, SellerDropCreateResponse{
		DropID:    dropID,
		ProductID: productID,
		StartsAt:  startsAt.Format("2006-01-02T15:04:05.000Z07:00"),
		EndsAt:    endsAt.Format("2006-01-02T15:04:05.000Z07:00"),
	})
}
