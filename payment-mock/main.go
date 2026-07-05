package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// paymentRecord は着金記録1件（GET /payments のレスポンス要素そのもの）。
type paymentRecord struct {
	PaymentID      string    `json:"payment_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Amount         int64     `json:"amount"`
	CreatedAt      time.Time `json:"created_at"`
}

type store struct {
	mu      sync.Mutex
	byKey   map[string]*paymentRecord
	ordered []*paymentRecord
	seq     int64
}

func newStore() *store {
	return &store{byKey: make(map[string]*paymentRecord)}
}

// lookup は既存の idempotency_key の記録を返す（あれば ok=true）。
func (s *store) lookup(key string) (*paymentRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byKey[key]
	return r, ok
}

// record は新しい着金記録を1件追加する。呼び出し側は事前に lookup で
// 未記録であることを確認していること（TOCTOU を避けるため、実際の追加は
// handlePayments 内で mu を握ったまま行う compareAndRecord を使う）。
func (s *store) compareAndRecord(key string, amount int64) *paymentRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.byKey[key]; ok {
		return r
	}

	s.seq++
	r := &paymentRecord{
		PaymentID:      fmt.Sprintf("pay_%d", s.seq),
		IdempotencyKey: key,
		Amount:         amount,
		CreatedAt:      time.Now(),
	}
	s.byKey[key] = r
	s.ordered = append(s.ordered, r)
	return r
}

// reset は POST /initialize による全消去。
func (s *store) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byKey = make(map[string]*paymentRecord)
	s.ordered = nil
	s.seq = 0
}

// snapshot は GET /payments 用に現在の全記録と合計金額を返す。
func (s *store) snapshot() ([]*paymentRecord, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]*paymentRecord, len(s.ordered))
	copy(records, s.ordered)
	var total int64
	for _, r := range records {
		total += r.Amount
	}
	return records, total
}

type mockConfig struct {
	Port        string
	LatencyMs   int
	FailureRate float64
}

func loadMockConfig() mockConfig {
	cfg := mockConfig{
		Port:        getEnv("PORT", "5000"),
		LatencyMs:   30,
		FailureRate: 0.01,
	}
	if v := os.Getenv("ISUCOMA_PAYMENT_LATENCY_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.LatencyMs = n
		}
	}
	if v := os.Getenv("ISUCOMA_PAYMENT_FAILURE_RATE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.FailureRate = f
		}
	}
	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type paymentRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	Amount         int64  `json:"amount"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// handlePayments は POST /payments。
// payment-mock スペック「決済受付（冪等）」「レイテンシと一時失敗の特性」の実装。
func handlePayments(s *store, cfg mockConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(cfg.LatencyMs) * time.Millisecond)

		var req paymentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
			return
		}
		if req.IdempotencyKey == "" || req.Amount <= 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "idempotency_key and positive amount are required"})
			return
		}

		// 同一キーの再送は記録済みの結果をそのまま返す（着金は増えない）。
		if existing, ok := s.lookup(req.IdempotencyKey); ok {
			writeJSON(w, http.StatusOK, map[string]string{
				"payment_id": existing.PaymentID,
				"status":     "succeeded",
			})
			return
		}

		if rand.Float64() < cfg.FailureRate {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "payment temporarily unavailable"})
			return
		}

		rec := s.compareAndRecord(req.IdempotencyKey, req.Amount)
		writeJSON(w, http.StatusOK, map[string]string{
			"payment_id": rec.PaymentID,
			"status":     "succeeded",
		})
	}
}

// handleListPayments は GET /payments。
// payment-mock スペック「着金記録の照会」の実装（§5.4: ベンチの事後突合に使う）。
func handleListPayments(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		records, total := s.snapshot()
		payments := make([]paymentRecord, 0, len(records))
		for _, rec := range records {
			payments = append(payments, *rec)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"payments":     payments,
			"count":        len(payments),
			"total_amount": total,
		})
	}
}

func handleInitialize(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.reset()
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// runHealthcheck は `payment-mock healthcheck` として自分自身の GET /healthz を叩く。
// webapp/go/main.go の healthcheck サブコマンドの流儀を踏襲し、Docker の HEALTHCHECK から
// curl/wget をイメージに追加せずに使えるようにする。
func runHealthcheck(port string) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/healthz", port))
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

func main() {
	cfg := loadMockConfig()

	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		runHealthcheck(cfg.Port)
		return
	}

	s := newStore()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /payments", handlePayments(s, cfg))
	mux.HandleFunc("GET /payments", handleListPayments(s))
	mux.HandleFunc("POST /initialize", handleInitialize(s))
	mux.HandleFunc("GET /healthz", handleHealthz)

	log.Printf("starting payment-mock on :%s (latency=%dms failure_rate=%v)", cfg.Port, cfg.LatencyMs, cfg.FailureRate)
	log.Fatal(http.ListenAndServe(":"+cfg.Port, mux))
}
