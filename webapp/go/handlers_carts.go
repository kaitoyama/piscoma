package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

const cartTTLSeconds = 60

type cartProductRow struct {
	ID     int64 `db:"id"`
	LiveID int64 `db:"live_id"`
}

type cartTimingRow struct {
	ExpiresAt time.Time `db:"expires_at"`
	NowTs     time.Time `db:"now_ts"`
}

// cartRow は GET /api/carts/:id 用にカートの現在状態一式を取得する行。
type cartRow struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	ProductID int64     `db:"product_id"`
	Status    string    `db:"status"`
	ExpiresAt time.Time `db:"expires_at"`
	NowTs     time.Time `db:"now_ts"`
}

// cartOwnerRow は DELETE /api/carts/:id の所有者・状態確認用の行。
type cartOwnerRow struct {
	ID        int64  `db:"id"`
	UserID    int64  `db:"user_id"`
	ProductID int64  `db:"product_id"`
	Status    string `db:"status"`
}

// expiredCartRow は sweep が列挙する期限切れカートの行。
type expiredCartRow struct {
	ID        int64 `db:"id"`
	ProductID int64 `db:"product_id"`
}

type dropTimingRow struct {
	ScheduledAt time.Time `db:"scheduled_at"`
	EndsAt      time.Time `db:"ends_at"`
	NowTs       time.Time `db:"now_ts"`
}

type dropAnnounceRow struct {
	DropID      int64     `db:"drop_id"`
	LiveID      int64     `db:"live_id"`
	ProductID   int64     `db:"product_id"`
	Name        string    `db:"name"`
	Price       uint32    `db:"price"`
	Quantity    uint32    `db:"quantity"`
	ScheduledAt time.Time `db:"scheduled_at"`
}

// dropStartRow は handleSweep が列挙する「開始待ち（scheduled）」ドロップの行。
type dropStartRow struct {
	DropID    int64 `db:"drop_id"`
	LiveID    int64 `db:"live_id"`
	ProductID int64 `db:"product_id"`
}

// handleCreateCart は POST /api/products/:id/cart。
// cart-reservation スペック「カート投入による在庫引き当て」「同一ユーザー同一商品の
// カートは1つまで」「オーバーセルの防止」の実装。
func handleCreateCart(c echo.Context) error {
	db := getDB(c)

	productID, err := strconv.ParseInt(c.Param("id"), 10, 64)
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

	// 1. 商品存在確認
	var prod cartProductRow
	err = tx.Get(&prod, "SELECT id, live_id FROM products WHERE id = ?", productID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// 2. stocks 行ロック獲得（ここからCOMMITまでロックを保持する）
	var quantity uint32
	err = tx.Get(&quantity, "SELECT quantity FROM stocks WHERE product_id = ? FOR UPDATE", productID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if quantity == 0 {
		return c.JSON(http.StatusConflict, map[string]string{"error": "sold out"})
	}

	var dropTiming dropTimingRow
	err = tx.Get(&dropTiming,
		"SELECT scheduled_at, ends_at, NOW(3) AS now_ts FROM drops WHERE product_id = ?",
		productID,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if err == nil {
		// ドロップ商品。期間外は409で拒否し、在庫・カートに変化を与えない。
		if dropTiming.NowTs.Before(dropTiming.ScheduledAt) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "drop not started"})
		}
		if !dropTiming.NowTs.Before(dropTiming.EndsAt) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "drop ended"})
		}
	}
	// 通常商品（drops に行がない）は従来どおりの判定に進む。

	// 3. 二重投入チェック（同一ユーザー・同一商品のactiveなカートが既にあるか）
	var dup int
	err = tx.Get(&dup,
		`SELECT COUNT(*) FROM carts
		 WHERE user_id = ? AND product_id = ? AND status = 'active' AND expires_at > NOW(3)`,
		userID, productID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if dup > 0 {
		return c.JSON(http.StatusConflict, map[string]string{"error": "already in cart"})
	}

	res, err := tx.Exec(
		`INSERT INTO carts (user_id, product_id, live_id, status, expires_at)
		 VALUES (?, ?, ?, 'active', NOW(3) + INTERVAL ? SECOND)`,
		userID, productID, prod.LiveID, cartTTLSeconds,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	cartID, err := res.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// 5. 在庫デクリメント
	if _, err := tx.Exec("UPDATE stocks SET quantity = quantity - 1 WHERE product_id = ?", productID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// 6. COMMIT
	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed = true

	var timing cartTimingRow
	if err := db.Get(&timing, "SELECT expires_at, NOW(3) AS now_ts FROM carts WHERE id = ?", cartID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	remainingMs := timing.ExpiresAt.Sub(timing.NowTs).Milliseconds()
	if remainingMs < 0 {
		remainingMs = 0
	}

	return c.JSON(http.StatusCreated, CartCreateResponse{
		CartID:      cartID,
		ExpiresAt:   timing.ExpiresAt.Format("2006-01-02T15:04:05.000Z07:00"),
		RemainingMs: remainingMs,
	})
}

func handleGetCart(c echo.Context) error {
	db := getDB(c)

	cartID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	var cart cartRow
	err = db.Get(&cart,
		"SELECT id, user_id, product_id, status, expires_at, NOW(3) AS now_ts FROM carts WHERE id = ?",
		cartID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "cart not found"})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if cart.UserID != userID {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "cart not found"})
	}

	remainingMs := cart.ExpiresAt.Sub(cart.NowTs).Milliseconds()
	if remainingMs < 0 {
		remainingMs = 0
	}

	return c.JSON(http.StatusOK, CartStatusResponse{
		CartID:      cart.ID,
		ProductID:   cart.ProductID,
		RemainingMs: remainingMs,
		Status:      cart.Status,
	})
}

