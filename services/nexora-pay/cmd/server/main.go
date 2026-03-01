package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

const (
	brlScale int64 = 100
	nexScale int64 = 1000000
)

type server struct {
	db *sql.DB
}

type createWalletRequest struct {
	UserID     string `json:"user_id"`
	InitialBRL string `json:"initial_brl"`
	InitialNEX string `json:"initial_nex"`
}

type transferRequest struct {
	FromUserID string `json:"from_user_id"`
	ToUserID   string `json:"to_user_id"`
	Currency   string `json:"currency"`
	Amount     string `json:"amount"`
}

type pixRequest struct {
	FromUserID string `json:"from_user_id"`
	PixKey     string `json:"pix_key"`
	AmountBRL  string `json:"amount_brl"`
}

type wallet struct {
	UserID   string
	BRLCents int64
	NEXUnits int64
}

func main() {
	port := envOrDefault("PORT", "8082")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@localhost:5432/nexora_pay?sslmode=disable")

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

	s := &server{db: db}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/wallets", s.handleWallets)
	mux.HandleFunc("/v1/wallets/transfer", s.handleTransfer)
	mux.HandleFunc("/v1/pix/send", s.handlePixSend)
	mux.HandleFunc("/v1/wallets/", s.handleWalletSubroutes)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-pay listening on :%s", port)
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "nexora-pay"})
}

