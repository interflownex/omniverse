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
	db                  *sql.DB
	httpClient          *http.Client
	documentEngineBase  string
	documentEngineToken string
}

type enrollmentRequest struct {
	UserID   string `json:"user_id"`
	CourseID string `json:"course_id"`
}

type completeRequest struct {
	UserID   string  `json:"user_id"`
	CourseID string  `json:"course_id"`
	Score    float64 `json:"score"`
}

type matchRequest struct {
	UserID           string   `json:"user_id"`
	Skills           []string `json:"skills"`
	City             string   `json:"city"`
	Seniority        string   `json:"seniority"`
	RemotePreference string   `json:"remote_preference"`
	Limit            int      `json:"limit"`
}

type jobItem struct {
	JobID       string
	Title       string
	Company     string
	City        string
	Seniority   string
	Skills      []string
	SalaryRange string
	Remote      bool
}

func main() {
	port := envOrDefault("PORT", "8097")
	dsn := envOrDefault("POSTGRES_DSN", "postgres://nexora:nexora123@postgres:5432/nexora_pay?sslmode=disable")
	docBase := strings.TrimRight(envOrDefault("DOCUMENT_ENGINE_BASE_URL", "http://document-engine:8094"), "/")
	docToken := strings.TrimSpace(envOrDefault("DOCUMENT_ENGINE_TOKEN", "doc-engine-token"))

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
	if err := seedBaseData(db); err != nil {
		log.Printf("warning: failed to seed base data: %v", err)
	}

	s := &server{
		db:                  db,
		httpClient:          &http.Client{Timeout: 7 * time.Second},
		documentEngineBase:  docBase,
		documentEngineToken: docToken,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/school/courses", s.handleCourses)
	mux.HandleFunc("/v1/school/enrollments", s.handleEnrollments)
	mux.HandleFunc("/v1/school/enrollments/complete", s.handleCompleteEnrollment)
	mux.HandleFunc("/v1/school/certificates", s.handleCertificates)
	mux.HandleFunc("/v1/jobs", s.handleJobs)
	mux.HandleFunc("/v1/jobs/match", s.handleMatch)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-school-job listening on :%s", port)
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db_unavailable", "service": "nexora-school-job"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexora-school-job", "features": []string{"courses", "certificates", "deep-match"}})
}

func (s *server) handleCourses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/school/courses" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT course_id, title, description, duration_hours, skills, level
		FROM school_courses
		WHERE active = TRUE
		ORDER BY title ASC
	`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list courses"})
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var courseID, title, description, skillsCSV, level string
		var durationHours int
		if err := rows.Scan(&courseID, &title, &description, &durationHours, &skillsCSV, &level); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan courses"})
			return
		}
		items = append(items, map[string]any{
			"course_id":      courseID,
			"title":          title,
			"description":    description,
			"duration_hours": durationHours,
			"skills":         splitCSV(skillsCSV),
			"level":          level,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) handleEnrollments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/school/enrollments" {
		http.NotFound(w, r)
		return
	}
	var req enrollmentRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.UserID = normalizeID(req.UserID)
	req.CourseID = normalizeID(req.CourseID)
	if req.UserID == "" || req.CourseID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and course_id are required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO school_enrollments (user_id, course_id, status, progress)
		VALUES ($1,$2,'enrolled',0)
		ON CONFLICT (user_id, course_id) DO UPDATE SET
			status = CASE WHEN school_enrollments.status = 'completed' THEN school_enrollments.status ELSE 'enrolled' END,
			updated_at = NOW()
	`, req.UserID, req.CourseID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create enrollment"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"status": "enrolled", "user_id": req.UserID, "course_id": req.CourseID})
}

