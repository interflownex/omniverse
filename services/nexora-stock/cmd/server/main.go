package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type SupplierAdapter interface {
	Name() string
	Mapping() map[string]any
	SearchSuggestedProducts(ctx context.Context, category string, limit int) ([]SupplierProduct, error)
}

type TrackingAdapter interface {
	Carrier() string
	Mapping() map[string]any
	Track(ctx context.Context, trackingCode string) (TrackingSnapshot, error)
}

type SupplierProduct struct {
	Source       string `json:"source"`
	ExternalID   string `json:"external_id"`
	Category     string `json:"category"`
	Title        string `json:"title"`
	Currency     string `json:"currency"`
	CostCents    int64  `json:"cost_cents"`
	FreightCents int64  `json:"freight_cents"`
	ProductURL   string `json:"product_url"`
	ImageURL     string `json:"image_url"`
}

type ImportedProduct struct {
	ProductID       string    `json:"product_id"`
	Source          string    `json:"source"`
	ExternalID      string    `json:"external_id"`
	Category        string    `json:"category"`
	Title           string    `json:"title"`
	Currency        string    `json:"currency"`
	CostCents       int64     `json:"cost_cents"`
	FreightCents    int64     `json:"freight_cents"`
	MarginCents     int64     `json:"margin_cents"`
	FinalPriceCents int64     `json:"final_price_cents"`
	ProductURL      string    `json:"product_url"`
	ImageURL        string    `json:"image_url"`
	ImportedAt      time.Time `json:"imported_at"`
}

type TrackingEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Status    string    `json:"status"`
	Location  string    `json:"location"`
	Message   string    `json:"message"`
}

