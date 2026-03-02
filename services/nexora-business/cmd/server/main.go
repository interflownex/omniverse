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
	"sync"
	"time"

	_ "github.com/lib/pq"
)

type server struct {
	db                  *sql.DB
	httpClient          *http.Client
	payBaseURL          string
	placeBaseURL        string
	documentEngineBase  string
	documentEngineToken string
	shieldMu            sync.RWMutex
	shieldPolicies      map[string]shieldPolicy
	shieldToken         string
}

type createCompanyRequest struct {
	CompanyID      string `json:"company_id"`
	Name           string `json:"name"`
	DocumentNumber string `json:"document_number"`
	City           string `json:"city"`
	State          string `json:"state"`
	PlaceSellerID  string `json:"place_seller_id"`
	PayrollWallet  string `json:"payroll_wallet_user_id"`
}

type issueInvoiceRequest struct {
	CompanyID      string `json:"company_id"`
	InvoiceID      string `json:"invoice_id"`
	BuyerUserID    string `json:"buyer_user_id"`
	Description    string `json:"description"`
	Subtotal       string `json:"subtotal_brl"`
	Tax            string `json:"tax_brl"`
	Freight        string `json:"freight_brl"`
	AutoSettle     bool   `json:"auto_settle"`
	PaymentRef     string `json:"payment_ref"`
	ExternalNumber string `json:"external_number"`
}

type syncInventoryRequest struct {
	CompanyID string `json:"company_id"`
	Items     []struct {
		ItemID    string `json:"item_id"`
		Title     string `json:"title"`
		Category  string `json:"category"`
		Price     string `json:"price"`
		StockQty  int    `json:"stock_qty"`
		SKU       string `json:"sku"`
		City      string `json:"city"`
	} `json:"items"`
}

type processPayrollRequest struct {
	CompanyID string `json:"company_id"`
	BatchID   string `json:"batch_id"`
	Items     []struct {
		EmployeeUserID string `json:"employee_user_id"`
		AmountBRL      string `json:"amount_brl"`
		Reference      string `json:"reference"`
	} `json:"items"`
}

type shieldPolicy struct {
	Blocked   bool      `json:"blocked"`
	Reason    string    `json:"reason"`
	Until     time.Time `json:"until"`
	UpdatedAt time.Time `json:"updated_at"`
}

type updateShieldRequest struct {
	UserID  string `json:"user_id"`
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason"`
	Until   string `json:"until"`
}

type dispatchNotificationRequest struct {
	NotificationID string         `json:"notification_id"`
	UserID         string         `json:"user_id"`
	Channel        string         `json:"channel"`
	Title          string         `json:"title"`
	Message        string         `json:"message"`
	Context        map[string]any `json:"context"`
}

func main() {
	port := envOrDefault("PORT", "8091")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@postgres:5432/nexora_pay?sslmode=disable")
	payBase := strings.TrimRight(envOrDefault("NEXORA_PAY_BASE_URL", "http://nexora-pay:8082"), "/")
	placeBase := strings.TrimRight(envOrDefault("NEXORA_PLACE_BASE_URL", "http://nexora-place:8088"), "/")
	docBase := strings.TrimRight(envOrDefault("DOCUMENT_ENGINE_BASE_URL", "http://document-engine:8094"), "/")
	docToken := strings.TrimSpace(envOrDefault("DOCUMENT_ENGINE_TOKEN", "doc-engine-token"))
	shieldToken := strings.TrimSpace(envOrDefault("BUSINESS_SHIELD_TOKEN", "persona-ai-token"))

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
		placeBaseURL:        placeBase,
		documentEngineBase:  docBase,
		documentEngineToken: docToken,
		shieldPolicies:      map[string]shieldPolicy{},
		shieldToken:         shieldToken,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/companies", s.handleCompanies)
	mux.HandleFunc("/v1/invoices/issue", s.handleIssueInvoice)
	mux.HandleFunc("/v1/invoices/", s.handleGetInvoice)
	mux.HandleFunc("/v1/inventory/sync-place", s.handleSyncInventoryPlace)
	mux.HandleFunc("/v1/payroll/process", s.handleProcessPayroll)
	mux.HandleFunc("/v1/business/policy/shield", s.handleShieldPolicy)
	mux.HandleFunc("/v1/business/notifications/dispatch", s.handleDispatchNotification)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      40 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-business listening on :%s", port)
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db_unavailable", "service": "nexora-business"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexora-business", "place_base": s.placeBaseURL})
}

