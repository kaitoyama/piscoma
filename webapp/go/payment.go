package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

const paymentContextKey = "payment"

// paymentMiddleware はリクエストごとの echo.Context に *PaymentClient を注入する
// （db.go の dbMiddleware と同じ流儀）。ハンドラは getPaymentClient(c) で取り出す。
func paymentMiddleware(pc *PaymentClient) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(paymentContextKey, pc)
			return next(c)
		}
	}
}

// getPaymentClient は echo.Context から *PaymentClient を取り出す。
func getPaymentClient(c echo.Context) *PaymentClient {
	return c.Get(paymentContextKey).(*PaymentClient)
}

const paymentMaxAttempts = 3

type PaymentClient struct {
	baseURL string
	client  *http.Client
}

func newPaymentClient(baseURL string) *PaymentClient {
	return &PaymentClient{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 1 * time.Second},
	}
}

type paymentChargeRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	Amount         int64  `json:"amount"`
}

type paymentChargeResponse struct {
	PaymentID string `json:"payment_id"`
	Status    string `json:"status"`
}

type ErrPaymentFailed struct {
	Attempts int
}

func (e *ErrPaymentFailed) Error() string {
	return fmt.Sprintf("payment failed after %d attempt(s)", e.Attempts)
}

func (pc *PaymentClient) Charge(idempotencyKey string, amount int64) (string, error) {
	reqBody, err := json.Marshal(paymentChargeRequest{
		IdempotencyKey: idempotencyKey,
		Amount:         amount,
	})
	if err != nil {
		return "", err
	}

	var lastErr error
	for attempt := 1; attempt <= paymentMaxAttempts; attempt++ {
		paymentID, retryable, err := pc.tryCharge(reqBody)
		if err == nil {
			return paymentID, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
	}

	if lastErr == nil {
		lastErr = &ErrPaymentFailed{Attempts: paymentMaxAttempts}
	}
	return "", &ErrPaymentFailed{Attempts: paymentMaxAttempts}
}

// tryCharge は1回分の決済呼び出し。retryable=true は503（一時失敗）でリトライ余地がある
// ことを示す。それ以外のエラー（ネットワークエラー・4xx等）はリトライしない。
func (pc *PaymentClient) tryCharge(reqBody []byte) (paymentID string, retryable bool, err error) {
	req, err := http.NewRequest(http.MethodPost, pc.baseURL+"/payments", bytes.NewReader(reqBody))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := pc.client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var body paymentChargeResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return "", false, err
		}
		return body.PaymentID, false, nil
	case http.StatusServiceUnavailable:
		return "", true, fmt.Errorf("payment temporarily unavailable (503)")
	default:
		return "", false, fmt.Errorf("payment gateway returned unexpected status %d", resp.StatusCode)
	}
}

func (pc *PaymentClient) Initialize() error {
	req, err := http.NewRequest(http.MethodPost, pc.baseURL+"/initialize", nil)
	if err != nil {
		return err
	}
	resp, err := pc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("payment-mock initialize returned status %d", resp.StatusCode)
	}
	return nil
}
