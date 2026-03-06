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

const moveFeePct = 10.0

type server struct {
	db             *sql.DB
	httpClient     *http.Client
	payBaseURL     string
	documentEngineBase  string
	documentEngineToken string
	platformUserID string
}

type quoteRequest struct {
	DistanceKM      float64 `json:"distance_km"`
	SurgeMultiplier float64 `json:"surge_multiplier"`
}

type createRideRequest struct {
	RideID       string  `json:"ride_id"`
	RiderUserID  string  `json:"rider_user_id"`
	DriverUserID string  `json:"driver_user_id"`
	Origin       string  `json:"origin"`
	Destination  string  `json:"destination"`
	DistanceKM   float64 `json:"distance_km"`
	Fare         string  `json:"fare"`
}

type completeRideRequest struct {
	PaymentRef string `json:"payment_ref"`
}

func main() {
	port := envOrDefault("PORT", "8089")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@postgres:5432/nexora_pay?sslmode=disable")
	payBase := strings.TrimRight(envOrDefault("NEXORA_PAY_BASE_URL", "http://nexora-pay:8082"), "/")
	docBase := strings.TrimRight(envOrDefault("DOCUMENT_ENGINE_BASE_URL", "http://document-engine:8094"), "/")
	docToken := strings.TrimSpace(envOrDefault("DOCUMENT_ENGINE_TOKEN", "doc-engine-token"))
	platformUser := envOrDefault("NEXORA_MOVE_PLATFORM_USER", "nexora-move")

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

	s := &server{db: db, httpClient: &http.Client{Timeout: 12 * time.Second}, payBaseURL: payBase, documentEngineBase: docBase, documentEngineToken: docToken, platformUserID: platformUser}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/rides/quote", s.handleQuote)
	mux.HandleFunc("/v1/rides/create", s.handleCreateRide)
	mux.HandleFunc("/v1/rides/", s.handleRideRoutes)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-move listening on :%s", port)
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db_unavailable", "service": "nexora-move"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexora-move", "fee_pct": moveFeePct})
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
	if req.DistanceKM <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "distance_km must be greater than zero"})
		return
	}
	if req.SurgeMultiplier <= 0 {
		req.SurgeMultiplier = 1
	}
	if req.SurgeMultiplier > 3 {
		req.SurgeMultiplier = 3
	}
	fareCents := estimateFareCents(req.DistanceKM, req.SurgeMultiplier)
	writeJSON(w, http.StatusOK, map[string]any{
		"distance_km":      req.DistanceKM,
		"surge_multiplier": req.SurgeMultiplier,
		"fare":             centsToMoney(fareCents),
		"fee_preview":      centsToMoney(pctCents(fareCents, moveFeePct)),
	})
}