// handleDeleteCart は DELETE /api/carts/:id。
// cart-status スペック「カートの破棄と即時解放」の実装。
func handleDeleteCart(c echo.Context) error {
	db := getDB(c)

	cartID, err := strconv.ParseInt(c.Param("id"), 10, 64)
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

	var cart cartOwnerRow
	err = tx.Get(&cart, "SELECT id, user_id, product_id, status FROM carts WHERE id = ? FOR UPDATE", cartID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "cart not found"})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if cart.UserID != userID || cart.Status != "active" {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "cart not found"})
	}

	if _, err := tx.Exec("DELETE FROM carts WHERE id = ?", cartID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	// 即時解放（cart-statusスペック「破棄による在庫解放」）
	if _, err := tx.Exec("UPDATE stocks SET quantity = quantity + 1 WHERE product_id = ?", cart.ProductID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	committed = true

	return c.NoContent(http.StatusNoContent)
}

func handleSweep(c echo.Context) error {
	db := getDB(c)

	var expired []expiredCartRow
	err := db.Select(&expired,
		"SELECT id, product_id FROM carts WHERE status = 'active' AND expires_at <= NOW(3)",
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	swept := 0
	// 雑だけど動く
	for _, cart := range expired {
		tx, err := db.Beginx()
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}

		// 他経路（DELETE /api/carts/:id や別の sweep 実行）と競合していないか
		// status='active' 条件付きDELETEで確認する。
		res, err := tx.Exec("DELETE FROM carts WHERE id = ? AND status = 'active'", cart.ID)
		if err != nil {
			_ = tx.Rollback()
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		affected, err := res.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if affected == 0 {
			// 既に別経路で解放済み。在庫は戻さず次のカートへ。
			_ = tx.Rollback()
			continue
		}

		if _, err := tx.Exec("UPDATE stocks SET quantity = quantity + 1 WHERE product_id = ?", cart.ProductID); err != nil {
			_ = tx.Rollback()
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}

		if err := tx.Commit(); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		swept++
	}

	log.Printf("sweep: released %d expired cart(s)", swept)

	announced, err := sweepDropAnnounce(db)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	started, err := sweepDropStart(db)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	soldOut, err := sweepDropSoldOut(db)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	closed, err := sweepDropClosed(db)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	log.Printf("sweep: drops announced=%d started=%d sold_out=%d closed=%d", announced, started, soldOut, closed)

	return c.JSON(http.StatusOK, map[string]int{
		"swept":          swept,
		"drop_announced": announced,
		"drop_started":   started,
		"drop_sold_out":  soldOut,
		"drop_closed":    closed,
	})
}

func sweepDropAnnounce(db *sqlx.DB) (int, error) {
	var rows []dropAnnounceRow
	err := db.Select(&rows, `
		SELECT d.id AS drop_id, d.live_id, d.product_id, d.scheduled_at,
		       p.name AS name, p.price AS price, s.quantity AS quantity
		FROM drops d
		JOIN products p ON p.id = d.product_id
		JOIN stocks s ON s.product_id = d.product_id
		WHERE d.announced = 0 AND d.scheduled_at <= NOW(3) + INTERVAL 15 SECOND
	`)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, row := range rows {
		if err := func() error {
			tx, err := db.Beginx()
			if err != nil {
				return err
			}
			committed := false
			defer func() {
				if !committed {
					_ = tx.Rollback()
				}
			}()

			res, err := tx.Exec("UPDATE drops SET announced = 1 WHERE id = ? AND announced = 0", row.DropID)
			if err != nil {
				return err
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if affected == 1 {
				payload := map[string]interface{}{
					"drop_id":    row.DropID,
					"product_id": row.ProductID,
					"name":       row.Name,
					"price":      row.Price,
					"quantity":   row.Quantity,
					"starts_at":  row.ScheduledAt.Format("2006-01-02T15:04:05.000Z07:00"),
				}
				if err := notifyBroadcast(tx, row.LiveID, "drop_scheduled", payload); err != nil {
					return err
				}
			}

			if err := tx.Commit(); err != nil {
				return err
			}
			committed = true
			if affected == 1 {
				count++
			}
			return nil
		}(); err != nil {
			return count, err
		}
	}
	return count, nil
}

func sweepDropStart(db *sqlx.DB) (int, error) {
	var rows []dropStartRow
	err := db.Select(&rows, `
		SELECT id AS drop_id, live_id, product_id
		FROM drops
		WHERE status = 'scheduled' AND scheduled_at <= NOW(3)
	`)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, row := range rows {
		if err := func() error {
			tx, err := db.Beginx()
			if err != nil {
				return err
			}
			committed := false
			defer func() {
				if !committed {
					_ = tx.Rollback()
				}
			}()

			res, err := tx.Exec(
				"UPDATE drops SET status = 'active' WHERE id = ? AND status = 'scheduled'",
				row.DropID,
			)
			if err != nil {
				return err
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if affected == 1 {
				payload := map[string]interface{}{
					"drop_id":    row.DropID,
					"product_id": row.ProductID,
				}
				if err := notifyBroadcast(tx, row.LiveID, "drop_started", payload); err != nil {
					return err
				}
			}

			if err := tx.Commit(); err != nil {
				return err
			}
			committed = true
			if affected == 1 {
				count++
			}
			return nil
		}(); err != nil {
			return count, err
		}
	}
	return count, nil
}

func sweepDropSoldOut(db *sqlx.DB) (int, error) {
	res, err := db.Exec(`
		UPDATE drops d
		JOIN stocks s ON s.product_id = d.product_id
		SET d.status = 'sold_out'
		WHERE d.status = 'active' AND s.quantity = 0
	`)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func sweepDropClosed(db *sqlx.DB) (int, error) {
	res, err := db.Exec(`
		UPDATE drops
		SET status = 'closed'
		WHERE status IN ('scheduled', 'active', 'sold_out') AND ends_at <= NOW(3)
	`)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}
