package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type server struct {
	db           *sql.DB
	httpClient   *http.Client
	personaBase  string
	personaToken string
}

type appointmentRequest struct {
	AppointmentID string `json:"appointment_id"`
	UserID        string `json:"user_id"`
	DoctorID      string `json:"doctor_id"`
	Specialty     string `json:"specialty"`
	Mode          string `json:"mode"`
	ScheduledAt   string `json:"scheduled_at"`
	Notes         string `json:"notes"`
}

type sosRequest struct {
	EventID      string  `json:"event_id"`
	UserID       string  `json:"user_id"`
	Reason       string  `json:"reason"`
	ContactPhone string  `json:"contact_phone"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
}

type heartbeatSimRequest struct {
	UserID      string  `json:"user_id"`
	DeviceID    string  `json:"device_id"`
	Activity    string  `json:"activity"`
	StressHint  float64 `json:"stress_hint"`
	ManualBPM   int     `json:"manual_bpm"`
	SensorNoise int     `json:"sensor_noise"`
}

func main() {
	port := envOrDefault("PORT", "8095")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@postgres:5432/nexora_pay?sslmode=disable")
	personaBase := strings.TrimRight(envOrDefault("NEXORA_PERSONA_BASE_URL", "http://persona-burnout:8098"), "/")
	personaToken := strings.TrimSpace(envOrDefault("NEXORA_PERSONA_TOKEN", "persona-ai-token"))

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
		db:           db,
		httpClient:   &http.Client{Timeout: 8 * time.Second},
		personaBase:  personaBase,
		personaToken: personaToken,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/telemedicine/appointments", s.handleAppointments)
	mux.HandleFunc("/v1/sos/trigger", s.handleSOS)
	mux.HandleFunc("/v1/iot/heartbeat/simulate", s.handleHeartbeatSimulate)
	mux.HandleFunc("/v1/iot/heartbeat/latest", s.handleHeartbeatLatest)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-life listening on :%s", port)
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db_unavailable", "service": "nexora-life"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexora-life", "features": []string{"telemedicine", "sos", "iot-heartbeat"}})
}

func (s *server) handleAppointments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createAppointment(w, r)
	case http.MethodGet:
		s.listAppointments(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) createAppointment(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/telemedicine/appointments" {
		http.NotFound(w, r)
		return
	}
	var req appointmentRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.AppointmentID = strings.TrimSpace(req.AppointmentID)
	req.UserID = normalizeID(req.UserID)
	req.DoctorID = normalizeID(req.DoctorID)
	req.Specialty = normalizeText(req.Specialty)
	req.Mode = normalizeText(req.Mode)
	req.Notes = strings.TrimSpace(req.Notes)
	if req.UserID == "" || req.DoctorID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and doctor_id are required"})
		return
	}
	if req.Specialty == "" {
		req.Specialty = "clinica-geral"
	}
	if req.Mode == "" {
		req.Mode = "video"
	}
	if req.AppointmentID == "" {
		req.AppointmentID = fmt.Sprintf("life-apt-%d", time.Now().UTC().UnixNano())
	}
	scheduledAt := time.Now().UTC().Add(5 * time.Minute)
	if strings.TrimSpace(req.ScheduledAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ScheduledAt))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scheduled_at must be RFC3339"})
			return
		}
		scheduledAt = parsed.UTC()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO life_appointments (
			appointment_id, user_id, doctor_id, specialty, mode, scheduled_at, notes, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,'scheduled')
	`, req.AppointmentID, req.UserID, req.DoctorID, req.Specialty, req.Mode, scheduledAt, req.Notes)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "failed to create appointment"})
		return
	}

	roomURL := fmt.Sprintf("https://telemed.nexora.local/session/%s", req.AppointmentID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"status":         "scheduled",
		"appointment_id": req.AppointmentID,
		"user_id":        req.UserID,
		"doctor_id":      req.DoctorID,
		"specialty":      req.Specialty,
		"mode":           req.Mode,
		"scheduled_at":   scheduledAt,
		"room_url":       roomURL,
	})
}