func (s *server) handleCompanies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req createCompanyRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		req.CompanyID = normalizeID(req.CompanyID)
		req.Name = strings.TrimSpace(req.Name)
		req.DocumentNumber = strings.TrimSpace(req.DocumentNumber)
		req.City = normalizeText(req.City)
		req.State = strings.ToUpper(strings.TrimSpace(req.State))
		req.PlaceSellerID = normalizeID(req.PlaceSellerID)
		req.PayrollWallet = normalizeID(req.PayrollWallet)
		if req.CompanyID == "" || req.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "company_id and name are required"})
			return
		}
		if req.City == "" {
			req.City = "betim"
		}
		if req.State == "" {
			req.State = "MG"
		}
		if req.PlaceSellerID == "" {
			req.PlaceSellerID = req.CompanyID + "-seller"
		}
		if req.PayrollWallet == "" {
			req.PayrollWallet = req.CompanyID + "-payroll"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		if err := s.ensureWallet(ctx, req.PayrollWallet); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare payroll wallet: " + err.Error()})
			return
		}
		if err := s.upsertPlaceSeller(ctx, req.PlaceSellerID, req.Name, req.City, req.State); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to sync seller in place: " + err.Error()})
			return
		}
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO business_companies (company_id, name, document_number, city, state, place_seller_id, payroll_wallet_user_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (company_id) DO UPDATE SET
				name = EXCLUDED.name,
				document_number = EXCLUDED.document_number,
				city = EXCLUDED.city,
				state = EXCLUDED.state,
				place_seller_id = EXCLUDED.place_seller_id,
				payroll_wallet_user_id = EXCLUDED.payroll_wallet_user_id,
				updated_at = NOW()
		`, req.CompanyID, req.Name, req.DocumentNumber, req.City, req.State, req.PlaceSellerID, req.PayrollWallet)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to upsert company"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "company_saved", "company": req})
	case http.MethodGet:
		limit := clampInt(intOrDefault(r.URL.Query().Get("limit"), 100), 1, 400)
		city := normalizeText(r.URL.Query().Get("city"))
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		rows, err := s.db.QueryContext(ctx, `
			SELECT company_id, name, document_number, city, state, place_seller_id, payroll_wallet_user_id, created_at
			FROM business_companies
			WHERE ($1 = '' OR city = $1)
			ORDER BY company_id
			LIMIT $2
		`, city, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list companies"})
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var companyID, name, doc, cityDB, state, sellerID, payrollWallet string
			var createdAt time.Time
			if err := rows.Scan(&companyID, &name, &doc, &cityDB, &state, &sellerID, &payrollWallet, &createdAt); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan companies"})
				return
			}
			items = append(items, map[string]any{
				"company_id": companyID, "name": name, "document_number": doc,
				"city": cityDB, "state": state, "place_seller_id": sellerID,
				"payroll_wallet_user_id": payrollWallet, "created_at": createdAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) handleIssueInvoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req issueInvoiceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.CompanyID = normalizeID(req.CompanyID)
	req.InvoiceID = strings.TrimSpace(req.InvoiceID)
	req.BuyerUserID = normalizeID(req.BuyerUserID)
	req.Description = strings.TrimSpace(req.Description)
	req.PaymentRef = strings.TrimSpace(req.PaymentRef)
	req.ExternalNumber = strings.TrimSpace(req.ExternalNumber)
	if req.CompanyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "company_id is required"})
		return
	}
	if req.InvoiceID == "" {
		req.InvoiceID = fmt.Sprintf("invoice-%d", time.Now().UTC().UnixNano())
	}
	subtotalCents, err := parseMoneyToCents(req.Subtotal)
	if err != nil || subtotalCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subtotal_brl must be greater than zero"})
		return
	}
	taxCents, err := parseMoneyToCentsDefault(req.Tax)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tax_brl"})
		return
	}
	freightCents, err := parseMoneyToCentsDefault(req.Freight)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid freight_brl"})
		return
	}
	totalCents := subtotalCents + taxCents + freightCents
	if totalCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "total must be positive"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	var payrollWallet, companyName string
	err = s.db.QueryRowContext(ctx, `
		SELECT payroll_wallet_user_id, name
		FROM business_companies
		WHERE company_id = $1
	`, req.CompanyID).Scan(&payrollWallet, &companyName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "company not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load company"})
		return
	}
	if err := s.ensureWallet(ctx, payrollWallet); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare company wallet: " + err.Error()})
		return
	}

	status := "issued"
	transfer := map[string]any{}
	if req.AutoSettle && req.BuyerUserID != "" {
		if err := s.ensureWallet(ctx, req.BuyerUserID); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare buyer wallet: " + err.Error()})
			return
		}
		transfer, err = s.payTransfer(ctx, req.BuyerUserID, payrollWallet, centsToMoney(totalCents))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invoice payment failed: " + err.Error()})
			return
		}
		status = "paid"
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO business_invoices (
			invoice_id, company_id, buyer_user_id, description,
			subtotal_cents, tax_cents, freight_cents, total_cents,
			status, payment_ref, external_number, transfer
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb
		)
		ON CONFLICT (invoice_id) DO UPDATE SET
			company_id = EXCLUDED.company_id,
			buyer_user_id = EXCLUDED.buyer_user_id,
			description = EXCLUDED.description,
			subtotal_cents = EXCLUDED.subtotal_cents,
			tax_cents = EXCLUDED.tax_cents,
			freight_cents = EXCLUDED.freight_cents,
			total_cents = EXCLUDED.total_cents,
			status = EXCLUDED.status,
			payment_ref = EXCLUDED.payment_ref,
			external_number = EXCLUDED.external_number,
			transfer = EXCLUDED.transfer,
			updated_at = NOW()
	`, req.InvoiceID, req.CompanyID, req.BuyerUserID, req.Description,
		subtotalCents, taxCents, freightCents, totalCents,
		status, req.PaymentRef, req.ExternalNumber, mustJSON(transfer),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save invoice"})
		return
	}

	if status == "paid" {
		s.emitPurchaseEvent(ctx, map[string]any{
			"source":         "business",
			"order_id":       req.InvoiceID,
			"buyer_user_id":  req.BuyerUserID,
			"seller_user_id": payrollWallet,
			"currency":       "BRL",
			"gross_cents":    totalCents,
			"fee_cents":      0,
			"net_cents":      totalCents,
			"description":    fmt.Sprintf("Nota fiscal %s - %s", req.InvoiceID, companyName),
		})
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":     status,
		"invoice_id": req.InvoiceID,
		"company_id": req.CompanyID,
		"pricing": map[string]any{
			"subtotal": centsToMoney(subtotalCents),
			"tax":      centsToMoney(taxCents),
			"freight":  centsToMoney(freightCents),
			"total":    centsToMoney(totalCents),
		},
		"transfer": transfer,
	})
}

