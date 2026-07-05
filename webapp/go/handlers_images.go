package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

var pngMagicBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

const iconMaxBytes = 1 << 20

func handleGetProductImage(c echo.Context) error {
	db := getDB(c)

	productID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	var image []byte
	err = db.Get(&image, "SELECT image FROM products WHERE id = ?", productID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.Blob(http.StatusOK, "image/png", image)
}

func handleGetUserIcon(c echo.Context) error {
	db := getDB(c)

	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "id が不正です"})
	}

	var image []byte
	err = db.Get(&image, "SELECT image FROM user_icons WHERE user_id = ?", userID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		// user_icons に行がない: ユーザー自体の存在確認をしてプレースホルダ or 404 を切り分ける
		// （spec「ユーザーアイコンの配信」）。
		var exists int
		if err := db.Get(&exists, "SELECT COUNT(*) FROM users WHERE id = ?", userID); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if exists == 0 {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "user not found"})
		}
		image = placeholderImage
	}

	return c.Blob(http.StatusOK, "image/png", image)
}

type iconUpdateRequest struct {
	Image string `json:"image"`
}

// iconUpdateResponse は POST /api/icon のレスポンス。
type iconUpdateResponse struct {
	UserID int64 `json:"user_id"`
}

func handleUpdateIcon(c echo.Context) error {
	db := getDB(c)

	userID, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	var req iconUpdateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	image, err := base64.StdEncoding.DecodeString(req.Image)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "image は base64 で指定してください"})
	}

	if !bytes.HasPrefix(image, pngMagicBytes) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "image はPNG形式である必要があります"})
	}
	if len(image) > iconMaxBytes {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "image は1MiB以下である必要があります"})
	}

	sum := sha256.Sum256(image)
	imageHash := hex.EncodeToString(sum[:])

	_, err = db.Exec(
		`INSERT INTO user_icons (user_id, image, image_hash)
		 VALUES (?, ?, ?)
		 ON DUPLICATE KEY UPDATE image = VALUES(image), image_hash = VALUES(image_hash)`,
		userID, image, imageHash,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, iconUpdateResponse{UserID: userID})
}