func (s *server) handleCompleteEnrollment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/school/enrollments/complete" {
		http.NotFound(w, r)
		return
	}
	var req completeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.UserID = normalizeID(req.UserID)
	req.CourseID = normalizeID(req.CourseID)
	req.Score = clamp(req.Score, 0, 100)
	if req.UserID == "" || req.CourseID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and course_id are required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var courseTitle string
	err := s.db.QueryRowContext(ctx, `
		SELECT title
		FROM school_courses
		WHERE course_id = $1 AND active = TRUE
	`, req.CourseID).Scan(&courseTitle)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "course not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load course"})
		return
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO school_enrollments (user_id, course_id, status, progress, score, completed_at)
		VALUES ($1,$2,'completed',100,$3,NOW())
		ON CONFLICT (user_id, course_id) DO UPDATE SET
			status = 'completed',
			progress = 100,
			score = $3,
			completed_at = NOW(),
			updated_at = NOW()
	`, req.UserID, req.CourseID, req.Score)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to complete enrollment"})
		return
	}

	certificateID := fmt.Sprintf("cert-%d", time.Now().UTC().UnixNano())
	docStatus := "pending"
	documentID := ""
	documentURL := ""
	if s.documentEngineBase != "" {
		if certResp, emitErr := s.emitCertificateEvent(ctx, certificateID, req.UserID, req.CourseID, courseTitle, req.Score); emitErr == nil {
			docStatus = "queued"
			if v, ok := certResp["document_id"].(string); ok {
				documentID = strings.TrimSpace(v)
			}
			if v, ok := certResp["document_url"].(string); ok {
				documentURL = strings.TrimSpace(v)
			}
		} else {
			log.Printf("certificate_emit_failed: %v", emitErr)
		}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO school_certificates (
			certificate_id, user_id, course_id, course_title, score, document_status, document_id, document_url
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, certificateID, req.UserID, req.CourseID, courseTitle, req.Score, docStatus, documentID, documentURL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save certificate"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":          "completed",
		"certificate_id":  certificateID,
		"user_id":         req.UserID,
		"course_id":       req.CourseID,
		"course_title":    courseTitle,
		"score":           req.Score,
		"document_status": docStatus,
		"document_id":     documentID,
		"document_url":    documentURL,
	})
}

func (s *server) handleCertificates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/school/certificates" {
		http.NotFound(w, r)
		return
	}
	userID := normalizeID(r.URL.Query().Get("user_id"))
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	limit := intOrDefault(r.URL.Query().Get("limit"), 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT certificate_id, user_id, course_id, course_title, score, document_status, document_id, document_url, issued_at
		FROM school_certificates
		WHERE user_id = $1
		ORDER BY issued_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list certificates"})
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var certificateID, user, courseID, courseTitle, docStatus, documentID, documentURL string
		var score float64
		var issuedAt time.Time
		if err := rows.Scan(&certificateID, &user, &courseID, &courseTitle, &score, &docStatus, &documentID, &documentURL, &issuedAt); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan certificates"})
			return
		}
		items = append(items, map[string]any{
			"certificate_id":  certificateID,
			"user_id":         user,
			"course_id":       courseID,
			"course_title":    courseTitle,
			"score":           score,
			"document_status": docStatus,
			"document_id":     documentID,
			"document_url":    documentURL,
			"issued_at":       issuedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/jobs" {
		http.NotFound(w, r)
		return
	}
	cityFilter := normalizeText(r.URL.Query().Get("city"))
	limit := intOrDefault(r.URL.Query().Get("limit"), 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 300 {
		limit = 300
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT job_id, title, company, city, seniority, skills, salary_range, remote
		FROM job_openings
		WHERE active = TRUE
		  AND ($1 = '' OR LOWER(city) = $1)
		ORDER BY job_id
		LIMIT $2
	`, cityFilter, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list jobs"})
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var jobID, title, company, city, seniority, skillsCSV, salaryRange string
		var remote bool
		if err := rows.Scan(&jobID, &title, &company, &city, &seniority, &skillsCSV, &salaryRange, &remote); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan jobs"})
			return
		}
		items = append(items, map[string]any{
			"job_id":        jobID,
			"title":         title,
			"company":       company,
			"city":          city,
			"seniority":     seniority,
			"skills":        splitCSV(skillsCSV),
			"salary_range":  salaryRange,
			"remote":        remote,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) handleMatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/jobs/match" {
		http.NotFound(w, r)
		return
	}
	var req matchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.UserID = normalizeID(req.UserID)
	req.City = normalizeText(req.City)
	req.Seniority = normalizeText(req.Seniority)
	req.RemotePreference = normalizeText(req.RemotePreference)
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 50 {
		req.Limit = 50
	}
	nSkills := normalizeSkillList(req.Skills)
	if len(nSkills) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skills are required"})
		return
	}

	jobs, err := s.loadJobs(r.Context(), 300)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load jobs"})
		return
	}

	type scored struct {
		job   jobItem
		score float64
		why   []string
	}
	results := make([]scored, 0, len(jobs))
	for _, job := range jobs {
		score, why := deepMatch(nSkills, req.City, req.Seniority, req.RemotePreference, job)
		if score <= 0 {
			continue
		}
		results = append(results, scored{job: job, score: score, why: why})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })
	if len(results) > req.Limit {
		results = results[:req.Limit]
	}

	payload := make([]map[string]any, 0, len(results))
	for _, item := range results {
		payload = append(payload, map[string]any{
			"job_id":       item.job.JobID,
			"title":        item.job.Title,
			"company":      item.job.Company,
			"city":         item.job.City,
			"seniority":    item.job.Seniority,
			"skills":       item.job.Skills,
			"salary_range": item.job.SalaryRange,
			"remote":       item.job.Remote,
			"match_score":  math.Round(item.score*10) / 10,
			"why":          item.why,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": req.UserID,
		"count":   len(payload),
		"data":    payload,
	})
}