func (s *server) handleGetInvoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] != "invoices" {
		http.NotFound(w, r)
		return
	}
	invoiceID := strings.TrimSpace(parts[2])
	if invoiceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid invoice id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `
		SELECT invoice_id, company_id, buyer_user_id, description,
			subtotal_cents, tax_cents, freight_cents, total_cents,
			status, payment_ref, external_number, created_at
		FROM business_invoices
		WHERE invoice_id = $1
	`, invoiceID)
	var companyID, buyerID, description, status, paymentRef, externalNumber string
	var subtotal, tax, freight, total int64
	var createdAt time.Time
	if err := row.Scan(&invoiceID, &companyID, &buyerID, &description, &subtotal, &tax, &freight, &total, &status, &paymentRef, &externalNumber, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "invoice not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load invoice"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"invoice_id":       invoiceID,
		"company_id":       companyID,
		"buyer_user_id":    buyerID,
		"description":      description,
		"subtotal":         centsToMoney(subtotal),
		"tax":              centsToMoney(tax),
		"freight":          centsToMoney(freight),
		"total":            centsToMoney(total),
		"status":           status,
		"payment_ref":      paymentRef,
		"external_number":  externalNumber,
		"created_at":       createdAt,
	})
}

func (s *server) handleSyncInventoryPlace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req syncInventoryRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.CompanyID = normalizeID(req.CompanyID)
	if req.CompanyID == "" || len(req.Items) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "company_id and items are required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	var placeSellerID, city, state, companyName string
	err := s.db.QueryRowContext(ctx, `
		SELECT place_seller_id, city, state, name
		FROM business_companies
		WHERE company_id = $1
	`, req.CompanyID).Scan(&placeSellerID, &city, &state, &companyName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "company not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load company"})
		return
	}
	if err := s.upsertPlaceSeller(ctx, placeSellerID, companyName, city, state); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to ensure seller in place: " + err.Error()})
		return
	}

	warnings := []string{}
	synced := 0
	for _, item := range req.Items {
		itemID := normalizeID(item.ItemID)
		title := strings.TrimSpace(item.Title)
		category := normalizeText(item.Category)
		price := strings.TrimSpace(item.Price)
		sku := strings.TrimSpace(item.SKU)
		stockQty := item.StockQty
		itemCity := normalizeText(item.City)
		if itemCity == "" {
			itemCity = city
		}
		if itemID == "" {
			itemID = fmt.Sprintf("%s-item-%d", req.CompanyID, time.Now().UTC().UnixNano())
		}
		if title == "" || price == "" {
			warnings = append(warnings, "item skipped: title and price are required")
			continue
		}
		if stockQty <= 0 {
			stockQty = 1
		}
		payload := map[string]any{
			"item_id":   itemID,
			"seller_id": placeSellerID,
			"title":     title,
			"category":  category,
			"city":      itemCity,
			"price":     price,
			"stock_qty": stockQty,
			"sku":       sku,
		}
		_, statusCode, rawBody, err := s.callJSON(ctx, http.MethodPost, s.placeBaseURL+"/v1/items", payload, nil)
		if err != nil {
			warnings = append(warnings, "place item sync failed: "+err.Error())
			continue
		}
		if statusCode < 200 || statusCode > 299 {
			warnings = append(warnings, fmt.Sprintf("place item sync status=%d body=%s", statusCode, rawBody))
			continue
		}
		synced++
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "synced", "company_id": req.CompanyID, "synced_count": synced, "warnings": warnings})
}

