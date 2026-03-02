package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type server struct {
	db                  *sql.DB
	httpClient          *http.Client
	payBaseURL          string
	platformUserID      string
	documentEngineBase  string
	documentEngineToken string
}

type quoteRequest struct {
	AmountBRL    string `json:"amount_brl"`
	Method       string `json:"method"`
	Installments int    `json:"installments"`
	CardBrand    string `json:"card_brand"`
}

type processTransactionRequest struct {
	TransactionID string `json:"transaction_id"`
	MerchantUser  string `json:"merchant_user_id"`
	PayerUser     string `json:"payer_user_id"`
	AmountBRL     string `json:"amount_brl"`
	Method        string `json:"method"`
	Installments  int    `json:"installments"`
	CardBrand     string `json:"card_brand"`
	TerminalID    string `json:"terminal_id"`
	NFCDeviceID   string `json:"nfc_device_id"`
	PaymentRef    string `json:"payment_ref"`
}

func main() {
	port := envOrDefault("PORT", "8092")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@postgres:5432/nexora_pay?sslmode=disable")
	payBase := strings.TrimRight(envOrDefault("NEXORA_PAY_BASE_URL", "http://nexora-pay:8082"), "/")
	platformUser := envOrDefault("NEXORA_PLUG_PLATFORM_USER", "nexora-plug")
	docBase := strings.TrimRight(envOrDefault("DOCUMENT_ENGINE_BASE_URL", "http://document-engine:8094"), "/")
	docToken := strings.TrimSpace(envOrDefault("DOCUMENT_ENGINE_TOKEN", "doc-engine-token"))

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()
	if err := waitForDB(db, 60*time.Second); err != nil {
		log.Fatalf("database unavailable: %v", err)
	}
	if err := initSchema(db); err != nil {
		log.Fatalf("failed to initialize schema: %v", err)
	}

	s := &server{
		db:                  db,
		httpClient:          &http.Client{Timeout: 15 * time.Second},
		payBaseURL:          payBase,
		platformUserID:      platformUser,
		documentEngineBase:  docBase,
		documentEngineToken: docToken,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/mdr/quote", s.handleQuote)
	mux.HandleFunc("/v1/transactions/process", s.handleProcessTransaction)
	mux.HandleFunc("/v1/transactions/", s.handleGetTransaction)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-plug listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db_unavailable", "service": "nexora-plug"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexora-plug", "settlement": "D+0"})
}

func (s *server) handleQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req quoteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	amountCents, err := parseMoneyToCents(req.AmountBRL)
	if err != nil || amountCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount_brl must be greater than zero"})
		return
	}
	method := normalizeMethod(req.Method)
	installments := clampInt(req.Installments, 1, 12)
	mdrPct := calcMDRPct(method, installments, req.CardBrand)
	anticipationPct := 0.50
	mdrCents := pctCents(amountCents, mdrPct)
	anticipationCents := pctCents(amountCents, anticipationPct)
	totalFee := mdrCents + anticipationCents
	merchantNet := amountCents - totalFee
	if merchantNet < 0 {
		merchantNet = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"method":             method,
		"installments":       installments,
		"mdr_pct":            mdrPct,
		"anticipation":       "D+0",
		"anticipation_pct":   anticipationPct,
		"amount":             centsToMoney(amountCents),
		"mdr_fee":            centsToMoney(mdrCents),
		"anticipation_fee":   centsToMoney(anticipationCents),
		"total_fee":          centsToMoney(totalFee),
		"merchant_net":       centsToMoney(merchantNet),
	})
}

