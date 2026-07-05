package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// parseLiveID は path パラメータ ":id" を配信IDとしてパースする。
func parseLiveID(c echo.Context) (int64, error) {
	return strconv.ParseInt(c.Param("id"), 10, 64)
}

// findLive は指定IDの配信を取得する。存在しなければ (nil, nil) を返す
// （呼び出し側で404を組み立てさせるため error ではなく nil で表現する）。
func findLive(db dbGetter, id int64) (*liveRow, error) {
	var lv liveRow
	err := db.Get(&lv, "SELECT id, seller_id, title, category, pinned_product_id, created_at FROM lives WHERE id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lv, nil
}

// dbGetter は *sqlx.DB のうち本ファイルで使うメソッドだけを切り出したインターフェース
// （テスト容易性のためではなく、findLive のシグネチャを簡潔にするための小さな抽象化）。
type dbGetter interface {
	Get(dest interface{}, query string, args ...interface{}) error
}

// fetchSeller は seller_id からセラーの id / display_name だけを引く。
// email 等は選択しないことで、レスポンスへの個人情報混入を仕組み上防ぐ（§5.4）。
func fetchSeller(db dbGetter, sellerID int64) (Seller, error) {
	var s Seller
	err := db.Get(&s, "SELECT id, display_name FROM users WHERE id = ?", sellerID)
	return s, err
}

func handleFeed(c echo.Context) error {
	db := getDB(c)

	var lives []liveRow
	if err := db.Select(&lives, "SELECT id, seller_id, title, category, pinned_product_id, created_at FROM lives ORDER BY created_at DESC"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// TODO: 本当は視聴者の好みを考慮したい
	result := make([]FeedLive, 0, len(lives))
	for _, lv := range lives {
		seller, err := fetchSeller(db, lv.SellerID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		vc, err := viewerCount(db, lv.ID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		rc, err := reactionCount(db, lv.ID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		products, err := fetchShelf(db, lv.ID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}

		result = append(result, FeedLive{
			ID:       lv.ID,
			Title:    lv.Title,
			Category: lv.Category,
			Seller:   seller,
			Stats:    Stats{ViewerCount: vc, ReactionCount: rc},
			Products: products,
		})
	}

	return c.JSON(http.StatusOK, result)
}

// handleLiveDetail は GET /api/lives/:id。
// live-detail スペック「詳細の取得」「視聴者数の定義」「いいね数は都度集計」の実装。
func handleLiveDetail(c echo.Context) error {
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

	userID, _ := currentUserID(c)
	if err := touchPresence(db, lv.ID, userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	seller, err := fetchSeller(db, lv.SellerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	var pinned *PinnedProduct
	if lv.PinnedProductID != nil {
		var p PinnedProduct
		err := db.Get(&p, "SELECT id, name, price FROM products WHERE id = ?", *lv.PinnedProductID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if err == nil {
			pinned = &p
		}
	}

	vc, err := viewerCount(db, lv.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	rc, err := reactionCount(db, lv.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, LiveDetail{
		ID:            lv.ID,
		Title:         lv.Title,
		Category:      lv.Category,
		Seller:        seller,
		PinnedProduct: pinned,
		Stats:         Stats{ViewerCount: vc, ReactionCount: rc},
	})
}

func handleListComments(c echo.Context) error {
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

	userID, _ := currentUserID(c)
	if err := touchPresence(db, lv.ID, userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	var rows []commentRow
	query := `
		SELECT c.id, c.body, c.reply_to_id, c.created_at,
		       u.id AS user_id, u.display_name AS user_display_name
		FROM comments c
		JOIN users u ON u.id = c.user_id
		WHERE c.live_id = ?
		ORDER BY c.created_at DESC, c.id DESC
	`
	if err := db.Select(&rows, query, liveID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	result := make([]Comment, 0, len(rows))
	for _, r := range rows {
		result = append(result, Comment{
			ID:        r.ID,
			User:      CommentUser{ID: r.UserID, DisplayName: r.UserDisplayName},
			Body:      r.Body,
			ReplyToID: r.ReplyToID,
			CreatedAt: r.CreatedAt,
		})
	}

	return c.JSON(http.StatusOK, result)
}

type postCommentRequest struct {
	Body      string `json:"body"`
	ReplyToID *int64 `json:"reply_to_id"`
}

// handlePostComment は POST /api/lives/:id/comments。
// セラーの返信も同一エンドポイントを使う（§6注記, §3.2）。
// live-comments スペック「コメント投稿と返信」の実装。
func handlePostComment(c echo.Context) error {
	db := getDB(c)

	liveID, err := parseLiveID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	var req postCommentRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if strings.TrimSpace(req.Body) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "body は必須です"})
	}

	lv, err := findLive(db, liveID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if lv == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "live not found"})
	}

	if req.ReplyToID != nil {
		var replyLiveID int64
		err := db.Get(&replyLiveID, "SELECT live_id FROM comments WHERE id = ?", *req.ReplyToID)
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "reply_to_id が存在しません"})
		}
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if replyLiveID != liveID {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "reply_to_id は同一配信のコメントを指す必要があります"})
		}
	}

	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	res, err := db.Exec(
		"INSERT INTO comments (live_id, user_id, body, reply_to_id) VALUES (?, ?, ?, ?)",
		liveID, userID, req.Body, req.ReplyToID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	id, err := res.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	var created commentRow
	err = db.Get(
		&created,
		`SELECT c.id, c.body, c.reply_to_id, c.created_at,
		        u.id AS user_id, u.display_name AS user_display_name
		 FROM comments c
		 JOIN users u ON u.id = c.user_id
		 WHERE c.id = ?`,
		id,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusCreated, Comment{
		ID:        created.ID,
		User:      CommentUser{ID: created.UserID, DisplayName: created.UserDisplayName},
		Body:      created.Body,
		ReplyToID: created.ReplyToID,
		CreatedAt: created.CreatedAt,
	})
}

func handleShelf(c echo.Context) error {
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

	userID, _ := currentUserID(c)
	if err := touchPresence(db, lv.ID, userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	result, err := fetchShelf(db, liveID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, result)
}

func fetchShelf(db *sqlx.DB, liveID int64) ([]ShelfProduct, error) {
	var products []productRow
	if err := db.Select(&products, "SELECT id, name, description, category, price FROM products WHERE live_id = ?", liveID); err != nil {
		return nil, err
	}

	result := make([]ShelfProduct, 0, len(products))
	for _, p := range products {
		var qty uint32
		err := db.Get(&qty, "SELECT quantity FROM stocks WHERE product_id = ?", p.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				qty = 0
			} else {
				return nil, err
			}
		}

		result = append(result, ShelfProduct{
			ID:             p.ID,
			Name:           p.Name,
			Description:    p.Description,
			Category:       p.Category,
			Price:          p.Price,
			RemainingStock: qty,
		})
	}

	return result, nil
}
