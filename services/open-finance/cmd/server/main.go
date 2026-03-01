package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type server struct {
	client     *mongo.Client
	collection *mongo.Collection
}

type consentRequest struct {
	UserID string   `json:"user_id"`
	Banks  []string `json:"banks"`
}

type consentDocument struct {
	UserID    string    `bson:"user_id"`
	Banks     []string  `bson:"banks"`
	UpdatedAt time.Time `bson:"updated_at"`
}

type bankBalance struct {
	BankCode         string `json:"bank_code"`
	Currency         string `json:"currency"`
	AvailableBalance string `json:"available_balance"`
	AccountType      string `json:"account_type"`
}

func main() {
	port := envOrDefault("PORT", "8083")
	mongoURI := envOrDefault("MONGODB_URI", "mongodb://localhost:27017")
	dbName := envOrDefault("OPEN_FINANCE_DB", "nexora_open_finance")

	client, collection, err := initMongo(mongoURI, dbName)
	if err != nil {
		log.Fatalf("failed to initialize mongodb: %v", err)
	}

	s := &server{
		client:     client,
		collection: collection,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/consents", s.handleConsents)
	mux.HandleFunc("/v1/users/", s.handleUserRoutes)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("open-finance listening on :%s", port)
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
	if err := s.client.Ping(ctx, nil); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "open-finance"})
}

func (s *server) handleConsents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/consents" {
		http.NotFound(w, r)
		return
	}

	var req consentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if len(req.Banks) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "banks is required"})
		return
	}

	normalizedBanks := normalizeBanks(req.Banks)
	doc := bson.M{
		"user_id":    req.UserID,
		"banks":      normalizedBanks,
		"updated_at": time.Now().UTC(),
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	_, err := s.collection.UpdateOne(
		ctx,
		bson.M{"user_id": req.UserID},
		bson.M{"$set": doc},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save consent"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id": req.UserID,
		"banks":   normalizedBanks,
		"status":  "consent_saved",
	})
}

func (s *server) handleUserRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "users" || parts[3] != "external-balances" {
		http.NotFound(w, r)
		return
	}
	userID := strings.TrimSpace(parts[2])
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var consent consentDocument
	err := s.collection.FindOne(ctx, bson.M{"user_id": userID}).Decode(&consent)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "consent not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load consent"})
		return
	}

	balances := make([]bankBalance, 0, len(consent.Banks))
	for _, bank := range consent.Banks {
		balances = append(balances, buildDeterministicBalance(userID, bank))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":  userID,
		"source":   "open-finance-stub",
		"balances": balances,
	})
}

func initMongo(uri, dbName string) (*mongo.Client, *mongo.Collection, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, nil, err
	}

	collection := client.Database(dbName).Collection("consents")
	_, err = collection.Indexes().CreateOne(
		ctx,
		mongo.IndexModel{
			Keys:    bson.D{{Key: "user_id", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("uniq_user_id"),
		},
	)
	if err != nil {
		return nil, nil, err
	}

	return client, collection, nil
}

func normalizeBanks(banks []string) []string {
	dedup := make(map[string]struct{}, len(banks))
	result := make([]string, 0, len(banks))
	for _, bank := range banks {
		normalized := strings.ToUpper(strings.TrimSpace(bank))
		if normalized == "" {
			continue
		}
		if _, exists := dedup[normalized]; exists {
			continue
		}
		dedup[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func buildDeterministicBalance(userID, bank string) bankBalance {
	h := fnv.New64a()
	_, _ = h.Write([]byte(userID + ":" + bank))
	hashValue := h.Sum64()

	brlCents := int64(hashValue%5_000_000) + 50_000
	return bankBalance{
		BankCode:         bank,
		Currency:         "BRL",
		AvailableBalance: formatBRL(brlCents),
		AccountType:      "CHECKING",
	}
}

func formatBRL(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withServiceHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Service", "open-finance")
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
