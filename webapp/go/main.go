package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// runHealthcheck は `isucoma-webapp healthcheck` として自分自身の
// GET /api/healthz を叩く。Docker の HEALTHCHECK から使う。
// 実行環境（distroless/slim イメージ）に curl や wget を追加でインストールせずに
// 済ませるための小さな自己完結ヘルスチェック。
func runHealthcheck(port string) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/api/healthz", port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck failed: status", resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}

func runSweepLoop(cfg config) {
	client := http.Client{Timeout: cfg.SweepInterval}
	log.Printf("sweep-loop starting: target=%s interval=%s", cfg.SweepTarget, cfg.SweepInterval)

	ticker := time.NewTicker(cfg.SweepInterval)
	defer ticker.Stop()

	for {
		sweepOnce(&client, cfg.SweepTarget)
		<-ticker.C
	}
}

// sweepOnce は sweep エンドポイントへの1回分のリクエストを送る。
// エラー時もプロセス自体は継続する（次のtickで再試行すればよいため）。
func sweepOnce(client *http.Client, target string) {
	resp, err := client.Post(target, "application/json", nil)
	if err != nil {
		log.Printf("sweep-loop: request failed: %v", err)
		return
	}
	defer resp.Body.Close()
	log.Printf("sweep-loop: request completed status=%d", resp.StatusCode)
}

func main() {
	cfg := loadConfig()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			runHealthcheck(cfg.ListenPort)
			return
		case "sweep-loop":
			runSweepLoop(cfg)
			return
		}
	}

	// MySQL コンテナは initdb（初期データ投入）中、一時サーバーがソケットのみで
	// 応答するため healthcheck が先に healthy になることがある。初期データが
	// 大きいほどこの窓が広がるので、起動時のDB接続は一定時間リトライする。
	db, err := connectDBWithRetry(cfg, 60, time.Second)
	if err != nil {
		log.Fatalf("failed to connect db: %v", err)
	}
	defer db.Close()

	paymentClient := newPaymentClient(cfg.PaymentURL)

	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(dbMiddleware(db))
	e.Use(paymentMiddleware(paymentClient))

	registerRoutes(e, cfg)

	log.Printf("starting isucoma webapp on :%s", cfg.ListenPort)
	e.Logger.Fatal(e.Start(":" + cfg.ListenPort))
}