func deepMatch(userSkills []string, city, seniority, remotePref string, job jobItem) (float64, []string) {
	skillSet := make(map[string]struct{}, len(userSkills))
	for _, s := range userSkills {
		skillSet[s] = struct{}{}
	}

	matchCount := 0
	for _, js := range job.Skills {
		if _, ok := skillSet[js]; ok {
			matchCount++
		}
	}
	why := []string{}
	score := 0.0
	if len(job.Skills) > 0 {
		ratio := float64(matchCount) / float64(len(job.Skills))
		score += ratio * 70
		why = append(why, fmt.Sprintf("skills %.0f%%", ratio*100))
	}
	if city != "" && normalizeText(job.City) == city {
		score += 15
		why = append(why, "cidade compativel")
	}
	if seniority != "" {
		d := seniorityDistance(seniority, normalizeText(job.Seniority))
		if d == 0 {
			score += 15
			why = append(why, "senioridade ideal")
		} else if d == 1 {
			score += 8
			why = append(why, "senioridade proxima")
		}
	}
	if remotePref == "remote" && job.Remote {
		score += 7
		why = append(why, "preferencia remota")
	}
	if remotePref == "hybrid" {
		score += 3
	}
	return score, why
}

func seniorityDistance(a, b string) int {
	levels := map[string]int{"junior": 1, "mid": 2, "pleno": 2, "senior": 3, "lead": 4}
	av, aok := levels[a]
	bv, bok := levels[b]
	if !aok || !bok {
		return 3
	}
	d := av - bv
	if d < 0 {
		d = -d
	}
	return d
}

func (s *server) loadJobs(parent context.Context, limit int) ([]jobItem, error) {
	ctx, cancel := context.WithTimeout(parent, 6*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT job_id, title, company, city, seniority, skills, salary_range, remote
		FROM job_openings
		WHERE active = TRUE
		ORDER BY job_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]jobItem, 0)
	for rows.Next() {
		var job jobItem
		var skillsCSV string
		if err := rows.Scan(&job.JobID, &job.Title, &job.Company, &job.City, &job.Seniority, &skillsCSV, &job.SalaryRange, &job.Remote); err != nil {
			return nil, err
		}
		job.Skills = splitCSV(skillsCSV)
		items = append(items, job)
	}
	return items, nil
}

