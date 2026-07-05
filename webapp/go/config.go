package main

import (
	"os"
	"time"
)

// config はアプリケーションの実行時設定。すべて環境変数から読み込み、
// 未設定時はローカル開発向けのデフォルト値を使う（ハードコード禁止の要件は
// 「環境変数を必ず経由する」という意味であり、デフォルト値自体は許容する）。
type config struct {
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string

	// SQLDir は schema.sql / init/seed.sql を配置したディレクトリ。
	// POST /api/initialize がここから初期データを読み込む。
	SQLDir string

	// SessionSecret は Cookie セッションの署名鍵。
	// 本番相当の運用では必ず環境変数で上書きすること。
	SessionSecret string

	// ListenPort はアプリケーションの待受ポート。
	ListenPort string

	SweepTarget   string
	SweepInterval time.Duration

	PaymentURL string
}

func loadConfig() config {
	sweepInterval, err := time.ParseDuration(getEnv("ISUCOMA_SWEEP_INTERVAL", "5s"))
	if err != nil {
		sweepInterval = 5 * time.Second
	}

	return config{
		DBHost:        getEnv("ISUCOMA_DB_HOST", "127.0.0.1"),
		DBPort:        getEnv("ISUCOMA_DB_PORT", "3306"),
		DBUser:        getEnv("ISUCOMA_DB_USER", "isucoma"),
		DBPassword:    getEnv("ISUCOMA_DB_PASSWORD", "isucoma"),
		DBName:        getEnv("ISUCOMA_DB_NAME", "isucoma"),
		SQLDir:        getEnv("ISUCOMA_SQL_DIR", "/webapp/sql"),
		SessionSecret: getEnv("ISUCOMA_SESSION_SECRET", "isucoma-dev-secret-change-me"),
		ListenPort:    getEnv("ISUCOMA_LISTEN_PORT", "8080"),
		SweepTarget:   getEnv("ISUCOMA_SWEEP_TARGET", "http://webapp:8080/api/internal/sweep"),
		SweepInterval: sweepInterval,
		PaymentURL:    getEnv("ISUCOMA_PAYMENT_URL", "http://127.0.0.1:5000"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
