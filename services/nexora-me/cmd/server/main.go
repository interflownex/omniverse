package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type enrollment struct {
	UserID    string    `json:"user_id"`
	FaceHash  string    `json:"face_hash"`
	CreatedAt time.Time `json:"created_at"`
}

type challenge struct {
	ID        string    `json:"challenge_id"`
	UserID    string    `json:"user_id"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
}

type server struct {
	mu                sync.RWMutex
	enrollments       map[string]enrollment
	challenges        map[string]challenge
	jwtSecret         []byte
	jwtTTL            time.Duration
	livenessThreshold float64
}

type enrollRequest struct {
	UserID   string `json:"user_id"`
	FaceHash string `json:"face_hash"`
}

type challengeRequest struct {
	UserID string `json:"user_id"`
}

type verifyRequest struct {
	ChallengeID   string  `json:"challenge_id"`
	FaceHash      string  `json:"face_hash"`
	LivenessScore float64 `json:"liveness_score"`
	DeviceID      string  `json:"device_id"`
}

func main() {
	port := envOrDefault("PORT", "8081")
	jwtSecret := envOrDefault("JWT_SECRET", "change-me-now")
	ttlMinutes := envAsInt("JWT_TTL_MINUTES", 30)
	livenessThreshold := envAsFloat("LIVENESS_THRESHOLD", 0.85)

	s := &server{
		enrollments:       make(map[string]enrollment),
		challenges:        make(map[string]challenge),
		jwtSecret:         []byte(jwtSecret),
		jwtTTL:            time.Duration(ttlMinutes) * time.Minute,
		livenessThreshold: livenessThreshold,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/users/enroll-face", s.handleEnrollFace)
	mux.HandleFunc("/v1/auth/challenge", s.handleChallenge)
	mux.HandleFunc("/v1/auth/biometric/verify", s.handleVerify)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withJSONContentType(mux),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-me listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "nexora-me"})
}

func (s *server) handleEnrollFace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/users/enroll-face" {
		http.NotFound(w, r)
		return
	}

	var req enrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	req.FaceHash = strings.TrimSpace(req.FaceHash)
	if req.UserID == "" || req.FaceHash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and face_hash are required"})
		return
	}

	s.mu.Lock()
	s.enrollments[req.UserID] = enrollment{
		UserID:    req.UserID,
		FaceHash:  req.FaceHash,
		CreatedAt: time.Now().UTC(),
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id": req.UserID,
		"status":  "enrolled",
	})
}

func (s *server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/auth/challenge" {
		http.NotFound(w, r)
		return
	}

	var req challengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	s.mu.RLock()
	_, enrolled := s.enrollments[req.UserID]
	s.mu.RUnlock()
	if !enrolled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not enrolled"})
		return
	}

	chID, err := randomHex(16)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate challenge"})
		return
	}
	nonce, err := randomHex(20)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate nonce"})
		return
	}

	ch := challenge{
		ID:        chID,
		UserID:    req.UserID,
		Nonce:     nonce,
		ExpiresAt: time.Now().UTC().Add(2 * time.Minute),
		Used:      false,
	}

	s.mu.Lock()
	s.challenges[chID] = ch
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"challenge_id": ch.ID,
		"nonce":        ch.Nonce,
		"expires_at":   ch.ExpiresAt,
	})
}

func (s *server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/auth/biometric/verify" {
		http.NotFound(w, r)
		return
	}

	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.ChallengeID = strings.TrimSpace(req.ChallengeID)
	req.FaceHash = strings.TrimSpace(req.FaceHash)
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	if req.ChallengeID == "" || req.FaceHash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "challenge_id and face_hash are required"})
		return
	}

	s.mu.Lock()
	ch, ok := s.challenges[req.ChallengeID]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid challenge"})
		return
	}
	if ch.Used {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "challenge already used"})
		return
	}
	if time.Now().UTC().After(ch.ExpiresAt) {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "challenge expired"})
		return
	}
	enrollment, enrolled := s.enrollments[ch.UserID]
	if !enrolled {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "user not enrolled"})
		return
	}
	if req.LivenessScore < s.livenessThreshold {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "liveness check failed"})
		return
	}
	if req.FaceHash != enrollment.FaceHash {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "face mismatch"})
		return
	}

	ch.Used = true
	s.challenges[ch.ID] = ch
	s.mu.Unlock()

	token, expiresAt, err := s.issueJWT(ch.UserID, ch.ID, req.DeviceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to issue token"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_at":   expiresAt,
		"user_id":      ch.UserID,
		"auth_mode":    "passwordless_face_liveness",
	})
}

func (s *server) issueJWT(userID, challengeID, deviceID string) (string, time.Time, error) {
	now := time.Now().UTC()
	exp := now.Add(s.jwtTTL)
	claims := jwt.MapClaims{
		"iss":          "nexora-me",
		"sub":          userID,
		"aud":          "nexora-platform",
		"iat":          now.Unix(),
		"exp":          exp.Unix(),
		"auth_method":  "face_liveness",
		"challenge_id": challengeID,
	}
	if deviceID != "" {
		claims["device_id"] = deviceID
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	return signed, exp, err
}

func randomHex(lengthBytes int) (string, error) {
	buf := make([]byte, lengthBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withJSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Service", "nexora-me")
		next.ServeHTTP(w, r)
	})
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envAsInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envAsFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
