package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// requireAuth はセッションに user_id が無いリクエストを 401 で拒否するミドルウェア。
// auth スペック「セッションによる保護」の実装。
func requireAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if _, ok := currentUserID(c); !ok {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
		return next(c)
	}
}

// requireRole は指定ロール以外のセッションを 403 で拒否するミドルウェア
// （/api/seller/* は seller、/api/admin/* は admin 専用）。
// requireAuth の後段に置く前提（未認証は 401 のまま先に弾かれる）。
func requireRole(role string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			got, ok := currentUserRole(c)
			if !ok {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			}
			if got != role {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
			}
			return next(c)
		}
	}
}
