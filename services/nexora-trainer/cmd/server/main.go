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
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type server struct {
	db         *sql.DB
	httpClient *http.Client
	lifeBase   string
}

type generatePlanRequest struct {
	UserID           string  `json:"user_id"`
	Goal             string  `json:"goal"`
	FatigueScore     float64 `json:"fatigue_score"`
	SleepHours       float64 `json:"sleep_hours"`
	RestingHeartRate int     `json:"resting_heart_rate"`
	ExperienceLevel  string  `json:"experience_level"`
	DaysAvailable    int     `json:"days_available"`
}

type metricsRequest struct {
	UserID       string  `json:"user_id"`
	FatigueScore float64 `json:"fatigue_score"`
	SleepHours   float64 `json:"sleep_hours"`
	Mood         string  `json:"mood"`
	Notes        string  `json:"notes"`
}

func main() {
	port := envOrDefault("PORT", "8096")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@postgres:5432/nexora_pay?sslmode=disable")
	lifeBase := strings.TrimRight(envOrDefault("NEXORA_LIFE_BASE_URL", "http://nexora-life:8095"), "/")

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
		httpClient: &http.Client{Timeout: 5 * time.Second},
		lifeBase:   lifeBase,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/trainer/prescriptions/generate", s.handleGeneratePrescription)
	mux.HandleFunc("/v1/trainer/prescriptions", s.handleListPrescriptions)
	mux.HandleFunc("/v1/trainer/metrics", s.handleMetrics)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       12 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      25 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-trainer listening on :%s", port)
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db_unavailable", "service": "nexora-trainer"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexora-trainer", "engine": "adaptive-fatigue-sleep"})
}

func (s *server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/trainer/metrics" {
		http.NotFound(w, r)
		return
	}
	var req metricsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.UserID = normalizeID(req.UserID)
	req.FatigueScore = clamp(req.FatigueScore, 0, 100)
	req.SleepHours = clamp(req.SleepHours, 0, 24)
	req.Mood = normalizeText(req.Mood)
	req.Notes = strings.TrimSpace(req.Notes)
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO trainer_metrics (user_id, fatigue_score, sleep_hours, mood, notes)
		VALUES ($1,$2,$3,$4,$5)
	`, req.UserID, req.FatigueScore, req.SleepHours, req.Mood, req.Notes)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save metrics"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"status": "saved", "user_id": req.UserID})
}

func (s *server) handleGeneratePrescription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/trainer/prescriptions/generate" {
		http.NotFound(w, r)
		return
	}
	var req generatePlanRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	req.UserID = normalizeID(req.UserID)
	req.Goal = normalizeText(req.Goal)
	req.ExperienceLevel = normalizeText(req.ExperienceLevel)
	req.FatigueScore = clamp(req.FatigueScore, 0, 100)
	req.SleepHours = clamp(req.SleepHours, 0, 24)
	if req.DaysAvailable <= 0 {
		req.DaysAvailable = 4
	}
	if req.DaysAvailable > 7 {
		req.DaysAvailable = 7
	}
	if req.Goal == "" {
		req.Goal = "performance"
	}
	if req.ExperienceLevel == "" {
		req.ExperienceLevel = "intermediate"
	}
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	restingHR := req.RestingHeartRate
	if restingHR <= 0 {
		if latest, err := s.fetchLatestHeartRate(r.Context(), req.UserID); err == nil && latest > 0 {
			restingHR = latest
		}
	}
	if restingHR <= 0 {
		restingHR = 72
	}

	readiness := computeReadiness(req.FatigueScore, req.SleepHours, restingHR)
	intensity, sessionMinutes, recoveryFocus := chooseTrainingShape(readiness, req.ExperienceLevel)
	exercises := chooseExercises(req.Goal, intensity)

	plan := map[string]any{
		"user_id":           req.UserID,
		"goal":              req.Goal,
		"experience_level":  req.ExperienceLevel,
		"fatigue_score":     req.FatigueScore,
		"sleep_hours":       req.SleepHours,
		"resting_heart_rate": restingHR,
		"readiness_score":   readiness,
		"intensity":         intensity,
		"days_available":    req.DaysAvailable,
		"sessions_per_week": minInt(req.DaysAvailable, recommendSessions(readiness, req.DaysAvailable)),
		"session_minutes":   sessionMinutes,
		"recovery_focus":    recoveryFocus,
		"exercises":         exercises,
		"generated_at":      time.Now().UTC(),
	}

	planJSON, _ := json.Marshal(plan)
	prescriptionID := fmt.Sprintf("trainer-plan-%d", time.Now().UTC().UnixNano())
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO trainer_prescriptions (
			prescription_id, user_id, goal, fatigue_score, sleep_hours,
			resting_heart_rate, readiness_score, intensity, sessions_per_week,
			session_minutes, plan
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)
	`, prescriptionID, req.UserID, req.Goal, req.FatigueScore, req.SleepHours,
		restingHR, readiness, intensity, plan["sessions_per_week"], sessionMinutes, string(planJSON))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save prescription"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"status": "generated", "prescription_id": prescriptionID, "plan": plan})
}

