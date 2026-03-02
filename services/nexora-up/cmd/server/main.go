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
	db         *sql.DB
	httpClient *http.Client
	payBaseURL string
	platformBy map[string]string
}

type registerReferralRequest struct {
	ReferrerUserID string `json:"referrer_user_id"`
	ReferredUserID string `json:"referred_user_id"`
	Source         string `json:"source"`
	CampaignID     string `json:"campaign_id"`
}

type processCommissionRequest struct {
	Source         string `json:"source"`
	OrderID        string `json:"order_id"`
	BuyerUserID    string `json:"buyer_user_id"`
	MarginCents    int64  `json:"margin_cents"`
	PlatformUserID string `json:"platform_user_id"`
	Currency       string `json:"currency"`
}

func main() {
	port := envOrDefault("PORT", "8093")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@postgres:5432/nexora_pay?sslmode=disable")
	payBase := strings.TrimRight(envOrDefault("NEXORA_PAY_BASE_URL", "http://nexora-pay:8082"), "/")

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
		db:         db,
		httpClient: &http.Client{Timeout: 12 * time.Second},
		payBaseURL: payBase,
		platformBy: map[string]string{
			"stock": envOrDefault("NEXORA_UP_SOURCE_STOCK_USER", "nexora-stock"),
			"place": envOrDefault("NEXORA_UP_SOURCE_PLACE_USER", "nexora-place"),
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/referrals/register", s.handleRegisterReferral)
	mux.HandleFunc("/v1/referrals/", s.handleGetReferral)
	mux.HandleFunc("/v1/commissions/process", s.handleProcessCommission)
	mux.HandleFunc("/v1/commissions", s.handleListCommissions)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-up listening on :%s", port)
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db_unavailable", "service": "nexora-up"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexora-up", "commission_pct": 5})
}

func (s *server) handleRegisterReferral(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/referrals/register" {
		http.NotFound(w, r)
		return
	}
	var req registerReferralRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.ReferrerUserID = normalizeID(req.ReferrerUserID)
	req.ReferredUserID = normalizeID(req.ReferredUserID)
	req.Source = normalizeText(req.Source)
	req.CampaignID = strings.TrimSpace(req.CampaignID)
	if req.ReferrerUserID == "" || req.ReferredUserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "referrer_user_id and referred_user_id are required"})
		return
	}
	if req.ReferrerUserID == req.ReferredUserID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "referrer and referred must be different"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO up_referrals (referred_user_id, referrer_user_id, source, campaign_id)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (referred_user_id) DO UPDATE SET
			referrer_user_id = EXCLUDED.referrer_user_id,
			source = EXCLUDED.source,
			campaign_id = EXCLUDED.campaign_id,
			updated_at = NOW()
	`, req.ReferredUserID, req.ReferrerUserID, req.Source, req.CampaignID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to upsert referral"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "registered", "referral": req})
}

func (s *server) handleGetReferral(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] != "referrals" {
		http.NotFound(w, r)
		return
	}
	referred := normalizeID(parts[2])
	if referred == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid referred user"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `
		SELECT referred_user_id, referrer_user_id, source, campaign_id, created_at
		FROM up_referrals
		WHERE referred_user_id = $1
	`, referred)
	var referredUser, referrerUser, source, campaign string
	var createdAt time.Time
	if err := row.Scan(&referredUser, &referrerUser, &source, &campaign, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "referral not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load referral"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"referred_user_id": referredUser,
		"referrer_user_id": referrerUser,
		"source":          source,
		"campaign_id":     campaign,
		"created_at":      createdAt,
	})
}

func (s *server) handleProcessCommission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/commissions/process" {
		http.NotFound(w, r)
		return
	}
	var req processCommissionRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.Source = normalizeText(req.Source)
	req.OrderID = strings.TrimSpace(req.OrderID)
	req.BuyerUserID = normalizeID(req.BuyerUserID)
	req.PlatformUserID = normalizeID(req.PlatformUserID)
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	if req.Currency == "" {
		req.Currency = "BRL"
	}
	if req.Currency != "BRL" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only BRL commissions are supported"})
		return
	}
	if req.Source == "" || req.OrderID == "" || req.BuyerUserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source, order_id and buyer_user_id are required"})
		return
	}
	if req.MarginCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "margin_cents must be greater than zero"})
		return
	}
	if req.PlatformUserID == "" {
		req.PlatformUserID = s.platformBy[req.Source]
	}
	if req.PlatformUserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "platform_user_id is required for source"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	row := s.db.QueryRowContext(ctx, `
		SELECT id, referrer_user_id, commission_cents, status
		FROM up_commissions
		WHERE source = $1 AND order_id = $2
	`, req.Source, req.OrderID)
	var existingID int64
	var existingReferrer, existingStatus string
	var existingCommission int64
	if err := row.Scan(&existingID, &existingReferrer, &existingCommission, &existingStatus); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "already_processed",
			"commission_id":    existingID,
			"referrer_user_id": existingReferrer,
			"commission":       centsToMoney(existingCommission),
			"state":            existingStatus,
		})
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check idempotency"})
		return
	}

	var referrerUserID string
	err := s.db.QueryRowContext(ctx, `
		SELECT referrer_user_id
		FROM up_referrals
		WHERE referred_user_id = $1
	`, req.BuyerUserID).Scan(&referrerUserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{
				"status":        "skipped",
				"reason":        "no_referral",
				"buyer_user_id": req.BuyerUserID,
				"order_id":      req.OrderID,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load referral"})
		return
	}

	commissionCents := pctCents(req.MarginCents, 5.0)
	if commissionCents <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "skipped",
			"reason": "commission_rounding_zero",
		})
		return
	}

	if err := s.ensureWallet(ctx, req.PlatformUserID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare platform wallet: " + err.Error()})
		return
	}
	if err := s.ensureWallet(ctx, referrerUserID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare referrer wallet: " + err.Error()})
		return
	}

	transfer, err := s.payTransfer(ctx, req.PlatformUserID, referrerUserID, centsToMoney(commissionCents))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to transfer commission: " + err.Error()})
		return
	}

	var commissionID int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO up_commissions (
			source, order_id, buyer_user_id, referrer_user_id,
			margin_cents, commission_cents, platform_user_id, currency, status, transfer
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,'paid',$9::jsonb
		)
		RETURNING id
	`, req.Source, req.OrderID, req.BuyerUserID, referrerUserID,
		req.MarginCents, commissionCents, req.PlatformUserID, req.Currency,
		mustJSON(transfer),
	).Scan(&commissionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save commission"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "paid",
		"commission_id":    commissionID,
		"source":           req.Source,
		"order_id":         req.OrderID,
		"buyer_user_id":    req.BuyerUserID,
		"referrer_user_id": referrerUserID,
		"margin":           centsToMoney(req.MarginCents),
		"commission":       centsToMoney(commissionCents),
		"percentage":       5,
		"transfer":         transfer,
	})
}