type TrackingSnapshot struct {
	Carrier      string          `json:"carrier"`
	TrackingCode string          `json:"tracking_code"`
	Status       string          `json:"status"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Events       []TrackingEvent `json:"events"`
}

type server struct {
	db                  *sql.DB
	httpClient          *http.Client
	suppliers           map[string]SupplierAdapter
	trackers            map[string]TrackingAdapter
	payBaseURL          string
	upBaseURL           string
	documentEngineBase  string
	documentEngineToken string
	platformUserID      string
	defaultCurrency     string
}

type restSupplierAdapter struct {
	name            string
	baseEnv         string
	tokenEnv        string
	searchPath      string
	defaultCurrency string
	defaultFreight  int64
	httpClient      *http.Client
}

type restTrackingAdapter struct {
	carrier    string
	baseEnv    string
	tokenEnv   string
	trackPath  string
	httpClient *http.Client
}

type importAutoRequest struct {
	Source              string   `json:"source"`
	Category            string   `json:"category"`
	Limit               int      `json:"limit"`
	SelectedExternalIDs []string `json:"selected_external_ids"`
}

type oneClickPurchaseRequest struct {
	VideoID       string `json:"video_id"`
	ProductID     string `json:"product_id"`
	BuyerUserID   string `json:"buyer_user_id"`
	SupplierUser  string `json:"supplier_user_id"`
	Quantity      int    `json:"quantity"`
	UnitCost      string `json:"unit_cost"`
	UnitFreight   string `json:"unit_freight"`
	Currency      string `json:"currency"`
	PaymentRef    string `json:"payment_ref"`
	ExternalOrder string `json:"external_order_id"`
}

func main() {
	port := envOrDefault("PORT", "8087")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@postgres:5432/nexora_pay?sslmode=disable")
	payBase := strings.TrimRight(envOrDefault("NEXORA_PAY_BASE_URL", "http://nexora-pay:8082"), "/")
	upBase := strings.TrimRight(envOrDefault("NEXORA_UP_BASE_URL", "http://nexora-up:8093"), "/")
	docBase := strings.TrimRight(envOrDefault("DOCUMENT_ENGINE_BASE_URL", "http://document-engine:8094"), "/")
	docToken := strings.TrimSpace(envOrDefault("DOCUMENT_ENGINE_TOKEN", "doc-engine-token"))
	platformUser := envOrDefault("NEXORA_STOCK_PLATFORM_USER", "nexora-stock")
	defaultCurrency := strings.ToUpper(envOrDefault("DEFAULT_CURRENCY", "BRL"))

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

	httpClient := &http.Client{Timeout: 12 * time.Second}

	s := &server{
		db:                  db,
		httpClient:          httpClient,
		suppliers:           buildSupplierAdapters(httpClient),
		trackers:            buildTrackingAdapters(httpClient),
		payBaseURL:          payBase,
		upBaseURL:           upBase,
		documentEngineBase:  docBase,
		documentEngineToken: docToken,
		platformUserID:      platformUser,
		defaultCurrency:     defaultCurrency,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/adapters/suppliers", s.handleSupplierAdapters)
	mux.HandleFunc("/v1/adapters/tracking", s.handleTrackingAdapters)
	mux.HandleFunc("/v1/products/suggestions", s.handleSuggestions)
	mux.HandleFunc("/v1/products/import-auto", s.handleImportAuto)
	mux.HandleFunc("/v1/products/imported", s.handleImportedList)
	mux.HandleFunc("/v1/tracking/status", s.handleTrackingStatus)
	mux.HandleFunc("/v1/tracking/status-all", s.handleTrackingStatusAll)
	mux.HandleFunc("/v1/payments/one-click", s.handleOneClickPurchase)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-stock listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func buildSupplierAdapters(client *http.Client) map[string]SupplierAdapter {
	adapters := []restSupplierAdapter{
		{name: "amazon", baseEnv: "AMAZON_API_BASE_URL", tokenEnv: "AMAZON_API_TOKEN", searchPath: "/catalog/suggestions", defaultCurrency: "BRL", defaultFreight: 2300, httpClient: client},
		{name: "alibaba", baseEnv: "ALIBABA_API_BASE_URL", tokenEnv: "ALIBABA_API_TOKEN", searchPath: "/product/suggest", defaultCurrency: "USD", defaultFreight: 2800, httpClient: client},
		{name: "cj-dropshipping", baseEnv: "CJ_API_BASE_URL", tokenEnv: "CJ_API_TOKEN", searchPath: "/api/v1/products/suggestions", defaultCurrency: "USD", defaultFreight: 3200, httpClient: client},
		{name: "aliexpress", baseEnv: "ALIEXPRESS_API_BASE_URL", tokenEnv: "ALIEXPRESS_API_TOKEN", searchPath: "/api/v1/recommendations", defaultCurrency: "USD", defaultFreight: 2600, httpClient: client},
		{name: "mercadolivre", baseEnv: "MERCADOLIVRE_API_BASE_URL", tokenEnv: "MERCADOLIVRE_API_TOKEN", searchPath: "/sites/MLB/search", defaultCurrency: "BRL", defaultFreight: 1800, httpClient: client},
		{name: "shopee", baseEnv: "SHOPEE_API_BASE_URL", tokenEnv: "SHOPEE_API_TOKEN", searchPath: "/api/v2/item/suggest", defaultCurrency: "BRL", defaultFreight: 2000, httpClient: client},
	}
	out := make(map[string]SupplierAdapter, len(adapters))
	for i := range adapters {
		adapter := adapters[i]
		out[adapter.name] = adapter
	}
	return out
}

func buildTrackingAdapters(client *http.Client) map[string]TrackingAdapter {
	adapters := []restTrackingAdapter{
		{carrier: "cainiao", baseEnv: "CAINIAO_API_BASE_URL", tokenEnv: "CAINIAO_API_TOKEN", trackPath: "/track/query", httpClient: client},
		{carrier: "17track", baseEnv: "TRACK17_API_BASE_URL", tokenEnv: "TRACK17_API_TOKEN", trackPath: "/tracking/status", httpClient: client},
		{carrier: "correios", baseEnv: "CORREIOS_API_BASE_URL", tokenEnv: "CORREIOS_API_TOKEN", trackPath: "/sro/v1/objects", httpClient: client},
		{carrier: "loggi", baseEnv: "LOGGI_API_BASE_URL", tokenEnv: "LOGGI_API_TOKEN", trackPath: "/tracking/v1/orders", httpClient: client},
	}
	out := make(map[string]TrackingAdapter, len(adapters))
	for i := range adapters {
		adapter := adapters[i]
		out[adapter.carrier] = adapter
	}
	return out
}

func (a restSupplierAdapter) Name() string {
	return a.name
}

func (a restSupplierAdapter) Mapping() map[string]any {
	return map[string]any{
		"name":        a.name,
		"base_env":    a.baseEnv,
		"token_env":   a.tokenEnv,
		"search_path": a.searchPath,
		"default_ccy": a.defaultCurrency,
		"mapped":      true,
		"api_type":    "rest",
	}
}

func (a restSupplierAdapter) SearchSuggestedProducts(ctx context.Context, category string, limit int) ([]SupplierProduct, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(a.baseEnv)), "/")
	token := strings.TrimSpace(os.Getenv(a.tokenEnv))
	if base != "" && token != "" {
		items, err := a.searchRemote(ctx, base, token, category, limit)
		if err == nil && len(items) > 0 {
			return items, nil
		}
	}
	return a.seedFallback(category, limit), nil
}

func (a restSupplierAdapter) searchRemote(ctx context.Context, baseURL, token, category string, limit int) ([]SupplierProduct, error) {
	u, err := url.Parse(baseURL + a.searchPath)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("category", category)
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("supplier %s status %d", a.name, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	candidates := pickArray(envelope, "items", "data", "products", "results")
	if len(candidates) == 0 {
		return nil, errors.New("empty payload")
	}

	out := make([]SupplierProduct, 0, len(candidates))
	for i, row := range candidates {
		if i >= limit {
			break
		}
		rec, ok := row.(map[string]any)
		if !ok {
			continue
		}
		extID := strAny(rec["id"])
		if extID == "" {
			extID = strAny(rec["sku"])
		}
		if extID == "" {
			extID = fmt.Sprintf("%s-%d", a.name, i+1)
		}
		title := strAny(rec["title"])
		if title == "" {
			title = strAny(rec["name"])
		}
		if title == "" {
			title = fmt.Sprintf("%s item %d", strings.Title(a.name), i+1)
		}
		costCents := parseNumberAsCents(rec["cost"], rec["price"], int64(10000+(i*250)))
		freightCents := parseNumberAsCents(rec["freight"], rec["shipping"], a.defaultFreight)
		currency := strings.ToUpper(strAny(rec["currency"]))
		if currency == "" {
			currency = a.defaultCurrency
		}
		out = append(out, SupplierProduct{
			Source:       a.name,
			ExternalID:   extID,
			Category:     category,
			Title:        title,
			Currency:     currency,
			CostCents:    costCents,
			FreightCents: freightCents,
			ProductURL:   strAny(rec["product_url"]),
			ImageURL:     strAny(rec["image_url"]),
		})
	}
	if len(out) == 0 {
		return nil, errors.New("empty mapped output")
	}
	return out, nil
}

func (a restSupplierAdapter) seedFallback(category string, limit int) []SupplierProduct {
	out := make([]SupplierProduct, 0, limit)
	for i := 0; i < limit; i++ {
		h := hash64(fmt.Sprintf("%s|%s|%d", a.name, category, i+1))
		cost := int64(4500 + (h % 30000))
		freight := a.defaultFreight + int64(h%900)
		extID := fmt.Sprintf("%s-%s-%03d", a.name, slug(category), i+1)
		out = append(out, SupplierProduct{
			Source:       a.name,
			ExternalID:   extID,
			Category:     category,
			Title:        fmt.Sprintf("%s %s item %d", strings.Title(strings.ReplaceAll(a.name, "-", " ")), strings.Title(category), i+1),
			Currency:     a.defaultCurrency,
			CostCents:    cost,
			FreightCents: freight,
			ProductURL:   fmt.Sprintf("https://%s.example/products/%s", strings.ReplaceAll(a.name, " ", ""), extID),
			ImageURL:     fmt.Sprintf("https://cdn.%s.example/images/%s.jpg", strings.ReplaceAll(a.name, " ", ""), extID),
		})
	}
	return out
}

func (a restTrackingAdapter) Carrier() string {
	return a.carrier
}

func (a restTrackingAdapter) Mapping() map[string]any {
	return map[string]any{
		"carrier":    a.carrier,
		"base_env":   a.baseEnv,
		"token_env":  a.tokenEnv,
		"track_path": a.trackPath,
		"mapped":     true,
		"api_type":   "rest",
	}
}

func (a restTrackingAdapter) Track(ctx context.Context, trackingCode string) (TrackingSnapshot, error) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(a.baseEnv)), "/")
	token := strings.TrimSpace(os.Getenv(a.tokenEnv))
	if base != "" && token != "" {
		track, err := a.trackRemote(ctx, base, token, trackingCode)
		if err == nil {
			return track, nil
		}
	}
	return a.seedTracking(trackingCode), nil
}

func (a restTrackingAdapter) trackRemote(ctx context.Context, baseURL, token, trackingCode string) (TrackingSnapshot, error) {
	u, err := url.Parse(baseURL + a.trackPath)
	if err != nil {
		return TrackingSnapshot{}, err
	}
	q := u.Query()
	q.Set("tracking_code", trackingCode)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return TrackingSnapshot{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return TrackingSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return TrackingSnapshot{}, fmt.Errorf("tracking %s status %d", a.carrier, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return TrackingSnapshot{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return TrackingSnapshot{}, err
	}
	status := strAny(payload["status"])
	if status == "" {
		status = "in_transit"
	}
	now := time.Now().UTC()
	events := []TrackingEvent{{
		Timestamp: now,
		Status:    status,
		Location:  strAny(payload["location"]),
		Message:   "remote tracking adapter response",
	}}
	return TrackingSnapshot{
		Carrier:      a.carrier,
		TrackingCode: trackingCode,
		Status:       status,
		UpdatedAt:    now,
		Events:       events,
	}, nil
}

func (a restTrackingAdapter) seedTracking(trackingCode string) TrackingSnapshot {
	now := time.Now().UTC()
	h := hash64(a.carrier + "|" + trackingCode)
	statuses := []string{"label_created", "in_transit", "out_for_delivery", "delivered"}
	idx := int(h % uint64(len(statuses)))
	if idx < 0 {
		idx = 0
	}
	status := statuses[idx]
	events := []TrackingEvent{
		{Timestamp: now.Add(-14 * time.Hour), Status: "label_created", Location: "origin hub", Message: "shipment created"},
		{Timestamp: now.Add(-8 * time.Hour), Status: "in_transit", Location: "distribution center", Message: "package moving"},
	}
	if status == "out_for_delivery" || status == "delivered" {
		events = append(events, TrackingEvent{Timestamp: now.Add(-2 * time.Hour), Status: "out_for_delivery", Location: "destination city", Message: "courier route active"})
	}
	if status == "delivered" {
		events = append(events, TrackingEvent{Timestamp: now.Add(-30 * time.Minute), Status: "delivered", Location: "recipient address", Message: "package delivered"})
	}
	return TrackingSnapshot{Carrier: a.carrier, TrackingCode: trackingCode, Status: status, UpdatedAt: now, Events: events}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db_unavailable", "service": "nexora-stock"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexora-stock", "supplier_adapters": len(s.suppliers), "tracking_adapters": len(s.trackers)})
}

func (s *server) handleSupplierAdapters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	items := make([]map[string]any, 0, len(s.suppliers))
	keys := sortedKeys(s.suppliers)
	for _, name := range keys {
		items = append(items, s.suppliers[name].Mapping())
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) handleTrackingAdapters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	items := make([]map[string]any, 0, len(s.trackers))
	keys := sortedKeys(s.trackers)
	for _, name := range keys {
		items = append(items, s.trackers[name].Mapping())
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) handleSuggestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	if category == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "category is required"})
		return
	}
	limit := intOrDefault(r.URL.Query().Get("limit"), 20)
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	source := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("source")))

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	items, warnings := s.collectSuggestions(ctx, source, category, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"category": category,
		"source":   chooseSourceLabel(source),
		"count":    len(items),
		"data":     items,
		"warnings": warnings,
	})
}

func (s *server) handleImportAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/products/import-auto" {
		http.NotFound(w, r)
		return
	}

	var req importAutoRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.Source = strings.TrimSpace(strings.ToLower(req.Source))
	req.Category = strings.TrimSpace(strings.ToLower(req.Category))
	if req.Category == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "category is required"})
		return
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	suggestions, warnings := s.collectSuggestions(ctx, req.Source, req.Category, req.Limit)
	if len(suggestions) == 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "no products returned by suppliers", "warnings": warnings})
		return
	}

	selected := map[string]struct{}{}
	for _, extID := range req.SelectedExternalIDs {
		n := strings.TrimSpace(strings.ToLower(extID))
		if n != "" {
			selected[n] = struct{}{}
		}
	}
	if len(selected) > 0 {
		filtered := make([]SupplierProduct, 0, len(suggestions))
		for _, item := range suggestions {
			if _, ok := selected[strings.TrimSpace(strings.ToLower(item.ExternalID))]; ok {
				filtered = append(filtered, item)
			}
		}
		suggestions = filtered
		if len(suggestions) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":    "selected_external_ids did not match any suggested products",
				"category": req.Category,
				"source":   chooseSourceLabel(req.Source),
			})
			return
		}
	}

	imported := make([]ImportedProduct, 0, len(suggestions))
	for _, item := range suggestions {
		margin := calculateMarginCents(item.CostCents, item.FreightCents)
		final := item.CostCents + item.FreightCents + margin
		productID := buildImportedProductID(item.Source, item.ExternalID, req.Category)
		if err := s.upsertImportedProduct(ctx, productID, item, req.Category, margin, final); err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		imported = append(imported, ImportedProduct{
			ProductID:       productID,
			Source:          item.Source,
			ExternalID:      item.ExternalID,
			Category:        req.Category,
			Title:           item.Title,
			Currency:        normalizeCurrency(item.Currency, s.defaultCurrency),
			CostCents:       item.CostCents,
			FreightCents:    item.FreightCents,
			MarginCents:     margin,
			FinalPriceCents: final,
			ProductURL:      item.ProductURL,
			ImageURL:        item.ImageURL,
			ImportedAt:      time.Now().UTC(),
		})
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":   "imported",
		"category": req.Category,
		"count":    len(imported),
		"warnings": warnings,
		"data":     imported,
	})
}

func (s *server) handleImportedList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	source := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("source")))
	category := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("category")))
	limit := intOrDefault(r.URL.Query().Get("limit"), 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
		SELECT product_id, source, external_id, category, title, currency, cost_cents, freight_cents, margin_cents, final_price_cents, product_url, image_url, imported_at
		FROM stock_imported_products
		WHERE ($1 = '' OR source = $1)
		  AND ($2 = '' OR category = $2)
		ORDER BY imported_at DESC
		LIMIT $3
	`, source, category, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to query imported products"})
		return
	}
	defer rows.Close()

	items := make([]ImportedProduct, 0)
	for rows.Next() {
		var p ImportedProduct
		if err := rows.Scan(&p.ProductID, &p.Source, &p.ExternalID, &p.Category, &p.Title, &p.Currency, &p.CostCents, &p.FreightCents, &p.MarginCents, &p.FinalPriceCents, &p.ProductURL, &p.ImageURL, &p.ImportedAt); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan imported products"})
			return
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load imported products"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) handleTrackingStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	carrier := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("carrier")))
	trackingCode := strings.TrimSpace(r.URL.Query().Get("tracking_code"))
	if carrier == "" || trackingCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "carrier and tracking_code are required"})
		return
	}
	adapter, ok := s.trackers[carrier]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "carrier adapter not found"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	result, err := adapter.Track(ctx, trackingCode)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) handleTrackingStatusAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	trackingCode := strings.TrimSpace(r.URL.Query().Get("tracking_code"))
	if trackingCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tracking_code is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	items := make([]TrackingSnapshot, 0, len(s.trackers))
	warnings := []string{}
	for _, carrier := range sortedKeys(s.trackers) {
		snapshot, err := s.trackers[carrier].Track(ctx, trackingCode)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", carrier, err))
			continue
		}
		items = append(items, snapshot)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tracking_code": trackingCode,
		"count":         len(items),
		"data":          items,
		"warnings":      warnings,
	})
}