func (s *server) handleWallets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/wallets" {
		http.NotFound(w, r)
		return
	}

	var req createWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	brlCents := int64(0)
	nexUnits := int64(0)
	var err error
	if strings.TrimSpace(req.InitialBRL) != "" {
		brlCents, err = parseScaled(req.InitialBRL, brlScale)
		if err != nil || brlCents < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid initial_brl"})
			return
		}
	}
	if strings.TrimSpace(req.InitialNEX) != "" {
		nexUnits, err = parseScaled(req.InitialNEX, nexScale)
		if err != nil || nexUnits < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid initial_nex"})
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO wallets (user_id, brl_cents, nex_units) VALUES ($1, $2, $3) ON CONFLICT (user_id) DO NOTHING`,
		req.UserID, brlCents, nexUnits,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create wallet"})
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "wallet already exists"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id":      req.UserID,
		"brl_balance":  formatScaled(brlCents, brlScale, 2),
		"nex_balance":  formatScaled(nexUnits, nexScale, 6),
		"currency_brl": "BRL",
		"currency_nex": "NEX",
	})
}

func (s *server) handleWalletSubroutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "wallets" || parts[3] != "balance" {
		http.NotFound(w, r)
		return
	}
	userID := strings.TrimSpace(parts[2])
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}

	walletData, err := s.getWallet(r.Context(), nil, userID, false)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "wallet not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load wallet"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":      walletData.UserID,
		"brl_balance":  formatScaled(walletData.BRLCents, brlScale, 2),
		"nex_balance":  formatScaled(walletData.NEXUnits, nexScale, 6),
		"currency_brl": "BRL",
		"currency_nex": "NEX",
	})
}

func (s *server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/wallets/transfer" {
		http.NotFound(w, r)
		return
	}

	var req transferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	req.FromUserID = strings.TrimSpace(req.FromUserID)
	req.ToUserID = strings.TrimSpace(req.ToUserID)
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	if req.FromUserID == "" || req.ToUserID == "" || req.Amount == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from_user_id, to_user_id and amount are required"})
		return
	}
	if req.FromUserID == req.ToUserID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source and destination must be different"})
		return
	}
	if req.Currency != "BRL" && req.Currency != "NEX" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "currency must be BRL or NEX"})
		return
	}

	amountRaw, err := parseScaled(req.Amount, scaleByCurrency(req.Currency))
	if err != nil || amountRaw <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid amount"})
		return
	}

	tx, err := s.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start transaction"})
		return
	}
	defer tx.Rollback()

	ordered := []string{req.FromUserID, req.ToUserID}
	sort.Strings(ordered)
	w1, err := s.getWallet(r.Context(), tx, ordered[0], true)
	if err != nil {
		handleWalletLookupError(w, err, ordered[0])
		return
	}
	w2, err := s.getWallet(r.Context(), tx, ordered[1], true)
	if err != nil {
		handleWalletLookupError(w, err, ordered[1])
		return
	}

	byUser := map[string]wallet{
		w1.UserID: w1,
		w2.UserID: w2,
	}
	source := byUser[req.FromUserID]
	target := byUser[req.ToUserID]

	switch req.Currency {
	case "BRL":
		if source.BRLCents < amountRaw {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "insufficient BRL balance"})
			return
		}
		source.BRLCents -= amountRaw
		target.BRLCents += amountRaw
	case "NEX":
		if source.NEXUnits < amountRaw {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "insufficient NEX balance"})
			return
		}
		source.NEXUnits -= amountRaw
		target.NEXUnits += amountRaw
	}

	if err := s.updateWallet(r.Context(), tx, source); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update source wallet"})
		return
	}
	if err := s.updateWallet(r.Context(), tx, target); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update target wallet"})
		return
	}

	var txID int64
	err = tx.QueryRowContext(
		r.Context(),
		`INSERT INTO wallet_transactions (tx_type, from_user_id, to_user_id, currency, amount_raw, status)
		 VALUES ('TRANSFER', $1, $2, $3, $4, 'COMPLETED')
		 RETURNING id`,
		req.FromUserID, req.ToUserID, req.Currency, amountRaw,
	).Scan(&txID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to record transfer"})
		return
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to commit transfer"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"transaction_id": txID,
		"type":           "TRANSFER",
		"currency":       req.Currency,
		"amount":         formatByCurrency(amountRaw, req.Currency),
		"from_user_id":   req.FromUserID,
		"to_user_id":     req.ToUserID,
		"status":         "COMPLETED",
	})
}

func (s *server) handlePixSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/pix/send" {
		http.NotFound(w, r)
		return
	}

	var req pixRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.FromUserID = strings.TrimSpace(req.FromUserID)
	req.PixKey = strings.TrimSpace(req.PixKey)
	if req.FromUserID == "" || req.PixKey == "" || strings.TrimSpace(req.AmountBRL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from_user_id, pix_key and amount_brl are required"})
		return
	}

	amountCents, err := parseScaled(req.AmountBRL, brlScale)
	if err != nil || amountCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid amount_brl"})
		return
	}

	tx, err := s.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start transaction"})
		return
	}
	defer tx.Rollback()

	source, err := s.getWallet(r.Context(), tx, req.FromUserID, true)
	if err != nil {
		handleWalletLookupError(w, err, req.FromUserID)
		return
	}
	if source.BRLCents < amountCents {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "insufficient BRL balance"})
		return
	}

	source.BRLCents -= amountCents
	if err := s.updateWallet(r.Context(), tx, source); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update wallet"})
		return
	}

	var txID int64
	err = tx.QueryRowContext(
		r.Context(),
		`INSERT INTO wallet_transactions (tx_type, from_user_id, pix_key, currency, amount_raw, status)
		 VALUES ('PIX', $1, $2, 'BRL', $3, 'SENT_STUB')
		 RETURNING id`,
		req.FromUserID, req.PixKey, amountCents,
	).Scan(&txID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to record pix transaction"})
		return
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to commit pix transaction"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"transaction_id": txID,
		"type":           "PIX",
		"currency":       "BRL",
		"amount":         formatScaled(amountCents, brlScale, 2),
		"from_user_id":   req.FromUserID,
		"pix_key":        req.PixKey,
		"status":         "SENT_STUB",
	})
}

func (s *server) getWallet(ctx context.Context, tx *sql.Tx, userID string, forUpdate bool) (wallet, error) {
	query := "SELECT user_id, brl_cents, nex_units FROM wallets WHERE user_id = $1"
	if forUpdate {
		query += " FOR UPDATE"
	}

	var w wallet
	var err error
	if tx != nil {
		err = tx.QueryRowContext(ctx, query, userID).Scan(&w.UserID, &w.BRLCents, &w.NEXUnits)
	} else {
		err = s.db.QueryRowContext(ctx, query, userID).Scan(&w.UserID, &w.BRLCents, &w.NEXUnits)
	}
	return w, err
}

func (s *server) updateWallet(ctx context.Context, tx *sql.Tx, w wallet) error {
	if tx == nil {
		return errors.New("transaction required")
	}
	_, err := tx.ExecContext(
		ctx,
		`UPDATE wallets
		 SET brl_cents = $2, nex_units = $3, updated_at = NOW()
		 WHERE user_id = $1`,
		w.UserID, w.BRLCents, w.NEXUnits,
	)
	return err
}

func handleWalletLookupError(w http.ResponseWriter, err error, userID string) {
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("wallet not found for %s", userID)})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load wallet"})
}

func initSchema(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS wallets (
  user_id TEXT PRIMARY KEY,
  brl_cents BIGINT NOT NULL DEFAULT 0,
  nex_units BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS wallet_transactions (
  id BIGSERIAL PRIMARY KEY,
  tx_type TEXT NOT NULL,
  from_user_id TEXT,
  to_user_id TEXT,
  pix_key TEXT,
  currency TEXT NOT NULL,
  amount_raw BIGINT NOT NULL CHECK (amount_raw > 0),
  status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`
	_, err := db.Exec(schema)
	return err
}

func parseScaled(value string, scale int64) (int64, error) {
	value = strings.TrimSpace(value)
	r := new(big.Rat)
	if _, ok := r.SetString(value); !ok {
		return 0, errors.New("invalid decimal")
	}
	if r.Sign() < 0 {
		return 0, errors.New("negative value")
	}

	scaled := new(big.Rat).Mul(r, big.NewRat(scale, 1))
	if !scaled.IsInt() {
		return 0, errors.New("too many decimal places")
	}

	i := new(big.Int).Set(scaled.Num())
	if !i.IsInt64() {
		return 0, errors.New("value out of range")
	}
	return i.Int64(), nil
}

func formatScaled(value int64, scale int64, decimals int) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	whole := value / scale
	frac := value % scale
	return fmt.Sprintf("%s%d.%0*d", sign, whole, decimals, frac)
}

func scaleByCurrency(currency string) int64 {
	if currency == "NEX" {
		return nexScale
	}
	return brlScale
}

func formatByCurrency(amount int64, currency string) string {
	if currency == "NEX" {
		return formatScaled(amount, nexScale, 6)
	}
	return formatScaled(amount, brlScale, 2)
}

func waitForDB(db *sql.DB, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		err := db.PingContext(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withServiceHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Service", "nexora-pay")
		next.ServeHTTP(w, r)
	})
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envAsInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
