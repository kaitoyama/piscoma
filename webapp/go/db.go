package main

import (
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	_ "github.com/go-sql-driver/mysql"
)

const dbContextKey = "db"

// dbMiddleware はリクエストごとの echo.Context に *sqlx.DB を注入する。
// ハンドラは getDB(c) で取り出す。
func dbMiddleware(db *sqlx.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(dbContextKey, db)
			return next(c)
		}
	}
}

// getDB は echo.Context から *sqlx.DB を取り出す。
func getDB(c echo.Context) *sqlx.DB {
	return c.Get(dbContextKey).(*sqlx.DB)
}

// connectDB は環境変数から組み立てた DSN で MySQL に接続する。
// multiStatements=true は POST /api/initialize が seed.sql をまとめて
// 実行するために必要（初期化専用の用途に限定して有効化している）。
func connectDB(c config) (*sqlx.DB, error) {
	dsn := fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?parseTime=true&loc=Local&multiStatements=true&charset=utf8mb4",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName,
	)

	db, err := sqlx.Connect("mysql", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Minute)

	return db, nil
}

// connectDBWithRetry は connectDB を最大 attempts 回・interval 間隔でリトライする。
// MySQL コンテナの initdb（初期データ投入）が完了して TCP 接続を受け付けるまで
// 待つための起動時専用ヘルパー（リクエスト処理経路では使わない）。
func connectDBWithRetry(c config, attempts int, interval time.Duration) (*sqlx.DB, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		db, err := connectDB(c)
		if err == nil {
			return db, nil
		}
		lastErr = err
		time.Sleep(interval)
	}
	return nil, lastErr
}
