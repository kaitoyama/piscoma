package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

type checkoutCartRow struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	ProductID int64     `db:"product_id"`
	LiveID    int64     `db:"live_id"`
	Status    string    `db:"status"`
	ExpiresAt time.Time `db:"expires_at"`
	NowTs     time.Time `db:"now_ts"`
}

type checkoutRequest struct {
	CouponID *int64 `json:"coupon_id"`
}

type checkoutCouponClaimRow struct {
	ClaimID int64  `db:"id"`
	Amount  uint32 `db:"amount"`
	LiveID  int64  `db:"live_id"`
}

func handleCheckout(c echo.Context) error {
	db := getDB(c)
	pc := getPaymentClient(c)

	cartID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	clientIdempotencyKey := c.Request().Header.Get("Idempotency-Key")
	if clientIdempotencyKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Idempotency-Key header is required"})
	}

	var req checkoutRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
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

	var cart checkoutCartRow
	err = tx.Get(&cart,
		"SELECT id, user_id, product_id, live_id, status, expires_at, NOW(3) AS now_ts FROM carts WHERE id = ? FOR UPDATE",
		cartID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "cart not found"})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if cart.UserID != userID || cart.Status != "active" {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "cart not found"})
	}

	if !cart.ExpiresAt.After(cart.NowTs) {
		return c.JSON(http.StatusGone, map[string]string{"error": "cart expired"})
	}

	var price uint32
	if err := tx.Get(&price, "SELECT price FROM products WHERE id = ?", cart.ProductID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	var discount uint32
	var couponClaimID *int64
	if req.CouponID != nil {
		var claim checkoutCouponClaimRow
		err := tx.Get(&claim, `
			SELECT cc.id, c.amount, c.live_id
			FROM coupon_claims cc
			JOIN coupons c ON c.id = cc.coupon_id
			WHERE cc.coupon_id = ? AND cc.user_id = ? AND cc.used_order_id IS NULL
			FOR UPDATE
		`, *req.CouponID, userID)
		if errors.Is(err, sql.ErrNoRows) {
			// checkout スペック「未取得クーポン」「二重使用」: 未取得・使用済みは400
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "coupon not claimed or already used"})
		}
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		// checkout スペック「対象外配信の拒否」: クーポンの live_id がカートの live_id と
		// 一致しない場合は400
		if claim.LiveID != cart.LiveID {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "coupon is not valid for this live"})
		}
		if claim.Amount < price {
			discount = claim.Amount
		} else {
			discount = price
		}
		id := claim.ClaimID
		couponClaimID = &id
	}
	total := price - discount

	paymentIdempotencyKey := "cart-" + strconv.FormatInt(cart.ID, 10)
	if total > 0 {
		if _, err := pc.Charge(paymentIdempotencyKey, int64(total)); err != nil {
			return c.JSON(http.StatusBadGateway, map[string]string{"error": "payment failed"})
		}
	}

	res, err := tx.Exec(
		`INSERT INTO orders (cart_id, user_id, product_id, live_id, price, discount, coupon_claim_id, idempotency_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		cart.ID, userID, cart.ProductID, cart.LiveID, price, discount, couponClaimID, clientIdempotencyKey,
	)
	if err != nil {
		if isDuplicateKeyError(err) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "idempotency key already used for a different checkout"})
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	orderID, err := res.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if couponClaimID != nil {
		claimRes, err := tx.Exec(
			"UPDATE coupon_claims SET used_order_id = ? WHERE id = ? AND used_order_id IS NULL",
			orderID, *couponClaimID,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		affected, err := claimRes.RowsAffected()
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if affected == 0 {
			return c.JSON(http.StatusConflict, map[string]string{"error": "coupon already used"})
		}
	}

	if _, err := tx.Exec("UPDATE carts SET status = 'checked_out' WHERE id = ?", cart.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed = true

	if err := notifyUser(db, cart.LiveID, userID, "checkout_result", map[string]interface{}{
		"order_id":   orderID,
		"cart_id":    cart.ID,
		"product_id": cart.ProductID,
		"status":     "succeeded",
	}); err != nil {
		log.Printf("checkout: failed to notify order_id=%d: %v", orderID, err)
	}

	return c.JSON(http.StatusOK, CheckoutResponse{
		OrderID:   orderID,
		ProductID: cart.ProductID,
		Price:     price,
		Discount:  discount,
		Total:     total,
	})
}