func (s *server) handleListPrescriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/trainer/prescriptions" {
		http.NotFound(w, r)
		return
	}
	userID := normalizeID(r.URL.Query().Get("user_id"))
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	limit := intOrDefault(r.URL.Query().Get("limit"), 20)
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT prescription_id, goal, intensity, readiness_score, sessions_per_week, session_minutes, created_at, plan
		FROM trainer_prescriptions
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list prescriptions"})
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var prescriptionID, goal, intensity string
		var readiness float64
		var sessionsPerWeek, sessionMinutes int
		var createdAt time.Time
		var planRaw string
		if err := rows.Scan(&prescriptionID, &goal, &intensity, &readiness, &sessionsPerWeek, &sessionMinutes, &createdAt, &planRaw); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan prescriptions"})
			return
		}
		var plan any
		_ = json.Unmarshal([]byte(planRaw), &plan)
		items = append(items, map[string]any{
			"prescription_id":   prescriptionID,
			"goal":              goal,
			"intensity":         intensity,
			"readiness_score":   readiness,
			"sessions_per_week": sessionsPerWeek,
			"session_minutes":   sessionMinutes,
			"created_at":        createdAt,
			"plan":              plan,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) fetchLatestHeartRate(parent context.Context, userID string) (int, error) {
	if s.lifeBase == "" {
		return 0, errors.New("life service disabled")
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	endpoint := fmt.Sprintf("%s/v1/iot/heartbeat/latest?user_id=%s", s.lifeBase, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, fmt.Errorf("life status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, err
	}
	bpm, _ := parsed["bpm"].(float64)
	return int(bpm), nil
}

func computeReadiness(fatigue, sleepHours float64, restingHR int) float64 {
	score := 100.0
	score -= fatigue * 0.62
	score += (sleepHours - 7.0) * 8.5
	if restingHR > 72 {
		score -= float64(restingHR-72) * 0.85
	}
	if restingHR < 58 {
		score += float64(58-restingHR) * 0.25
	}
	return clamp(math.Round(score*10)/10, 0, 100)
}

func chooseTrainingShape(readiness float64, experience string) (string, int, string) {
	experienceBoost := map[string]int{"beginner": -6, "intermediate": 0, "advanced": 6}[experience]
	adjusted := readiness + float64(experienceBoost)
	switch {
	case adjusted < 40:
		return "low", 30, "mobilidade, respiracao e sono"
	case adjusted < 70:
		return "moderate", 42, "recuperacao ativa e hidratacao"
	default:
		return "high", 55, "deload estrategico e monitoramento cardiaco"
	}
}

func recommendSessions(readiness float64, daysAvailable int) int {
	target := 3
	switch {
	case readiness < 35:
		target = 2
	case readiness < 65:
		target = 3
	default:
		target = 5
	}
	if target > daysAvailable {
		target = daysAvailable
	}
	if target < 1 {
		target = 1
	}
	return target
}

func chooseExercises(goal, intensity string) []map[string]any {
	library := map[string][]string{
		"performance":    {"agachamento", "supino", "remada", "sprint intervalado", "core anti-rotacao"},
		"weight-loss":    {"caminhada inclinada", "circuito funcional", "bike hiit", "prancha", "saltos controlados"},
		"hypertrophy":    {"agachamento frontal", "levantamento terra romeno", "desenvolvimento", "puxada", "abdominal cable"},
		"mobility":       {"mobilidade toracica", "alongamento quadril", "ponte glutea", "respiracao diafragmatica", "foam roll"},
		"rehabilitation": {"ponte unilateral", "bird-dog", "isometria parede", "caminhada leve", "controle escapular"},
	}
	items, ok := library[goal]
	if !ok {
		items = library["performance"]
	}
	density := "normal"
	switch intensity {
	case "low":
		density = "baixa"
	case "moderate":
		density = "media"
	case "high":
		density = "alta"
	}
	out := make([]map[string]any, 0, len(items))
	for i, ex := range items {
		out = append(out, map[string]any{
			"order":   i + 1,
			"name":    ex,
			"density": density,
		})
	}
	return out
}

func initSchema(db *sql.DB) error {
	ddl := `
CREATE TABLE IF NOT EXISTS trainer_metrics (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT NOT NULL,
	fatigue_score DOUBLE PRECISION NOT NULL,
	sleep_hours DOUBLE PRECISION NOT NULL,
	mood TEXT NOT NULL DEFAULT '',
	notes TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_trainer_metrics_user_created ON trainer_metrics(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS trainer_prescriptions (
	prescription_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	goal TEXT NOT NULL,
	fatigue_score DOUBLE PRECISION NOT NULL,
	sleep_hours DOUBLE PRECISION NOT NULL,
	resting_heart_rate INT NOT NULL,
	readiness_score DOUBLE PRECISION NOT NULL,
	intensity TEXT NOT NULL,
	sessions_per_week INT NOT NULL,
	session_minutes INT NOT NULL,
	plan JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_trainer_prescriptions_user_created ON trainer_prescriptions(user_id, created_at DESC);
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

func clamp(v, minV, maxV float64) float64 {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func intOrDefault(raw string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withServiceHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-service", "nexora-trainer")
		next.ServeHTTP(w, r)
	})
}

func mustSortStrings(items []string) []string {
	out := make([]string, len(items))
	copy(out, items)
	sort.Strings(out)
	return out
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