func (s *server) handleOneClickPurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/payments/one-click" {
		http.NotFound(w, r)
		return
	}

	var req oneClickPurchaseRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.BuyerUserID = strings.TrimSpace(strings.ToLower(req.BuyerUserID))
	req.SupplierUser = strings.TrimSpace(strings.ToLower(req.SupplierUser))
	req.ProductID = strings.TrimSpace(req.ProductID)
	req.VideoID = strings.TrimSpace(req.VideoID)
	req.Currency = normalizeCurrency(req.Currency, s.defaultCurrency)
	if req.Quantity <= 0 {
		req.Quantity = 1
	}
	if req.Quantity > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "quantity exceeds limit"})
		return
	}
	if req.BuyerUserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "buyer_user_id is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	unitCost := int64(0)
	unitFreight := int64(0)
	productSource := "manual"
	if req.ProductID != "" {
		row := s.db.QueryRowContext(ctx, `
			SELECT source, cost_cents, freight_cents
			FROM stock_imported_products
			WHERE product_id = $1
		`, req.ProductID)
		if err := row.Scan(&productSource, &unitCost, &unitFreight); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "product_id not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load imported product"})
			return
		}
		if req.SupplierUser == "" {
			req.SupplierUser = "supplier-" + productSource
		}
	} else {
		parsedCost, err := parseMoneyToCents(req.UnitCost)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unit_cost is required when product_id is not provided"})
			return
		}
		parsedFreight, err := parseMoneyToCents(req.UnitFreight)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unit_freight is required when product_id is not provided"})
			return
		}
		unitCost = parsedCost
		unitFreight = parsedFreight
		if req.SupplierUser == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "supplier_user_id is required for manual purchase"})
			return
		}
	}

	unitMargin := calculateMarginCents(unitCost, unitFreight)
	unitBase := unitCost + unitFreight
	unitFinal := unitBase + unitMargin
	supplierSplit := unitBase * int64(req.Quantity)
	nexoraSplit := unitMargin * int64(req.Quantity)
	gross := unitFinal * int64(req.Quantity)

	if err := s.ensureWallet(ctx, req.BuyerUserID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare buyer wallet: " + err.Error()})
		return
	}
	if err := s.ensureWallet(ctx, req.SupplierUser); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare supplier wallet: " + err.Error()})
		return
	}
	if err := s.ensureWallet(ctx, s.platformUserID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare platform wallet: " + err.Error()})
		return
	}

	supplierTransfer, err := s.payTransfer(ctx, req.BuyerUserID, req.SupplierUser, centsToMoney(supplierSplit))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "supplier split transfer failed: " + err.Error()})
		return
	}
	platformTransfer, err := s.payTransfer(ctx, req.BuyerUserID, s.platformUserID, centsToMoney(nexoraSplit))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "nexora split transfer failed: " + err.Error()})
		return
	}

	orderID := fmt.Sprintf("stock-order-%d", time.Now().UTC().UnixNano())
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO stock_one_click_orders (
			order_id, video_id, product_id, buyer_user_id, supplier_user_id, source,
			quantity, currency, gross_cents, supplier_split_cents, nexora_split_cents,
			payment_ref, external_order_id, status, transfer_supplier, transfer_platform
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'paid',$14::jsonb,$15::jsonb
		)
	`, orderID, req.VideoID, req.ProductID, req.BuyerUserID, req.SupplierUser, productSource,
		req.Quantity, req.Currency, gross, supplierSplit, nexoraSplit,
		req.PaymentRef, req.ExternalOrder,
		mustJSON(supplierTransfer), mustJSON(platformTransfer),
	)

	s.processAffiliateCommission(ctx, orderID, req.BuyerUserID, nexoraSplit)
	s.emitPurchaseEvent(ctx, map[string]any{
		"source":         "stock",
		"order_id":       orderID,
		"buyer_user_id":  req.BuyerUserID,
		"seller_user_id": req.SupplierUser,
		"currency":       "BRL",
		"gross_cents":    gross,
		"fee_cents":      nexoraSplit,
		"net_cents":      supplierSplit,
		"description":    "Compra 1-click NEXORA STOCK",
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "paid",
		"order_id":   orderID,
		"video_id":   req.VideoID,
		"product_id": req.ProductID,
		"quantity":   req.Quantity,
		"currency":   req.Currency,
		"pricing": map[string]any{
			"unit_cost":      centsToMoney(unitCost),
			"unit_freight":   centsToMoney(unitFreight),
			"unit_margin":    centsToMoney(unitMargin),
			"unit_final":     centsToMoney(unitFinal),
			"total_gross":    centsToMoney(gross),
			"split_supplier": centsToMoney(supplierSplit),
			"split_nexora":   centsToMoney(nexoraSplit),
		},
		"transfers": map[string]any{
			"supplier": supplierTransfer,
			"nexora":   platformTransfer,
		},
	})
}

func (s *server) collectSuggestions(ctx context.Context, source, category string, limit int) ([]SupplierProduct, []string) {
	warnings := []string{}
	results := make([]SupplierProduct, 0)

	if source != "" && source != "all" {
		adapter, ok := s.suppliers[source]
		if !ok {
			return nil, []string{"supplier adapter not found: " + source}
		}
		items, err := adapter.SearchSuggestedProducts(ctx, category, limit)
		if err != nil {
			return nil, []string{err.Error()}
		}
		return items, warnings
	}

	for _, name := range sortedKeys(s.suppliers) {
		items, err := s.suppliers[name].SearchSuggestedProducts(ctx, category, limit)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		results = append(results, items...)
	}

	if len(results) > limit*len(s.suppliers) {
		results = results[:limit*len(s.suppliers)]
	}
	return results, warnings
}

func (s *server) upsertImportedProduct(ctx context.Context, productID string, item SupplierProduct, category string, marginCents, finalCents int64) error {
	currency := normalizeCurrency(item.Currency, s.defaultCurrency)
	raw, _ := json.Marshal(item)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO stock_imported_products (
			product_id, source, external_id, category, title, currency,
			cost_cents, freight_cents, margin_cents, final_price_cents, product_url, image_url, raw_json
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb
		)
		ON CONFLICT (product_id) DO UPDATE SET
			title = EXCLUDED.title,
			currency = EXCLUDED.currency,
			cost_cents = EXCLUDED.cost_cents,
			freight_cents = EXCLUDED.freight_cents,
			margin_cents = EXCLUDED.margin_cents,
			final_price_cents = EXCLUDED.final_price_cents,
			product_url = EXCLUDED.product_url,
			image_url = EXCLUDED.image_url,
			raw_json = EXCLUDED.raw_json,
			imported_at = NOW()
	`, productID, item.Source, item.ExternalID, category, item.Title, currency,
		item.CostCents, item.FreightCents, marginCents, finalCents, item.ProductURL, item.ImageURL, string(raw))
	return err
}

