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
	db             *sql.DB
	httpClient     *http.Client
	payBaseURL     string
	upBaseURL      string
	documentEngineBase  string
	documentEngineToken string
	platformUserID string
	feePct         float64
}

type createSellerRequest struct {
	SellerID string `json:"seller_id"`
	Name     string `json:"name"`
	City     string `json:"city"`
	State    string `json:"state"`
}

type createItemRequest struct {
	ItemID    string `json:"item_id"`
	SellerID  string `json:"seller_id"`
	Title     string `json:"title"`
	Category  string `json:"category"`
	City      string `json:"city"`
	Price     string `json:"price"`
	StockQty  int    `json:"stock_qty"`
	SKU       string `json:"sku"`
}

type createOrderRequest struct {
	BuyerUserID string `json:"buyer_user_id"`
	ItemID      string `json:"item_id"`
	Quantity    int    `json:"quantity"`
}

func main() {
	port := envOrDefault("PORT", "8088")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@postgres:5432/nexora_pay?sslmode=disable")
	payBase := strings.TrimRight(envOrDefault("NEXORA_PAY_BASE_URL", "http://nexora-pay:8082"), "/")
	upBase := strings.TrimRight(envOrDefault("NEXORA_UP_BASE_URL", "http://nexora-up:8093"), "/")
	docBase := strings.TrimRight(envOrDefault("DOCUMENT_ENGINE_BASE_URL", "http://document-engine:8094"), "/")
	docToken := strings.TrimSpace(envOrDefault("DOCUMENT_ENGINE_TOKEN", "doc-engine-token"))
	platformUser := envOrDefault("NEXORA_PLACE_PLATFORM_USER", "nexora-place")
	feePct := envAsFloat("NEXORA_PLACE_FEE_PCT", 12.0)
	if feePct < 0 {
		feePct = 0
	}
	if feePct > 100 {
		feePct = 100
	}

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
		db:             db,
		httpClient:     &http.Client{Timeout: 12 * time.Second},
		payBaseURL:     payBase,
		upBaseURL:      upBase,
		documentEngineBase:  docBase,
		documentEngineToken: docToken,
		platformUserID: platformUser,
		feePct:         feePct,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/sellers", s.handleSellers)
	mux.HandleFunc("/v1/items", s.handleItems)
	mux.HandleFunc("/v1/orders/create", s.handleCreateOrder)
	mux.HandleFunc("/v1/orders/", s.handleOrderByID)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-place listening on :%s", port)
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db_unavailable", "service": "nexora-place"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexora-place", "fee_pct": s.feePct})
}