func (s *server) handleListCommissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	source := normalizeText(r.URL.Query().Get("source"))
	referrer := normalizeID(r.URL.Query().Get("referrer_user_id"))
	limit := clampInt(intOrDefault(r.URL.Query().Get("limit"), 100), 1, 500)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source, order_id, buyer_user_id, referrer_user_id, margin_cents, commission_cents, status, created_at
		FROM up_commissions
		WHERE ($1 = '' OR source = $1)
		  AND ($2 = '' OR referrer_user_id = $2)
		ORDER BY created_at DESC
		LIMIT $3
	`, source, referrer, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list commissions"})
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var sourceDB, orderID, buyerID, referrerID, status string
		var marginCents, commissionCents int64
		var createdAt time.Time
		if err := rows.Scan(&id, &sourceDB, &orderID, &buyerID, &referrerID, &marginCents, &commissionCents, &status, &createdAt); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan commissions"})
			return
		}
		items = append(items, map[string]any{
			"id":               id,
			"source":           sourceDB,
			"order_id":         orderID,
			"buyer_user_id":    buyerID,
			"referrer_user_id": referrerID,
			"margin":           centsToMoney(marginCents),
			"commission":       centsToMoney(commissionCents),
			"status":           status,
			"created_at":       createdAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) ensureWallet(ctx context.Context, userID string) error {
	payload := map[string]any{"user_id": userID, "initial_brl": "0.00", "initial_nex": "0.000000"}
	_, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.payBaseURL+"/v1/wallets", payload)
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
	respBody, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.payBaseURL+"/v1/wallets/transfer", payload)
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

func (s *server) callJSON(ctx context.Context, method, endpoint string, payload any) (any, int, string, error) {
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
CREATE TABLE IF NOT EXISTS up_referrals (
	referred_user_id TEXT PRIMARY KEY,
	referrer_user_id TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	campaign_id TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS up_commissions (
	id BIGSERIAL PRIMARY KEY,
	source TEXT NOT NULL,
	order_id TEXT NOT NULL,
	buyer_user_id TEXT NOT NULL,
	referrer_user_id TEXT NOT NULL,
	margin_cents BIGINT NOT NULL,
	commission_cents BIGINT NOT NULL,
	platform_user_id TEXT NOT NULL,
	currency TEXT NOT NULL,
	status TEXT NOT NULL,
	transfer JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_up_commission_source_order ON up_commissions(source, order_id);
CREATE INDEX IF NOT EXISTS ix_up_commissions_referrer_created ON up_commissions(referrer_user_id, created_at DESC);
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

func intOrDefault(raw string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
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
		w.Header().Set("X-Service", "nexora-up")
		next.ServeHTTP(w, r)
	})
}