func (s *server) handleProcessPayroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req processPayrollRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.CompanyID = normalizeID(req.CompanyID)
	req.BatchID = strings.TrimSpace(req.BatchID)
	if req.CompanyID == "" || len(req.Items) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "company_id and items are required"})
		return
	}
	if req.BatchID == "" {
		req.BatchID = fmt.Sprintf("payroll-%d", time.Now().UTC().UnixNano())
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	var payrollWallet string
	err := s.db.QueryRowContext(ctx, `
		SELECT payroll_wallet_user_id
		FROM business_companies
		WHERE company_id = $1
	`, req.CompanyID).Scan(&payrollWallet)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "company not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load company"})
		return
	}
	if err := s.ensureWallet(ctx, payrollWallet); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to prepare payroll wallet: " + err.Error()})
		return
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start payroll tx"})
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO business_payroll_batches (batch_id, company_id, payroll_wallet_user_id, status)
		VALUES ($1,$2,$3,'processing')
	`, req.BatchID, req.CompanyID, payrollWallet)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "batch_id already exists or failed to create batch"})
		return
	}

	processed := 0
	totalCents := int64(0)
	warnings := []string{}
	for _, line := range req.Items {
		employee := normalizeID(line.EmployeeUserID)
		if employee == "" {
			warnings = append(warnings, "line skipped: employee_user_id required")
			continue
		}
		amountCents, err := parseMoneyToCents(line.AmountBRL)
		if err != nil || amountCents <= 0 {
			warnings = append(warnings, "line skipped: invalid amount_brl for "+employee)
			continue
		}
		if err := s.ensureWallet(ctx, employee); err != nil {
			warnings = append(warnings, "line skipped: wallet error for "+employee+": "+err.Error())
			continue
		}
		transfer, err := s.payTransfer(ctx, payrollWallet, employee, centsToMoney(amountCents))
		if err != nil {
			warnings = append(warnings, "line skipped: transfer failed for "+employee+": "+err.Error())
			continue
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO business_payroll_items (
				batch_id, employee_user_id, amount_cents, reference, transfer, status
			) VALUES (
				$1,$2,$3,$4,$5::jsonb,'paid'
			)
		`, req.BatchID, employee, amountCents, strings.TrimSpace(line.Reference), mustJSON(transfer))
		if err != nil {
			warnings = append(warnings, "line skipped: failed to record item for "+employee)
			continue
		}
		processed++
		totalCents += amountCents
	}
	batchStatus := "completed"
	if processed == 0 {
		batchStatus = "failed"
	}
	_, _ = tx.ExecContext(ctx, `
		UPDATE business_payroll_batches
		SET status = $2,
			total_items = $3,
			total_amount_cents = $4,
			updated_at = NOW()
		WHERE batch_id = $1
	`, req.BatchID, batchStatus, processed, totalCents)

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to commit payroll batch"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       batchStatus,
		"batch_id":     req.BatchID,
		"processed":    processed,
		"total_amount": centsToMoney(totalCents),
		"warnings":     warnings,
	})
}