func (s *server) handleCreateRide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req createRideRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.RideID = normalizeID(req.RideID)
	req.RiderUserID = normalizeID(req.RiderUserID)
	req.DriverUserID = normalizeID(req.DriverUserID)
	req.Origin = strings.TrimSpace(req.Origin)
	req.Destination = strings.TrimSpace(req.Destination)

	if req.RideID == "" {
		req.RideID = fmt.Sprintf("move-ride-%d", time.Now().UTC().UnixNano())
	}
	if req.RiderUserID == "" || req.DriverUserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rider_user_id and driver_user_id are required"})
		return
	}
	if req.RiderUserID == req.DriverUserID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rider and driver must be different"})
		return
	}

	fareCents := int64(0)
	if strings.TrimSpace(req.Fare) != "" {
		parsedFare, err := parseMoneyToCents(req.Fare)
		if err != nil || parsedFare <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid fare"})
			return
		}
		fareCents = parsedFare
	} else {
		if req.DistanceKM <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "distance_km is required when fare is omitted"})
			return
		}
		fareCents = estimateFareCents(req.DistanceKM, 1)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO move_rides (
			ride_id, rider_user_id, driver_user_id, origin, destination,
			distance_km, fare_cents, fee_cents, driver_net_cents, status
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,0,0,'created'
		)
		ON CONFLICT (ride_id) DO UPDATE SET
			rider_user_id = EXCLUDED.rider_user_id,
			driver_user_id = EXCLUDED.driver_user_id,
			origin = EXCLUDED.origin,
			destination = EXCLUDED.destination,
			distance_km = EXCLUDED.distance_km,
			fare_cents = EXCLUDED.fare_cents,
			updated_at = NOW(),
			status = 'created'
	`, req.RideID, req.RiderUserID, req.DriverUserID, req.Origin, req.Destination, req.DistanceKM, fareCents)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create ride"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":         "created",
		"ride_id":        req.RideID,
		"rider_user_id":  req.RiderUserID,
		"driver_user_id": req.DriverUserID,
		"fare":           centsToMoney(fareCents),
		"fee_preview":    centsToMoney(pctCents(fareCents, moveFeePct)),
	})
}

func (s *server) handleRideRoutes(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "rides" {
		http.NotFound(w, r)
		return
	}
	rideID := normalizeID(parts[2])
	if rideID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ride id"})
		return
	}

	if len(parts) == 3 && r.Method == http.MethodGet {
		s.handleGetRide(w, r, rideID)
		return
	}
	if len(parts) == 4 && parts[3] == "complete" && r.Method == http.MethodPost {
		s.handleCompleteRide(w, r, rideID)
		return
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "unsupported ride route"})
}

func (s *server) handleGetRide(w http.ResponseWriter, r *http.Request, rideID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `
		SELECT ride_id, rider_user_id, driver_user_id, origin, destination,
			distance_km, fare_cents, fee_cents, driver_net_cents, status, created_at, updated_at
		FROM move_rides
		WHERE ride_id = $1
	`, rideID)
	var riderID, driverID, origin, destination, status string
	var distanceKM float64
	var fare, fee, driverNet int64
	var createdAt, updatedAt time.Time
	if err := row.Scan(&rideID, &riderID, &driverID, &origin, &destination, &distanceKM, &fare, &fee, &driverNet, &status, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ride not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load ride"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ride_id":         rideID,
		"rider_user_id":   riderID,
		"driver_user_id":  driverID,
		"origin":          origin,
		"destination":     destination,
		"distance_km":     distanceKM,
		"fare":            centsToMoney(fare),
		"fee":             centsToMoney(fee),
		"driver_net":      centsToMoney(driverNet),
		"status":          status,
		"created_at":      createdAt,
		"updated_at":      updatedAt,
	})
}

func (s *server) handleCompleteRide(w http.ResponseWriter, r *http.Request, rideID string) {
	var req completeRideRequest
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start transaction"})
		return
	}
	defer tx.Rollback()

	var riderID, driverID, status string
	var fareCents int64
	err = tx.QueryRowContext(ctx, `
		SELECT rider_user_id, driver_user_id, fare_cents, status
		FROM move_rides
		WHERE ride_id = $1
		FOR UPDATE
	`, rideID).Scan(&riderID, &driverID, &fareCents, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ride not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load ride"})
		return
	}
	if status == "paid" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "ride already paid"})
		return
	}

	if err := s.ensureWallet(ctx, riderID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare rider wallet: " + err.Error()})
		return
	}
	if err := s.ensureWallet(ctx, driverID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare driver wallet: " + err.Error()})
		return
	}
	if err := s.ensureWallet(ctx, s.platformUserID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare platform wallet: " + err.Error()})
		return
	}

	feeCents := pctCents(fareCents, moveFeePct)
	driverNet := fareCents - feeCents

	driverTransfer, err := s.payTransfer(ctx, riderID, driverID, centsToMoney(driverNet))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "driver transfer failed: " + err.Error()})
		return
	}
	platformTransfer, err := s.payTransfer(ctx, riderID, s.platformUserID, centsToMoney(feeCents))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "platform transfer failed: " + err.Error()})
		return
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE move_rides
		SET fee_cents = $1,
			driver_net_cents = $2,
			status = 'paid',
			payment_ref = $3,
			transfer_driver = $4::jsonb,
			transfer_platform = $5::jsonb,
			updated_at = NOW()
		WHERE ride_id = $6
	`, feeCents, driverNet, strings.TrimSpace(req.PaymentRef), mustJSON(driverTransfer), mustJSON(platformTransfer), rideID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update ride"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to commit ride payment"})
		return
	}

	s.emitPurchaseEvent(ctx, map[string]any{
		"source":         "move",
		"order_id":       rideID,
		"buyer_user_id":  riderID,
		"seller_user_id": driverID,
		"currency":       "BRL",
		"gross_cents":    fareCents,
		"fee_cents":      feeCents,
		"net_cents":      driverNet,
		"description":    "Corrida NEXORA MOVE",
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "paid",
		"ride_id":  rideID,
		"pricing":  map[string]any{"fare": centsToMoney(fareCents), "fee": centsToMoney(feeCents), "driver_net": centsToMoney(driverNet), "fee_pct": moveFeePct},
		"transfers": map[string]any{"driver": driverTransfer, "platform": platformTransfer},
	})
}

func estimateFareCents(distanceKM float64, surge float64) int64 {
	base := 600.0
	kmRate := 210.0
	fare := (base + kmRate*distanceKM) * surge
	if fare < 600 {
		fare = 600
	}
	return int64(math.Round(fare))
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

func (s *server) emitPurchaseEvent(ctx context.Context, payload map[string]any) {
	if s.documentEngineBase == "" {
		return
	}
	headers := map[string]string{"x-doc-engine-token": s.documentEngineToken}
	_, status, rawBody, err := s.callJSONWithHeaders(ctx, http.MethodPost, s.documentEngineBase+"/v1/events/purchase", payload, headers)
	if err != nil || status < 200 || status > 299 {
		log.Printf("document_event_failed source=move status=%d err=%v body=%s", status, err, rawBody)
	}
}

func (s *server) callJSON(ctx context.Context, method, endpoint string, payload any) (any, int, string, error) {
	return s.callJSONWithHeaders(ctx, method, endpoint, payload, nil)
}

func (s *server) callJSONWithHeaders(ctx context.Context, method, endpoint string, payload any, headers map[string]string) (any, int, string, error) {
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
CREATE TABLE IF NOT EXISTS move_rides (
	ride_id TEXT PRIMARY KEY,
	rider_user_id TEXT NOT NULL,
	driver_user_id TEXT NOT NULL,
	origin TEXT NOT NULL DEFAULT '',
	destination TEXT NOT NULL DEFAULT '',
	distance_km DOUBLE PRECISION NOT NULL DEFAULT 0,
	fare_cents BIGINT NOT NULL,
	fee_cents BIGINT NOT NULL,
	driver_net_cents BIGINT NOT NULL,
	status TEXT NOT NULL,
	payment_ref TEXT NOT NULL DEFAULT '',
	transfer_driver JSONB NOT NULL DEFAULT '{}'::jsonb,
	transfer_platform JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_move_rides_status_created_at ON move_rides(status, created_at DESC);
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
		w.Header().Set("X-Service", "nexora-move")
		next.ServeHTTP(w, r)
	})
}