func (s *server) handleProcessTransaction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/transactions/process" {
		http.NotFound(w, r)
		return
	}
	var req processTransactionRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.TransactionID = strings.TrimSpace(req.TransactionID)
	req.MerchantUser = normalizeID(req.MerchantUser)
	req.PayerUser = normalizeID(req.PayerUser)
	req.Method = normalizeMethod(req.Method)
	req.CardBrand = normalizeText(req.CardBrand)
	req.TerminalID = strings.TrimSpace(req.TerminalID)
	req.NFCDeviceID = strings.TrimSpace(req.NFCDeviceID)
	req.PaymentRef = strings.TrimSpace(req.PaymentRef)
	if req.TransactionID == "" {
		req.TransactionID = fmt.Sprintf("plug-tx-%d", time.Now().UTC().UnixNano())
	}
	if req.MerchantUser == "" || req.PayerUser == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "merchant_user_id and payer_user_id are required"})
		return
	}
	if req.MerchantUser == req.PayerUser {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "merchant and payer must be different"})
		return
	}
	if req.Method == "nfc" && req.NFCDeviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nfc_device_id is required for NFC transactions"})
		return
	}
	amountCents, err := parseMoneyToCents(req.AmountBRL)
	if err != nil || amountCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount_brl must be greater than zero"})
		return
	}
	installments := clampInt(req.Installments, 1, 12)
	mdrPct := calcMDRPct(req.Method, installments, req.CardBrand)
	anticipationPct := 0.50
	mdrCents := pctCents(amountCents, mdrPct)
	anticipationCents := pctCents(amountCents, anticipationPct)
	feeCents := mdrCents + anticipationCents
	merchantNet := amountCents - feeCents
	if merchantNet < 0 {
		merchantNet = 0
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	if err := s.ensureWallet(ctx, req.PayerUser); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare payer wallet: " + err.Error()})
		return
	}
	if err := s.ensureWallet(ctx, req.MerchantUser); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare merchant wallet: " + err.Error()})
		return
	}
	if err := s.ensureWallet(ctx, s.platformUserID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare platform wallet: " + err.Error()})
		return
	}

	merchantTransfer, err := s.payTransfer(ctx, req.PayerUser, req.MerchantUser, centsToMoney(merchantNet))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "merchant transfer failed: " + err.Error()})
		return
	}
	feeTransfer, err := s.payTransfer(ctx, req.PayerUser, s.platformUserID, centsToMoney(feeCents))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "platform transfer failed: " + err.Error()})
		return
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO plug_transactions (
			transaction_id, merchant_user_id, payer_user_id, method,
			card_brand, installments, amount_cents, mdr_pct,
			mdr_cents, anticipation_pct, anticipation_cents, fee_cents,
			merchant_net_cents, status, settlement_model, settlement_at,
			terminal_id, nfc_device_id, payment_ref,
			transfer_merchant, transfer_platform
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
			$13,'paid','D+0',NOW(),$14,$15,$16,$17::jsonb,$18::jsonb
		)
	`, req.TransactionID, req.MerchantUser, req.PayerUser, req.Method,
		req.CardBrand, installments, amountCents, mdrPct,
		mdrCents, anticipationPct, anticipationCents, feeCents,
		merchantNet, req.TerminalID, req.NFCDeviceID, req.PaymentRef,
		mustJSON(merchantTransfer), mustJSON(feeTransfer),
	)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "transaction_id already exists or failed to save transaction"})
		return
	}

	s.emitPurchaseEvent(ctx, map[string]any{
		"source":         "plug",
		"order_id":       req.TransactionID,
		"buyer_user_id":  req.PayerUser,
		"seller_user_id": req.MerchantUser,
		"currency":       "BRL",
		"gross_cents":    amountCents,
		"fee_cents":      feeCents,
		"net_cents":      merchantNet,
		"description":    fmt.Sprintf("Nexora Plug %s D+0", strings.ToUpper(req.Method)),
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":         "paid",
		"transaction_id": req.TransactionID,
		"method":         req.Method,
		"installments":   installments,
		"settlement":     "D+0",
		"pricing": map[string]any{
			"amount":            centsToMoney(amountCents),
			"mdr_pct":           mdrPct,
			"mdr_fee":           centsToMoney(mdrCents),
			"anticipation_pct":  anticipationPct,
			"anticipation_fee":  centsToMoney(anticipationCents),
			"total_fee":         centsToMoney(feeCents),
			"merchant_net":      centsToMoney(merchantNet),
		},
		"transfers": map[string]any{"merchant": merchantTransfer, "platform": feeTransfer},
	})
}

func (s *server) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] != "transactions" {
		http.NotFound(w, r)
		return
	}
	txID := strings.TrimSpace(parts[2])
	if txID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid transaction id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `
		SELECT transaction_id, merchant_user_id, payer_user_id, method, card_brand,
			installments, amount_cents, mdr_pct, mdr_cents, anticipation_pct,
			anticipation_cents, fee_cents, merchant_net_cents, status,
			settlement_model, settlement_at, created_at
		FROM plug_transactions
		WHERE transaction_id = $1
	`, txID)
	var merchantUser, payerUser, method, cardBrand, status, settlementModel string
	var installments int
	var amountCents, mdrCents, anticipationCents, feeCents, merchantNet int64
	var mdrPct, anticipationPct float64
	var settlementAt, createdAt time.Time
	if err := row.Scan(&txID, &merchantUser, &payerUser, &method, &cardBrand, &installments,
		&amountCents, &mdrPct, &mdrCents, &anticipationPct, &anticipationCents, &feeCents,
		&merchantNet, &status, &settlementModel, &settlementAt, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "transaction not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load transaction"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"transaction_id": txID,
		"merchant_user_id": merchantUser,
		"payer_user_id": payerUser,
		"method": method,
		"card_brand": cardBrand,
		"installments": installments,
		"amount": centsToMoney(amountCents),
		"mdr_pct": mdrPct,
		"mdr_fee": centsToMoney(mdrCents),
		"anticipation_pct": anticipationPct,
		"anticipation_fee": centsToMoney(anticipationCents),
		"total_fee": centsToMoney(feeCents),
		"merchant_net": centsToMoney(merchantNet),
		"status": status,
		"settlement_model": settlementModel,
		"settlement_at": settlementAt,
		"created_at": createdAt,
	})
}

func calcMDRPct(method string, installments int, cardBrand string) float64 {
	base := 2.99
	if method == "nfc" {
		base = 2.49
	}
	if installments > 1 {
		base += 0.35 * float64(installments-1)
	}
	brand := normalizeText(cardBrand)
	switch brand {
	case "amex":
		base += 0.35
	case "elo":
		base += 0.15
	case "hipercard":
		base += 0.20
	}
	if base < 0 {
		base = 0
	}
	return math.Round(base*100) / 100
}

func normalizeMethod(v string) string {
	m := normalizeText(v)
	if m == "nfc" || m == "tap_to_pay" || m == "tap" {
		return "nfc"
	}
	if m == "card" || m == "chip" || m == "swipe" {
		return "card"
	}
	return "card"
}

func (s *server) ensureWallet(ctx context.Context, userID string) error {
	payload := map[string]any{"user_id": userID, "initial_brl": "0.00", "initial_nex": "0.000000"}
	_, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.payBaseURL+"/v1/wallets", payload, nil)
	if err != nil {
		return err
	}
	if status == http.StatusCreated || status == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("wallet create unexpected status %d: %s", status, rawBody)
}

func (s *server) payTransfer(ctx context.Context, fromUser, toUser, amount string) (map[string]any, error) {
	payload := map[string]any{"from_user_id": fromUser, "to_user_id": toUser, "currency": "BRL", "amount": amount}
	respBody, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.payBaseURL+"/v1/wallets/transfer", payload, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("nexora-pay transfer status %d: %s", status, rawBody)
	}
	mapped, _ := respBody.(map[string]any)
	if mapped == nil {
		mapped = map[string]any{"raw": rawBody}
	}
	return mapped, nil
}

func (s *server) emitPurchaseEvent(ctx context.Context, payload map[string]any) {
	if s.documentEngineBase == "" {
		return
	}
	headers := map[string]string{"x-doc-engine-token": s.documentEngineToken}
	_, statusCode, _, err := s.callJSON(ctx, http.MethodPost, s.documentEngineBase+"/v1/events/purchase", payload, headers)
	if err != nil || statusCode < 200 || statusCode > 299 {
		log.Printf("document_engine_emit_failed source=plug error=%v status=%d", err, statusCode)
	}
}

func (s *server) callJSON(ctx context.Context, method, endpoint string, payload any, headers map[string]string) (any, int, string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, resp.StatusCode, "", err
	}
	rawText := string(raw)
	if len(raw) == 0 {
		return nil, resp.StatusCode, rawText, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, resp.StatusCode, rawText, nil
	}
	return decoded, resp.StatusCode, rawText, nil
}

func initSchema(db *sql.DB) error {
	ddl := `