func (s *server) upsertPlaceSeller(ctx context.Context, sellerID, name, city, state string) error {
	payload := map[string]any{
		"seller_id": sellerID,
		"name":      name,
		"city":      city,
		"state":     state,
	}
	_, statusCode, rawBody, err := s.callJSON(ctx, http.MethodPost, s.placeBaseURL+"/v1/sellers", payload, nil)
	if err != nil {
		return err
	}
	if statusCode < 200 || statusCode > 299 {
		return fmt.Errorf("place seller sync status %d: %s", statusCode, rawBody)
	}
	return nil
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
		log.Printf("document_engine_emit_failed source=business error=%v status=%d", err, statusCode)
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

func (s *server) setShieldPolicy(userID string, blocked bool, reason string, until time.Time) shieldPolicy {
	s.shieldMu.Lock()
	defer s.shieldMu.Unlock()
	if !blocked {
		delete(s.shieldPolicies, userID)
		return shieldPolicy{Blocked: false, UpdatedAt: time.Now().UTC()}
	}
	policy := shieldPolicy{
		Blocked:   true,
		Reason:    strings.TrimSpace(reason),
		Until:     until,
		UpdatedAt: time.Now().UTC(),
	}
	s.shieldPolicies[userID] = policy
	return policy
}

func (s *server) getShieldPolicy(userID string) shieldPolicy {
	s.shieldMu.Lock()
	defer s.shieldMu.Unlock()
	policy, ok := s.shieldPolicies[userID]
	if !ok || !policy.Blocked {
		return shieldPolicy{Blocked: false}
	}
	if !policy.Until.IsZero() && !time.Now().UTC().Before(policy.Until) {
		delete(s.shieldPolicies, userID)
		return shieldPolicy{Blocked: false}
	}
	return policy
}

func (s *server) handleShieldPolicy(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/business/policy/shield" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		token := strings.TrimSpace(r.Header.Get("x-shield-token"))
		if s.shieldToken != "" && token != s.shieldToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid shield token"})
			return
		}
		var req updateShieldRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		req.UserID = normalizeID(req.UserID)
		if req.UserID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
			return
		}
		until := time.Time{}
		if strings.TrimSpace(req.Until) != "" {
			parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.Until))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "until must be RFC3339"})
				return
			}
			until = parsed.UTC()
		}
		if req.Blocked && until.IsZero() {
			until = time.Now().UTC().Add(48 * time.Hour)
		}
		policy := s.setShieldPolicy(req.UserID, req.Blocked, req.Reason, until)
		writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "user_id": req.UserID, "policy": policy})
	case http.MethodGet:
		userID := normalizeID(r.URL.Query().Get("user_id"))
		if userID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
			return
		}
		policy := s.getShieldPolicy(userID)
		writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "policy": policy})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) handleDispatchNotification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/business/notifications/dispatch" {
		http.NotFound(w, r)
		return
	}
	var req dispatchNotificationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.UserID = normalizeID(req.UserID)
	req.Channel = normalizeText(req.Channel)
	req.Title = strings.TrimSpace(req.Title)
	req.Message = strings.TrimSpace(req.Message)
	if req.UserID == "" || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and message are required"})
		return
	}
	if req.Channel == "" {
		req.Channel = "work"
	}
	if req.NotificationID == "" {
		req.NotificationID = fmt.Sprintf("biz-notif-%d", time.Now().UTC().UnixNano())
	}
	blocked := false
	reason := ""
	until := time.Time{}
	if req.Channel == "work" {
		policy := s.getShieldPolicy(req.UserID)
		blocked = policy.Blocked
		reason = policy.Reason
		until = policy.Until
	}
	if blocked {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":          "blocked",
			"notification_id": req.NotificationID,
			"user_id":         req.UserID,
			"channel":         req.Channel,
			"reason":          reason,
			"until":           until,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "delivered",
		"notification_id": req.NotificationID,
		"user_id":         req.UserID,
		"channel":         req.Channel,
		"title":           req.Title,
		"message":         req.Message,
		"context":         req.Context,
		"delivered_at":    time.Now().UTC(),
	})
}

