package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

const defaultCampaignLevel = 1

const bcryptCost = 12

// initTables は POST /api/initialize が TRUNCATE する対象テーブル。
// 依存関係はないが（外部キー制約なし）、読みやすさのため子テーブルから並べている。
var initTables = []string{
	"sessions",
	"notifications",
	"coupon_claims",
	"coupons",
	"drops",
	"reactions",
	"live_viewers",
	"comments",
	"orders",
	"carts",
	"stocks",
	"products",
	"lives",
	"user_icons",
	"users",
}

// handleInitialize は POST /api/initialize。
// 全テーブルを TRUNCATE したのち、ISUCOMA_SQL_DIR 配下の初期データ SQL を
// 流し込んで初期状態に戻す（initialize スペック「正常な初期化」「再実行の冪等性」）。
func handleInitialize(cfg config) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		db := getDB(c)

		if _, err := db.Exec("SET FOREIGN_KEY_CHECKS=0"); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		for _, t := range initTables {
			if _, err := db.Exec("TRUNCATE TABLE " + t); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
		}
		if _, err := db.Exec("SET FOREIGN_KEY_CHECKS=1"); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}

		seedPath := filepath.Join(cfg.SQLDir, "init", "seed.sql")
		seedSQL, err := os.ReadFile(seedPath)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "seed file not found: "+err.Error())
		}
		if len(seedSQL) > 0 {
			if _, err := db.Exec(string(seedSQL)); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "seed load failed: "+err.Error())
			}
		}

		if err := getPaymentClient(c).Initialize(); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "payment-mock initialize failed: "+err.Error())
		}

		campaign := defaultCampaignLevel
		var campaignLevelStr string
		if err := db.Get(&campaignLevelStr, "SELECT value FROM settings WHERE name = 'campaign_level'"); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
		} else if n, err := strconv.Atoi(campaignLevelStr); err == nil {
			campaign = n
		}

		// §5.4 の30秒制限に対する余裕を手元で計測できるようログに残す。
		log.Printf("initialize completed in %s", time.Since(start))

		return c.JSON(http.StatusOK, map[string]interface{}{"lang": "go", "campaign": campaign})
	}
}

type registerRequest struct {
	DisplayName        string `json:"display_name"`
	Email              string `json:"email"`
	Password           string `json:"password"`
	PreferenceCategory string `json:"preference_category"`
}

// handleRegister は POST /api/register。視聴者アカウントを作成し、
// そのままログイン済みセッションを発行する（auth スペック「正常な登録」）。
func handleRegister(c echo.Context) error {
	var req registerRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if req.DisplayName == "" || req.Email == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "display_name, email, password は必須です"})
	}
	if !validCategories[req.PreferenceCategory] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "preference_category が不正です"})
	}

	db := getDB(c)

	var existing int
	if err := db.Get(&existing, "SELECT COUNT(*) FROM users WHERE email = ?", req.Email); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if existing > 0 {
		return c.JSON(http.StatusConflict, map[string]string{"error": "email はすでに登録されています"})
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	res, err := db.Exec(
		"INSERT INTO users (email, password_hash, display_name, role, preference_category) VALUES (?, ?, ?, ?, ?)",
		req.Email, string(hash), req.DisplayName, RoleViewer, req.PreferenceCategory,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	id, err := res.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	u := User{ID: id, Email: req.Email, DisplayName: req.DisplayName, Role: RoleViewer, PreferenceCategory: &req.PreferenceCategory}
	if err := establishSession(c, u); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusCreated, u)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin は POST /api/login。視聴者/セラー/Admin 共用ログイン
// （auth スペック「共用ログイン」）。
func handleLogin(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if req.Email == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "email, password は必須です"})
	}

	db := getDB(c)

	var u User
	err := db.Get(&u, "SELECT id, email, password_hash, display_name, role, preference_category, created_at FROM users WHERE email = ?", req.Email)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "email または password が違います"})
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "email または password が違います"})
	}

	if err := establishSession(c, u); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, u)
}