CREATE TABLE IF NOT EXISTS plug_transactions (
	transaction_id TEXT PRIMARY KEY,
	merchant_user_id TEXT NOT NULL,
	payer_user_id TEXT NOT NULL,
	method TEXT NOT NULL,
	card_brand TEXT NOT NULL DEFAULT '',
	installments INT NOT NULL,
	amount_cents BIGINT NOT NULL,
	mdr_pct DOUBLE PRECISION NOT NULL,
	mdr_cents BIGINT NOT NULL,
	anticipation_pct DOUBLE PRECISION NOT NULL,
	anticipation_cents BIGINT NOT NULL,
	fee_cents BIGINT NOT NULL,
	merchant_net_cents BIGINT NOT NULL,
	status TEXT NOT NULL,
	settlement_model TEXT NOT NULL,
	settlement_at TIMESTAMPTZ NOT NULL,
	terminal_id TEXT NOT NULL DEFAULT '',
	nfc_device_id TEXT NOT NULL DEFAULT '',
	payment_ref TEXT NOT NULL DEFAULT '',
	transfer_merchant JSONB NOT NULL DEFAULT '{}'::jsonb,
	transfer_platform JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_plug_transactions_created_at ON plug_transactions(created_at DESC);
CREATE INDEX IF NOT EXISTS ix_plug_transactions_merchant ON plug_transactions(merchant_user_id, created_at DESC);
`
	_, err := db.Exec(ddl)
	return err
}

func waitForDB(db *sql.DB, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := db.PingContext(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func normalizeText(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func normalizeID(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func parseMoneyToCents(raw string) (int64, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
	if raw == "" {
		return 0, errors.New("empty amount")
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, errors.New("negative amount")
	}
	return int64(math.Round(v * 100)), nil
}

func centsToMoney(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

func pctCents(base int64, pct float64) int64 {
	if base <= 0 || pct <= 0 {
		return 0
	}
	return int64(math.Round(float64(base) * pct / 100.0))
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func mustJSON(v any) string {
	encoded, _ := json.Marshal(v)
	return string(encoded)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withServiceHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Service", "nexora-plug")
		next.ServeHTTP(w, r)
	})
}