func initSchema(db *sql.DB) error {
	ddl := `
CREATE TABLE IF NOT EXISTS business_companies (
	company_id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	document_number TEXT NOT NULL DEFAULT '',
	city TEXT NOT NULL,
	state TEXT NOT NULL,
	place_seller_id TEXT NOT NULL,
	payroll_wallet_user_id TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS business_invoices (
	invoice_id TEXT PRIMARY KEY,
	company_id TEXT NOT NULL REFERENCES business_companies(company_id),
	buyer_user_id TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	subtotal_cents BIGINT NOT NULL,
	tax_cents BIGINT NOT NULL,
	freight_cents BIGINT NOT NULL,
	total_cents BIGINT NOT NULL,
	status TEXT NOT NULL,
	payment_ref TEXT NOT NULL DEFAULT '',
	external_number TEXT NOT NULL DEFAULT '',
	transfer JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_business_invoices_company_created ON business_invoices(company_id, created_at DESC);

CREATE TABLE IF NOT EXISTS business_payroll_batches (
	batch_id TEXT PRIMARY KEY,
	company_id TEXT NOT NULL REFERENCES business_companies(company_id),
	payroll_wallet_user_id TEXT NOT NULL,
	total_items INT NOT NULL DEFAULT 0,
	total_amount_cents BIGINT NOT NULL DEFAULT 0,
	status TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS business_payroll_items (
	id BIGSERIAL PRIMARY KEY,
	batch_id TEXT NOT NULL REFERENCES business_payroll_batches(batch_id),
	employee_user_id TEXT NOT NULL,
	amount_cents BIGINT NOT NULL,
	reference TEXT NOT NULL DEFAULT '',
	transfer JSONB NOT NULL DEFAULT '{}'::jsonb,
	status TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_business_payroll_items_batch ON business_payroll_items(batch_id);
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

func parseMoneyToCentsDefault(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	return parseMoneyToCents(raw)
}

func centsToMoney(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
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
		w.Header().Set("X-Service", "nexora-business")
		next.ServeHTTP(w, r)
	})
}