func (s *server) handleSellers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req createSellerRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		req.SellerID = normalizeID(req.SellerID)
		req.Name = strings.TrimSpace(req.Name)
		req.City = normalizeText(req.City)
		req.State = strings.ToUpper(strings.TrimSpace(req.State))
		if req.SellerID == "" || req.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "seller_id and name are required"})
			return
		}
		if req.City == "" {
			req.City = "betim"
		}
		if req.State == "" {
			req.State = "MG"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO place_sellers (seller_id, name, city, state)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (seller_id) DO UPDATE SET
				name = EXCLUDED.name,
				city = EXCLUDED.city,
				state = EXCLUDED.state,
				updated_at = NOW()
		`, req.SellerID, req.Name, req.City, req.State)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to upsert seller"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "seller_saved", "seller": req})
	case http.MethodGet:
		city := normalizeText(r.URL.Query().Get("city"))
		limit := clampInt(intOrDefault(r.URL.Query().Get("limit"), 100), 1, 300)
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		rows, err := s.db.QueryContext(ctx, `
			SELECT seller_id, name, city, state
			FROM place_sellers
			WHERE ($1 = '' OR city = $1)
			ORDER BY seller_id
			LIMIT $2
		`, city, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list sellers"})
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var sellerID, name, cityDB, state string
			if err := rows.Scan(&sellerID, &name, &cityDB, &state); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan sellers"})
				return
			}
			items = append(items, map[string]any{"seller_id": sellerID, "name": name, "city": cityDB, "state": state})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) handleItems(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req createItemRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		req.ItemID = normalizeID(req.ItemID)
		req.SellerID = normalizeID(req.SellerID)
		req.Title = strings.TrimSpace(req.Title)
		req.Category = normalizeText(req.Category)
		req.City = normalizeText(req.City)
		req.SKU = strings.TrimSpace(req.SKU)
		if req.ItemID == "" {
			req.ItemID = fmt.Sprintf("place-item-%d", time.Now().UTC().UnixNano())
		}
		if req.SellerID == "" || req.Title == "" || req.Price == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "seller_id, title and price are required"})
			return
		}
		if req.StockQty <= 0 {
			req.StockQty = 1
		}
		priceCents, err := parseMoneyToCents(req.Price)
		if err != nil || priceCents <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid price"})
			return
		}
		if req.City == "" {
			req.City = "betim"
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO place_items (item_id, seller_id, title, category, city, price_cents, stock_qty, sku)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (item_id) DO UPDATE SET
				seller_id = EXCLUDED.seller_id,
				title = EXCLUDED.title,
				category = EXCLUDED.category,
				city = EXCLUDED.city,
				price_cents = EXCLUDED.price_cents,
				stock_qty = EXCLUDED.stock_qty,
				sku = EXCLUDED.sku,
				updated_at = NOW()
		`, req.ItemID, req.SellerID, req.Title, req.Category, req.City, priceCents, req.StockQty, req.SKU)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to upsert item"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "item_saved", "item_id": req.ItemID, "price": centsToMoney(priceCents)})
	case http.MethodGet:
		city := normalizeText(r.URL.Query().Get("city"))
		if city == "" {
			city = "betim"
		}
		category := normalizeText(r.URL.Query().Get("category"))
		limit := clampInt(intOrDefault(r.URL.Query().Get("limit"), 120), 1, 500)
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		rows, err := s.db.QueryContext(ctx, `
			SELECT item_id, seller_id, title, category, city, price_cents, stock_qty, sku
			FROM place_items
			WHERE ($1 = '' OR city = $1)
			  AND ($2 = '' OR category = $2)
			  AND stock_qty > 0
			ORDER BY updated_at DESC
			LIMIT $3
		`, city, category, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list items"})
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var itemID, sellerID, title, categoryDB, cityDB, sku string
			var priceCents int64
			var stockQty int
			if err := rows.Scan(&itemID, &sellerID, &title, &categoryDB, &cityDB, &priceCents, &stockQty, &sku); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan items"})
				return
			}
			items = append(items, map[string]any{
				"item_id": itemID, "seller_id": sellerID, "title": title, "category": categoryDB,
				"city": cityDB, "price": centsToMoney(priceCents), "stock_qty": stockQty, "sku": sku,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req createOrderRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.BuyerUserID = normalizeID(req.BuyerUserID)
	req.ItemID = normalizeID(req.ItemID)
	if req.BuyerUserID == "" || req.ItemID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "buyer_user_id and item_id are required"})
		return
	}
	if req.Quantity <= 0 {
		req.Quantity = 1
	}
	if req.Quantity > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "quantity exceeds limit"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	if err := s.ensureWallet(ctx, req.BuyerUserID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare buyer wallet: " + err.Error()})
		return
	}
	if err := s.ensureWallet(ctx, s.platformUserID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare platform wallet: " + err.Error()})
		return
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start transaction"})
		return
	}
	defer tx.Rollback()

	var sellerID, title string
	var priceCents int64
	var stockQty int
	err = tx.QueryRowContext(ctx, `
		SELECT seller_id, title, price_cents, stock_qty
		FROM place_items
		WHERE item_id = $1
		FOR UPDATE
	`, req.ItemID).Scan(&sellerID, &title, &priceCents, &stockQty)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "item not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load item"})
		return
	}
	if stockQty < req.Quantity {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "insufficient stock"})
		return
	}

	if err := s.ensureWallet(ctx, sellerID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare seller wallet: " + err.Error()})
		return
	}

	totalCents := priceCents * int64(req.Quantity)
	feeCents := pctCents(totalCents, s.feePct)
	sellerNet := totalCents - feeCents

	sellerTransfer, err := s.payTransfer(ctx, req.BuyerUserID, sellerID, centsToMoney(sellerNet))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "seller transfer failed: " + err.Error()})
		return
	}
	platformTransfer, err := s.payTransfer(ctx, req.BuyerUserID, s.platformUserID, centsToMoney(feeCents))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "platform transfer failed: " + err.Error()})
		return
	}

	orderID := fmt.Sprintf("place-order-%d", time.Now().UTC().UnixNano())
	_, err = tx.ExecContext(ctx, `
		UPDATE place_items
		SET stock_qty = stock_qty - $1, updated_at = NOW()
		WHERE item_id = $2
	`, req.Quantity, req.ItemID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update stock"})
		return
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO place_orders (
			order_id, buyer_user_id, seller_user_id, item_id, quantity,
			total_cents, fee_cents, seller_net_cents, status,
			transfer_seller, transfer_platform
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,'paid',$9::jsonb,$10::jsonb
		)
	`, orderID, req.BuyerUserID, sellerID, req.ItemID, req.Quantity,
		totalCents, feeCents, sellerNet,
		mustJSON(sellerTransfer), mustJSON(platformTransfer),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to insert order"})
		return
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to commit order"})
		return
	}

	s.processAffiliateCommission(ctx, orderID, req.BuyerUserID, feeCents)
	s.emitPurchaseEvent(ctx, map[string]any{
		"source":         "place",
		"order_id":       orderID,
		"buyer_user_id":  req.BuyerUserID,
		"seller_user_id": sellerID,
		"currency":       "BRL",
		"gross_cents":    totalCents,
		"fee_cents":      feeCents,
		"net_cents":      sellerNet,
		"description":    "Compra NEXORA PLACE",
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":   "paid",
		"order_id": orderID,
		"item_id":  req.ItemID,
		"title":    title,
		"pricing": map[string]any{
			"unit_price":    centsToMoney(priceCents),
			"total":         centsToMoney(totalCents),
			"fee":           centsToMoney(feeCents),
			"seller_net":    centsToMoney(sellerNet),
			"fee_percentage": s.feePct,
		},
		"transfers": map[string]any{"seller": sellerTransfer, "platform": platformTransfer},
	})
}

func (s *server) handleOrderByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] != "orders" {
		http.NotFound(w, r)
		return
	}
	orderID := strings.TrimSpace(parts[2])
	if orderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid order id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	row := s.db.QueryRowContext(ctx, `
		SELECT order_id, buyer_user_id, seller_user_id, item_id, quantity, total_cents, fee_cents, seller_net_cents, status, created_at
		FROM place_orders
		WHERE order_id = $1
	`, orderID)
	var buyerID, sellerID, itemID, status string
	var quantity int
	var total, fee, net int64
	var createdAt time.Time
	if err := row.Scan(&orderID, &buyerID, &sellerID, &itemID, &quantity, &total, &fee, &net, &status, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "order not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load order"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"order_id":      orderID,
		"buyer_user_id": buyerID,
		"seller_user_id": sellerID,
		"item_id":       itemID,
		"quantity":      quantity,
		"total":         centsToMoney(total),
		"fee":           centsToMoney(fee),
		"seller_net":    centsToMoney(net),
		"status":        status,
		"created_at":    createdAt,
	})
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

func (s *server) processAffiliateCommission(ctx context.Context, orderID, buyerUserID string, marginCents int64) {
	if s.upBaseURL == "" || marginCents <= 0 {
		return
	}
	payload := map[string]any{
		"source":           "place",
		"order_id":         orderID,
		"buyer_user_id":    buyerUserID,
		"margin_cents":     marginCents,
		"platform_user_id": s.platformUserID,
		"currency":         "BRL",
	}
	_, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.upBaseURL+"/v1/commissions/process", payload)
	if err != nil || status < 200 || status > 299 {
		log.Printf("up_commission_failed source=place order_id=%s status=%d err=%v body=%s", orderID, status, err, rawBody)
	}
}

func (s *server) emitPurchaseEvent(ctx context.Context, payload map[string]any) {
	if s.documentEngineBase == "" {
		return
	}
	headers := map[string]string{"x-doc-engine-token": s.documentEngineToken}
	_, status, rawBody, err := s.callJSONWithHeaders(ctx, http.MethodPost, s.documentEngineBase+"/v1/events/purchase", payload, headers)
	if err != nil || status < 200 || status > 299 {
		log.Printf("document_event_failed source=place status=%d err=%v body=%s", status, err, rawBody)
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
CREATE TABLE IF NOT EXISTS place_sellers (
	seller_id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	city TEXT NOT NULL,
	state TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS place_items (
	item_id TEXT PRIMARY KEY,
	seller_id TEXT NOT NULL REFERENCES place_sellers(seller_id),
	title TEXT NOT NULL,
	category TEXT NOT NULL DEFAULT '',
	city TEXT NOT NULL,
	price_cents BIGINT NOT NULL,
	stock_qty INT NOT NULL,
	sku TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_place_items_city_category ON place_items(city, category);
CREATE INDEX IF NOT EXISTS ix_place_items_updated_at ON place_items(updated_at DESC);

CREATE TABLE IF NOT EXISTS place_orders (
	order_id TEXT PRIMARY KEY,
	buyer_user_id TEXT NOT NULL,
	seller_user_id TEXT NOT NULL,
	item_id TEXT NOT NULL REFERENCES place_items(item_id),
	quantity INT NOT NULL,
	total_cents BIGINT NOT NULL,
	fee_cents BIGINT NOT NULL,
	seller_net_cents BIGINT NOT NULL,
	status TEXT NOT NULL,
	transfer_seller JSONB NOT NULL DEFAULT '{}'::jsonb,
	transfer_platform JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
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

func envAsFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
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
		w.Header().Set("X-Service", "nexora-place")
		next.ServeHTTP(w, r)
	})
}