func (s *server) ensureWallet(ctx context.Context, userID string) error {
	payload := map[string]any{
		"user_id":     userID,
		"initial_brl": "0.00",
		"initial_nex": "0.000000",
	}
	_, status, body, err := s.callJSON(ctx, http.MethodPost, s.payBaseURL+"/v1/wallets", payload, nil)
	if err != nil {
		return err
	}
	if status == http.StatusCreated || status == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("wallet create unexpected status %d: %s", status, body)
}

func (s *server) payTransfer(ctx context.Context, fromUser, toUser, amount string) (map[string]any, error) {
	payload := map[string]any{
		"from_user_id": fromUser,
		"to_user_id":   toUser,
		"currency":     "BRL",
		"amount":       amount,
	}
	respBody, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.payBaseURL+"/v1/wallets/transfer", payload, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("nexora-pay transfer status %d: %s", status, rawBody)
	}
	m, _ := respBody.(map[string]any)
	if m == nil {
		m = map[string]any{"raw": rawBody}
	}
	return m, nil
}

func (s *server) processAffiliateCommission(ctx context.Context, orderID, buyerUserID string, marginCents int64) {
	if s.upBaseURL == "" || marginCents <= 0 {
		return
	}
	payload := map[string]any{
		"source":           "stock",
		"order_id":         orderID,
		"buyer_user_id":    buyerUserID,
		"margin_cents":     marginCents,
		"platform_user_id": s.platformUserID,
		"currency":         "BRL",
	}
	_, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.upBaseURL+"/v1/commissions/process", payload, nil)
	if err != nil || status < 200 || status > 299 {
		log.Printf("up_commission_failed source=stock order_id=%s status=%d err=%v body=%s", orderID, status, err, rawBody)
	}
}

