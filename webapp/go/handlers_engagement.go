package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-sql-driver/mysql"
	"github.com/labstack/echo/v4"
)

// sellerPinRequest は PUT /api/seller/lives/:id/pin のリクエストボディ。
type sellerPinRequest struct {
	ProductID int64 `json:"product_id"`
}

// pinProductRow はピン留め対象商品の所属確認・通知payload組み立てに使う行。
type pinProductRow struct {
	ID     int64  `db:"id"`
	LiveID int64  `db:"live_id"`
	Name   string `db:"name"`
	Price  uint32 `db:"price"`
}

// SellerPinResponse は PUT /api/seller/lives/:id/pin の200レスポンス。
type SellerPinResponse struct {
	ProductID int64  `json:"product_id"`
	Name      string `json:"name"`
	Price     uint32 `json:"price"`
}

func handleSellerUpdatePin(c echo.Context) error {
	db := getDB(c)

	sellerID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	liveID, err := parseLiveID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	var req sellerPinRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	lv, err := findLive(db, liveID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if lv == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "live not found"})
	}
	// live-pin スペック「他セラーの配信は4xx」
	if lv.SellerID != sellerID {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
	}

	var prod pinProductRow
	err = db.Get(&prod, "SELECT id, live_id, name, price FROM products WHERE id = ?", req.ProductID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if prod.LiveID != liveID {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "product is not part of this live"})
	}

	if _, err := db.Exec("UPDATE lives SET pinned_product_id = ? WHERE id = ?", prod.ID, liveID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if err := notifyBroadcast(db, liveID, "pin_changed", map[string]interface{}{
		"product_id": prod.ID,
		"name":       prod.Name,
		"price":      prod.Price,
	}); err != nil {
		log.Printf("pin: failed to notify live_id=%d: %v", liveID, err)
	}

	return c.JSON(http.StatusOK, SellerPinResponse{
		ProductID: prod.ID,
		Name:      prod.Name,
		Price:     prod.Price,
	})
}

// sellerCouponCreateRequest は POST /api/seller/coupons のリクエストボディ。
type sellerCouponCreateRequest struct {
	LiveID     int64 `json:"live_id"`
	Amount     int64 `json:"amount"`
	TotalCount int64 `json:"total_count"`
}

// SellerCouponCreateResponse は POST /api/seller/coupons の201レスポンス。
type SellerCouponCreateResponse struct {
	CouponID int64 `json:"coupon_id"`
}

// handleSellerCreateCoupon は POST /api/seller/coupons。
// coupons スペック「クーポンの発行」の実装。remaining_count は total_count で初期化する。
func handleSellerCreateCoupon(c echo.Context) error {
	db := getDB(c)

	sellerID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	var req sellerCouponCreateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if req.Amount <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "amount は正の整数である必要があります"})
	}
	if req.TotalCount <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "total_count は正の整数である必要があります"})
	}

	lv, err := findLive(db, req.LiveID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if lv == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "live not found"})
	}
	// coupons スペック「他セラーの配信・不正な値は4xx」
	if lv.SellerID != sellerID {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
	}

	res, err := db.Exec(
		`INSERT INTO coupons (live_id, seller_id, amount, total_count, remaining_count)
		 VALUES (?, ?, ?, ?, ?)`,
		req.LiveID, sellerID, req.Amount, req.TotalCount, req.TotalCount,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	couponID, err := res.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// coupons スペック「発行と通知」
	if err := notifyBroadcast(db, req.LiveID, "coupon_issued", map[string]interface{}{
		"coupon_id":   couponID,
		"amount":      req.Amount,
		"total_count": req.TotalCount,
	}); err != nil {
		log.Printf("coupon: failed to notify live_id=%d coupon_id=%d: %v", req.LiveID, couponID, err)
	}

	return c.JSON(http.StatusCreated, SellerCouponCreateResponse{CouponID: couponID})
}

type couponClaimLockRow struct {
	RemainingCount uint32 `db:"remaining_count"`
	Amount         uint32 `db:"amount"`
}

// CouponClaimResponse は POST /api/coupons/:id/claim の201レスポンス。
type CouponClaimResponse struct {
	ClaimID  int64  `json:"claim_id"`
	CouponID int64  `json:"coupon_id"`
	Amount   uint32 `json:"amount"`
}

// isDuplicateKeyError は MySQL の UNIQUE 制約違反（ER_DUP_ENTRY = 1062）かどうかを判定する。
func isDuplicateKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func handleClaimCoupon(c echo.Context) error {
	db := getDB(c)

	couponID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
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

	var coupon couponClaimLockRow
	err = tx.Get(&coupon, "SELECT remaining_count, amount FROM coupons WHERE id = ? FOR UPDATE", couponID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "coupon not found"})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if coupon.RemainingCount == 0 {
		return c.JSON(http.StatusConflict, map[string]string{"error": "coupon exhausted"})
	}

	res, err := tx.Exec("INSERT INTO coupon_claims (coupon_id, user_id) VALUES (?, ?)", couponID, userID)
	if err != nil {
		if isDuplicateKeyError(err) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "already claimed"})
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	claimID, err := res.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if _, err := tx.Exec("UPDATE coupons SET remaining_count = remaining_count - 1 WHERE id = ?", couponID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed = true

	return c.JSON(http.StatusCreated, CouponClaimResponse{
		ClaimID:  claimID,
		CouponID: couponID,
		Amount:   coupon.Amount,
	})
}

func handleReaction(c echo.Context) error {
	db := getDB(c)

	liveID, err := parseLiveID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	lv, err := findLive(db, liveID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if lv == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "live not found"})
	}

	if _, err := db.Exec("INSERT INTO reactions (live_id, user_id) VALUES (?, ?)", liveID, userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.NoContent(http.StatusCreated)
}