func (s *server) emitCertificateEvent(parent context.Context, certificateID, userID, courseID, courseTitle string, score float64) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	payload := map[string]any{
		"certificate_id": certificateID,
		"user_id":        userID,
		"course_id":      courseID,
		"course_title":   courseTitle,
		"issuer":         "Nexora School",
		"score":          score,
		"description":    fmt.Sprintf("Certificado de conclusao - %s", courseTitle),
	}
	headers := map[string]string{"x-doc-engine-token": s.documentEngineToken}
	respBody, status, rawBody, err := s.callJSON(ctx, http.MethodPost, s.documentEngineBase+"/v1/events/certificate", payload, headers)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("document-engine status %d: %s", status, rawBody)
	}
	mapped, _ := respBody.(map[string]any)
	if mapped == nil {
		mapped = map[string]any{"status": "queued", "certificate_id": certificateID}
	}
	return mapped, nil
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
CREATE TABLE IF NOT EXISTS school_courses (
	course_id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	description TEXT NOT NULL,
	duration_hours INT NOT NULL,
	skills TEXT NOT NULL,
	level TEXT NOT NULL,
	active BOOLEAN NOT NULL DEFAULT TRUE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS school_enrollments (
	id BIGSERIAL PRIMARY KEY,
	user_id TEXT NOT NULL,
	course_id TEXT NOT NULL REFERENCES school_courses(course_id),
	status TEXT NOT NULL,
	progress INT NOT NULL DEFAULT 0,
	score DOUBLE PRECISION NOT NULL DEFAULT 0,
	started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	completed_at TIMESTAMPTZ,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE(user_id, course_id)
);

CREATE INDEX IF NOT EXISTS ix_school_enrollments_user_updated ON school_enrollments(user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS school_certificates (
	certificate_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	course_id TEXT NOT NULL,
	course_title TEXT NOT NULL,
	score DOUBLE PRECISION NOT NULL,
	document_status TEXT NOT NULL,
	document_id TEXT NOT NULL DEFAULT '',
	document_url TEXT NOT NULL DEFAULT '',
	issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_school_certificates_user_issued ON school_certificates(user_id, issued_at DESC);

CREATE TABLE IF NOT EXISTS job_openings (
	job_id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	company TEXT NOT NULL,
	city TEXT NOT NULL,
	seniority TEXT NOT NULL,
	skills TEXT NOT NULL,
	salary_range TEXT NOT NULL,
	remote BOOLEAN NOT NULL DEFAULT FALSE,
	active BOOLEAN NOT NULL DEFAULT TRUE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`
	_, err := db.Exec(ddl)
	return err
}

func seedBaseData(db *sql.DB) error {
	seed := `
INSERT INTO school_courses (course_id, title, description, duration_hours, skills, level)
VALUES
	('course-go-api', 'Go API Express', 'Curso rapido de APIs escalaveis com Go.', 10, 'go,api,postgres,docker', 'mid'),
	('course-finops', 'FinOps para Negocios Digitais', 'Controle de custos e margem para plataformas.', 8, 'finops,analytics,business', 'junior'),
	('course-ai-retail', 'IA para Social Commerce', 'Match de produtos, humor e conversao no feed.', 12, 'ai,python,recommendation,ecommerce', 'mid')
ON CONFLICT (course_id) DO NOTHING;

INSERT INTO job_openings (job_id, title, company, city, seniority, skills, salary_range, remote)
VALUES
	('job-001', 'Backend Go Engineer', 'Nexora Labs', 'betim', 'mid', 'go,postgres,docker,api', 'R$ 8k - R$ 12k', true),
	('job-002', 'Analista de Dados FinTech', 'Nexora Pay', 'betim', 'junior', 'sql,python,analytics,fintech', 'R$ 5k - R$ 8k', false),
	('job-003', 'Product Analyst Social Commerce', 'Nexora Social', 'sao-paulo', 'senior', 'ai,product,experimentation,ecommerce', 'R$ 12k - R$ 18k', true),
	('job-004', 'Especialista Logistica Dropship', 'Nexora Stock', 'betim', 'pleno', 'dropship,ops,excel,tracking', 'R$ 6k - R$ 9k', false)
ON CONFLICT (job_id) DO NOTHING;
`
	_, err := db.Exec(seed)
	return err
}

func normalizeSkillList(raw []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		n := normalizeText(item)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func splitCSV(raw string) []string {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		n := normalizeText(p)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
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
		w.Header().Set("x-service", "nexora-school-job")
		next.ServeHTTP(w, r)
	})
}
