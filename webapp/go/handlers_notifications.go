package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	notificationRetryAfterMs = 500
	notificationJitterMs     = 0
)

// notificationRow は notifications テーブルの行のうちレスポンスに必要な列
// （DB スキャン専用の内部表現。payload は JSON 型カラムを []byte のまま受け取り、
// レスポンス組み立て時に json.RawMessage としてそのまま埋め込む＝文字列化しない）。
type notificationRow struct {
	ID        int64     `db:"id"`
	Type      string    `db:"type"`
	Payload   []byte    `db:"payload"`
	CreatedAt time.Time `db:"created_at"`
}

// NotificationItem は GET /api/lives/:id/notifications のレスポンス要素。
// Payload は json.RawMessage としてそのまま埋め込み、JSON オブジェクトとして返す
// （notifications スペック「通知の取得（since_id カーソル）」: 文字列のまま返さない）。
type NotificationItem struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type NotificationsResponse struct {
	Notifications []NotificationItem `json:"notifications"`
	RetryAfterMs  int                `json:"retry_after_ms"`
	JitterMs      int                `json:"jitter_ms"`
}

func handleListNotifications(c echo.Context) error {
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

	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	var sinceID int64
	if s := c.QueryParam("since_id"); s != "" {
		sinceID, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "since_id が不正です"})
		}
	}

	var rows []notificationRow
	query := `
		SELECT id, type, payload, created_at
		FROM notifications
		WHERE live_id = ? AND (user_id IS NULL OR user_id = ?) AND id > ?
		ORDER BY id
	`
	if err := db.Select(&rows, query, liveID, userID, sinceID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	result := make([]NotificationItem, 0, len(rows))
	for _, r := range rows {
		result = append(result, NotificationItem{
			ID:        r.ID,
			Type:      r.Type,
			Payload:   json.RawMessage(r.Payload),
			CreatedAt: r.CreatedAt,
		})
	}

	return c.JSON(http.StatusOK, NotificationsResponse{
		Notifications: result,
		RetryAfterMs:  notificationRetryAfterMs,
		JitterMs:      notificationJitterMs,
	})
}