func (s *server) listAppointments(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/telemedicine/appointments" {
		http.NotFound(w, r)
		return
	}
	userID := normalizeID(r.URL.Query().Get("user_id"))
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
		SELECT appointment_id, user_id, doctor_id, specialty, mode, scheduled_at, status, notes, created_at
		FROM life_appointments
		WHERE ($1 = '' OR user_id = $1)
		ORDER BY scheduled_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list appointments"})
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var appointmentID, user, doctor, specialty, mode, status, notes string
		var scheduledAt, createdAt time.Time
		if err := rows.Scan(&appointmentID, &user, &doctor, &specialty, &mode, &scheduledAt, &status, &notes, &createdAt); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan appointments"})
			return
		}
		items = append(items, map[string]any{
			"appointment_id": appointmentID,
			"user_id":        user,
			"doctor_id":      doctor,
			"specialty":      specialty,
			"mode":           mode,
			"scheduled_at":   scheduledAt,
			"status":         status,
			"notes":          notes,
			"created_at":     createdAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) handleSOS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/sos/trigger" {
		http.NotFound(w, r)
		return
	}
	var req sosRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.EventID = strings.TrimSpace(req.EventID)
	req.UserID = normalizeID(req.UserID)
	req.Reason = strings.TrimSpace(req.Reason)
	req.ContactPhone = strings.TrimSpace(req.ContactPhone)
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if req.EventID == "" {
		req.EventID = fmt.Sprintf("life-sos-%d", time.Now().UTC().UnixNano())
	}
	if req.Reason == "" {
		req.Reason = "sos acionado pelo usuario"
	}
	severity := classifySOSSeverity(req.Reason)

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO life_sos_events (
			event_id, user_id, reason, contact_phone, latitude, longitude, severity, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,'open')
	`, req.EventID, req.UserID, req.Reason, req.ContactPhone, req.Latitude, req.Longitude, severity)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to register sos"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":      "open",
		"event_id":    req.EventID,
		"user_id":     req.UserID,
		"severity":    severity,
		"dispatched":  true,
		"next_steps":  []string{"acionar contato de emergencia", "abrir canal medico imediato", "monitorar batimentos"},
		"created_at":  time.Now().UTC(),
		"contact_phone": req.ContactPhone,
	})
}

func (s *server) handleHeartbeatSimulate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/iot/heartbeat/simulate" {
		http.NotFound(w, r)
		return
	}
	var req heartbeatSimRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.UserID = normalizeID(req.UserID)
	req.DeviceID = normalizeID(req.DeviceID)
	req.Activity = normalizeText(req.Activity)
	req.StressHint = clampFloat(req.StressHint, 0, 1)
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if req.DeviceID == "" {
		req.DeviceID = "iot-simulator"
	}
	if req.Activity == "" {
		req.Activity = "rest"
	}

	bpm := req.ManualBPM
	if bpm <= 0 {
		computed, err := simulateHeartRate(req.Activity, req.StressHint, req.SensorNoise)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to simulate heartbeat"})
			return
		}
		bpm = computed
	}
	if bpm < 35 {
		bpm = 35
	}
	if bpm > 220 {
		bpm = 220
	}

	stressScore := calculateStressScore(bpm, req.StressHint)
	riskLevel := classifyRisk(stressScore, bpm)
	mood := classifyMood(stressScore, bpm)
	measuredAt := time.Now().UTC()
	readingID := fmt.Sprintf("life-hr-%d", measuredAt.UnixNano())

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO life_heartbeats (
			reading_id, user_id, device_id, activity, bpm, stress_score, risk_level, mood, measured_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, readingID, req.UserID, req.DeviceID, req.Activity, bpm, stressScore, riskLevel, mood, measuredAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist heartbeat"})
		return
	}

	personaStatus := map[string]any{"status": "skipped"}
	if s.personaBase != "" {
		if out, err := s.emitToPersona(r.Context(), req.UserID, bpm, stressScore, measuredAt); err == nil {
			personaStatus = out
		} else {
			personaStatus = map[string]any{"status": "error", "message": err.Error()}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"reading_id":    readingID,
		"user_id":       req.UserID,
		"device_id":     req.DeviceID,
		"activity":      req.Activity,
		"bpm":           bpm,
		"stress_score":  stressScore,
		"risk_level":    riskLevel,
		"mood":          mood,
		"measured_at":   measuredAt,
		"persona_signal": personaStatus,
	})
}

func (s *server) handleHeartbeatLatest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/iot/heartbeat/latest" {
		http.NotFound(w, r)
		return
	}
	userID := normalizeID(r.URL.Query().Get("user_id"))
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `
		SELECT reading_id, user_id, device_id, activity, bpm, stress_score, risk_level, mood, measured_at
		FROM life_heartbeats
		WHERE user_id = $1
		ORDER BY measured_at DESC
		LIMIT 1
	`, userID)

	var readingID, user, deviceID, activity, riskLevel, mood string
	var bpm int
	var stressScore float64
	var measuredAt time.Time
	if err := row.Scan(&readingID, &user, &deviceID, &activity, &bpm, &stressScore, &riskLevel, &mood, &measuredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no heartbeat for user"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load heartbeat"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"reading_id":   readingID,
		"user_id":      user,
		"device_id":    deviceID,
		"activity":     activity,
		"bpm":          bpm,
		"stress_score": stressScore,
		"risk_level":   riskLevel,
		"mood":         mood,
		"measured_at":  measuredAt,
	})
}

func (s *server) emitToPersona(parent context.Context, userID string, bpm int, stressScore float64, measuredAt time.Time) (map[string]any, error) {
	if s.personaBase == "" {
		return map[string]any{"status": "disabled"}, nil
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	payload := map[string]any{
		"user_id":      userID,
		"bpm":          bpm,
		"stress_score": stressScore,
		"event_at":     measuredAt.Format(time.RFC3339),
		"source":       "nexora-life",
	}
	respBody, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.personaBase+"/v1/signals/heartbeat", payload, map[string]string{"x-persona-token": s.personaToken})
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("persona status %d: %s", status, rawBody)
	}
	mapped, _ := respBody.(map[string]any)
	if mapped == nil {
		mapped = map[string]any{"status": "ok"}
	}
	return mapped, nil
}

func simulateHeartRate(activity string, stressHint float64, sensorNoise int) (int, error) {
	baseMin, baseMax := int64(58), int64(72)
	switch activity {
	case "sleep":
		baseMin, baseMax = 42, 58
	case "walk", "walking":
		baseMin, baseMax = 82, 106
	case "run", "running":
		baseMin, baseMax = 128, 172
	case "workout", "training":
		baseMin, baseMax = 110, 158
	case "rest":
		baseMin, baseMax = 58, 72
	default:
		baseMin, baseMax = 64, 88
	}
	v, err := randomRange(baseMin, baseMax)
	if err != nil {
		return 0, err
	}
	stressBoost := int(math.Round(stressHint * 26))
	noise := sensorNoise
	if noise == 0 {
		r, err := randomRange(-4, 4)
		if err != nil {
			return 0, err
		}
		noise = int(r)
	}
	return int(v) + stressBoost + noise, nil
}

func randomRange(minV, maxV int64) (int64, error) {
	if maxV < minV {
		return minV, errors.New("invalid random range")
	}
	if maxV == minV {
		return minV, nil
	}
	delta := maxV - minV + 1
	n, err := crand.Int(crand.Reader, big.NewInt(delta))
	if err != nil {
		return 0, err
	}
	return minV + n.Int64(), nil
}

func calculateStressScore(bpm int, hint float64) float64 {
	base := (float64(bpm) - 60.0) / 100.0
	if base < 0 {
		base = 0
	}
	score := base*0.75 + hint*0.45
	return clampFloat(score, 0, 1)
}

func classifyRisk(stressScore float64, bpm int) string {
	switch {
	case bpm >= 170 || stressScore >= 0.9:
		return "critical"
	case bpm >= 140 || stressScore >= 0.75:
		return "high"
	case bpm >= 110 || stressScore >= 0.55:
		return "moderate"
	default:
		return "low"
	}
}

func classifyMood(stressScore float64, bpm int) string {
	switch {
	case stressScore >= 0.75 || bpm >= 130:
		return "stressed"
	case stressScore <= 0.35 && bpm < 95:
		return "calm"
	default:
		return "focused"
	}
}

func classifySOSSeverity(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.Contains(r, "desmaio"), strings.Contains(r, "infarto"), strings.Contains(r, "acidente"):
		return "critical"
	case strings.Contains(r, "dor"), strings.Contains(r, "falta de ar"), strings.Contains(r, "pressao"):
		return "high"
	default:
		return "moderate"
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
CREATE TABLE IF NOT EXISTS life_appointments (
	appointment_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	doctor_id TEXT NOT NULL,
	specialty TEXT NOT NULL,
	mode TEXT NOT NULL,
	scheduled_at TIMESTAMPTZ NOT NULL,
	notes TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_life_appointments_user_sched ON life_appointments(user_id, scheduled_at DESC);

CREATE TABLE IF NOT EXISTS life_sos_events (
	event_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	reason TEXT NOT NULL,
	contact_phone TEXT NOT NULL DEFAULT '',
	latitude DOUBLE PRECISION NOT NULL DEFAULT 0,
	longitude DOUBLE PRECISION NOT NULL DEFAULT 0,
	severity TEXT NOT NULL,
	status TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_life_sos_user_created ON life_sos_events(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS life_heartbeats (
	reading_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	device_id TEXT NOT NULL,
	activity TEXT NOT NULL,
	bpm INT NOT NULL,
	stress_score DOUBLE PRECISION NOT NULL,
	risk_level TEXT NOT NULL,
	mood TEXT NOT NULL,
	measured_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_life_heartbeats_user_measured ON life_heartbeats(user_id, measured_at DESC);
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

func clampFloat(v, minV, maxV float64) float64 {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
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
		w.Header().Set("x-service", "nexora-life")
		next.ServeHTTP(w, r)
	})
}