func (s *server) emitPurchaseEvent(ctx context.Context, payload map[string]any) {
	if s.documentEngineBase == "" {
		return
	}
	headers := map[string]string{"x-doc-engine-token": s.documentEngineToken}
	_, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.documentEngineBase+"/v1/events/purchase", payload, headers)
	if err != nil || status < 200 || status > 299 {
		log.Printf("document_event_failed source=stock status=%d err=%v body=%s", status, err, rawBody)
	}
}

func (s *server) callJSON(ctx context.Context, method, endpoint string, payload any, headers map[string]string) (any, int, string, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, "", err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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
CREATE TABLE IF NOT EXISTS stock_imported_products (
	product_id TEXT PRIMARY KEY,
	source TEXT NOT NULL,
	external_id TEXT NOT NULL,
	category TEXT NOT NULL,
	title TEXT NOT NULL,
	currency TEXT NOT NULL,
	cost_cents BIGINT NOT NULL,
	freight_cents BIGINT NOT NULL,
	margin_cents BIGINT NOT NULL,
	final_price_cents BIGINT NOT NULL,
	product_url TEXT NOT NULL DEFAULT '',
	image_url TEXT NOT NULL DEFAULT '',
	imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	raw_json JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_stock_source_external_category ON stock_imported_products(source, external_id, category);
CREATE INDEX IF NOT EXISTS ix_stock_imported_at ON stock_imported_products(imported_at DESC);

CREATE TABLE IF NOT EXISTS stock_one_click_orders (
	order_id TEXT PRIMARY KEY,
	video_id TEXT NOT NULL DEFAULT '',
	product_id TEXT NOT NULL DEFAULT '',
	buyer_user_id TEXT NOT NULL,
	supplier_user_id TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	quantity INT NOT NULL,
	currency TEXT NOT NULL,
	gross_cents BIGINT NOT NULL,
	supplier_split_cents BIGINT NOT NULL,
	nexora_split_cents BIGINT NOT NULL,
	payment_ref TEXT NOT NULL DEFAULT '',
	external_order_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	transfer_supplier JSONB NOT NULL DEFAULT '{}'::jsonb,
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

func pickArray(payload map[string]any, keys ...string) []any {
	for _, key := range keys {
		if raw, ok := payload[key]; ok {
			if arr, ok := raw.([]any); ok {
				return arr
			}
		}
	}
	return nil
}

func strAny(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case fmt.Stringer:
		return strings.TrimSpace(t.String())
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(t, 'f', -1, 64))
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return ""
	}
}

func parseNumberAsCents(primary any, secondary any, fallback int64) int64 {
	for _, raw := range []any{primary, secondary} {
		s := strAny(raw)
		if s == "" {
			continue
		}
		c, err := parseMoneyToCents(s)
		if err == nil {
			return c
		}
	}
	return fallback
}

func parseMoneyToCents(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("empty money string")
	}
	raw = strings.ReplaceAll(raw, ",", ".")
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, errors.New("money cannot be negative")
	}
	return int64(math.Round(value * 100)), nil
}

func centsToMoney(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

func calculateMarginCents(costCents, freightCents int64) int64 {
	base := costCents + freightCents
	if base < 0 {
		base = 0
	}
	return int64(math.Round(float64(base) * 0.50))
}

func chooseSourceLabel(source string) string {
	if source == "" {
		return "all"
	}
	return source
}

func normalizeCurrency(value, fallback string) string {
	v := strings.ToUpper(strings.TrimSpace(value))
	if v == "" {
		return strings.ToUpper(strings.TrimSpace(fallback))
	}
	return v
}

func buildImportedProductID(source, externalID, category string) string {
	seed := strings.ToLower(source + "|" + externalID + "|" + category)
	h := hash64(seed)
	return fmt.Sprintf("stk-%s-%x", slug(source), h)
}

func slug(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "na"
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
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "na"
	}
	if len(out) > 40 {
		return out[:40]
	}
	return out
}

func hash64(input string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(input))
	return h.Sum64()
}

func mustJSON(v any) string {
	encoded, _ := json.Marshal(v)
	return string(encoded)
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func intOrDefault(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
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

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withServiceHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Service", "nexora-stock")
		next.ServeHTTP(w, r)
	})
}
