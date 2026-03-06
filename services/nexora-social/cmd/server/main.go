package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type monetization struct {
	Enabled     bool    `json:"enabled"`
	Model       string  `json:"model"`
	CPMUSD      float64 `json:"cpm_usd"`
	RevSharePct float64 `json:"rev_share_pct"`
}

type video struct {
	ID              string       `json:"id"`
	CreatorID       string       `json:"creator_id"`
	Title           string       `json:"title"`
	Description     string       `json:"description"`
	ObjectKey       string       `json:"object_key"`
	DurationSeconds int          `json:"duration_seconds"`
	Audience        string       `json:"audience"`
	Tags            []string     `json:"tags"`
	PublishedAt     time.Time    `json:"published_at"`
	Views           int64        `json:"views"`
	Likes           int64        `json:"likes"`
	Monetization    monetization `json:"monetization"`
}

type feedVideo struct {
	ID              string       `json:"id"`
	CreatorID       string       `json:"creator_id"`
	Title           string       `json:"title"`
	Description     string       `json:"description"`
	VideoURL        string       `json:"video_url"`
	DurationSeconds int          `json:"duration_seconds"`
	Audience        string       `json:"audience"`
	Tags            []string     `json:"tags"`
	PublishedAt     time.Time    `json:"published_at"`
	Views           int64        `json:"views"`
	Likes           int64        `json:"likes"`
	Monetization    monetization `json:"monetization"`
}

type ingestVideoRequest struct {
	ID              string       `json:"id"`
	CreatorID       string       `json:"creator_id"`
	Title           string       `json:"title"`
	Description     string       `json:"description"`
	ObjectKey       string       `json:"object_key"`
	DurationSeconds int          `json:"duration_seconds"`
	Audience        string       `json:"audience"`
	Tags            []string     `json:"tags"`
	PublishedAt     string       `json:"published_at"`
	Views           int64        `json:"views"`
	Likes           int64        `json:"likes"`
	Monetization    monetization `json:"monetization"`
}

type feedCursor struct {
	PublishedAt time.Time `json:"published_at"`
	ID          string    `json:"id"`
}

type server struct {
	mu            sync.RWMutex
	videos        []video
	publicBaseURL string
	bucket        string
	ingestToken   string
}

func main() {
	port := envOrDefault("PORT", "8084")
	publicBase := envOrDefault("MINIO_PUBLIC_BASE_URL", "http://minio:9000")
	bucket := envOrDefault("MINIO_BUCKET", "nexora-videos")
	ingestToken := envOrDefault("SOCIAL_INGEST_TOKEN", "social-ingest-token")
	seedSize := envAsInt("SOCIAL_FEED_SEED_SIZE", 90)
	if seedSize < 0 {
		seedSize = 0
	}

	s := &server{
		videos:        seedFeed(seedSize),
		publicBaseURL: publicBase,
		bucket:        bucket,
		ingestToken:   ingestToken,
	}
	sortVideosDesc(s.videos)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/feed", s.handleFeed)
	mux.HandleFunc("/v1/videos", s.handleVideos)
	mux.HandleFunc("/v1/videos/", s.handleVideoByID)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-social listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	s.mu.RLock()
	count := len(s.videos)
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"service":      "nexora-social",
		"videos_loaded": count,
		"bucket":       s.bucket,
	})
}

func (s *server) handleFeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/feed" {
		http.NotFound(w, r)
		return
	}

	limit := envAsInt("SOCIAL_FEED_DEFAULT_LIMIT", 12)
	if limit <= 0 {
		limit = 12
	}
	if q := strings.TrimSpace(r.URL.Query().Get("limit")); q != "" {
		if parsed, err := strconv.Atoi(q); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}

	persona := normalizePersona(r.URL.Query().Get("persona"))
	if persona == "" {
		persona = "all"
	}

	cursor, err := decodeCursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid cursor"})
		return
	}

	s.mu.RLock()
	videosSnapshot := make([]video, len(s.videos))
	copy(videosSnapshot, s.videos)
	s.mu.RUnlock()

	filtered := make([]video, 0, len(videosSnapshot))
	for _, v := range videosSnapshot {
		if audienceMatchesPersona(v.Audience, persona) {
			filtered = append(filtered, v)
		}
	}

	startIndex := 0
	if cursor != nil {
		startIndex = len(filtered)
		for i, v := range filtered {
			if isAfterCursor(v, *cursor) {
				startIndex = i
				break
			}
		}
	}

	endIndex := startIndex + limit
	if endIndex > len(filtered) {
		endIndex = len(filtered)
	}

	pageVideos := filtered[startIndex:endIndex]
	responseItems := make([]feedVideo, 0, len(pageVideos))
	for _, v := range pageVideos {
		responseItems = append(responseItems, feedVideo{
			ID:              v.ID,
			CreatorID:       v.CreatorID,
			Title:           v.Title,
			Description:     v.Description,
			VideoURL:        s.buildObjectURL(v.ObjectKey),
			DurationSeconds: v.DurationSeconds,
			Audience:        v.Audience,
			Tags:            v.Tags,
			PublishedAt:     v.PublishedAt,
			Views:           v.Views,
			Likes:           v.Likes,
			Monetization:    v.Monetization,
		})
	}

	hasMore := endIndex < len(filtered)
	nextCursor := ""
	if hasMore && len(pageVideos) > 0 {
		last := pageVideos[len(pageVideos)-1]
		nextCursor = encodeCursor(feedCursor{PublishedAt: last.PublishedAt, ID: last.ID})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": responseItems,
		"paging": map[string]any{
			"strategy":    "keyset_cursor",
			"limit":       limit,
			"next_cursor": nextCursor,
			"has_more":    hasMore,
		},
		"persona": persona,
	})
}

func (s *server) handleVideos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleIngestVideo(w, r)
	case http.MethodGet:
		s.handleListVideos(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) handleListVideos(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/videos" {
		http.NotFound(w, r)
		return
	}

	creatorID := strings.TrimSpace(r.URL.Query().Get("creator_id"))
	persona := normalizePersona(r.URL.Query().Get("persona"))
	if persona == "" {
		persona = "all"
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]feedVideo, 0)
	for _, v := range s.videos {
		if creatorID != "" && v.CreatorID != creatorID {
			continue
		}
		if !audienceMatchesPersona(v.Audience, persona) {
			continue
		}
		items = append(items, feedVideo{
			ID:              v.ID,
			CreatorID:       v.CreatorID,
			Title:           v.Title,
			Description:     v.Description,
			VideoURL:        s.buildObjectURL(v.ObjectKey),
			DurationSeconds: v.DurationSeconds,
			Audience:        v.Audience,
			Tags:            v.Tags,
			PublishedAt:     v.PublishedAt,
			Views:           v.Views,
			Likes:           v.Likes,
			Monetization:    v.Monetization,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *server) handleIngestVideo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/videos" {
		http.NotFound(w, r)
		return
	}

	reqToken := strings.TrimSpace(r.Header.Get("x-ingest-token"))
	if s.ingestToken != "" && reqToken != s.ingestToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid ingest token"})
		return
	}

	var req ingestVideoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	req.CreatorID = strings.TrimSpace(req.CreatorID)
	req.Title = strings.TrimSpace(req.Title)
	req.ObjectKey = strings.TrimSpace(req.ObjectKey)
	req.Description = strings.TrimSpace(req.Description)

	if req.ID == "" {
		generatedID, err := randomHex(12)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate video id"})
			return
		}
		req.ID = "vid-" + generatedID
	}
	if req.CreatorID == "" || req.Title == "" || req.ObjectKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "creator_id, title and object_key are required"})
		return
	}
	if req.DurationSeconds <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "duration_seconds must be greater than zero"})
		return
	}

	publishedAt := time.Now().UTC()
	if ts := strings.TrimSpace(req.PublishedAt); ts != "" {
		parsed, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "published_at must be RFC3339"})
			return
		}
		publishedAt = parsed.UTC()
	}

	item := video{
		ID:              req.ID,
		CreatorID:       req.CreatorID,
		Title:           req.Title,
		Description:     req.Description,
		ObjectKey:       strings.TrimLeft(req.ObjectKey, "/"),
		DurationSeconds: req.DurationSeconds,
		Audience:        normalizeAudience(req.Audience),
		Tags:            normalizeTags(req.Tags),
		PublishedAt:     publishedAt,
		Views:           maxInt64(req.Views, 0),
		Likes:           maxInt64(req.Likes, 0),
		Monetization:    normalizeMonetization(req.Monetization),
	}

	s.mu.Lock()
	for _, existing := range s.videos {
		if existing.ID == item.ID {
			s.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "video already exists"})
			return
		}
	}
	s.videos = append(s.videos, item)
	sortVideosDesc(s.videos)
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":        item.ID,
		"creator_id": item.CreatorID,
		"video_url": s.buildObjectURL(item.ObjectKey),
		"status":    "ingested",
	})
}

func (s *server) handleVideoByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] != "videos" {
		http.NotFound(w, r)
		return
	}
	videoID := strings.TrimSpace(parts[2])
	if videoID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid video id"})
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.videos {
		if v.ID == videoID {
			writeJSON(w, http.StatusOK, map[string]any{
				"id":               v.ID,
				"creator_id":       v.CreatorID,
				"title":            v.Title,
				"description":      v.Description,
				"video_url":        s.buildObjectURL(v.ObjectKey),
				"duration_seconds": v.DurationSeconds,
				"audience":         v.Audience,
				"tags":             v.Tags,
				"published_at":     v.PublishedAt,
				"views":            v.Views,
				"likes":            v.Likes,
				"monetization":     v.Monetization,
			})
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "video not found"})
}

func sortVideosDesc(items []video) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].PublishedAt.Equal(items[j].PublishedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].PublishedAt.After(items[j].PublishedAt)
	})
}

func (s *server) buildObjectURL(objectKey string) string {
	trimmedBase := strings.TrimRight(s.publicBaseURL, "/")
	trimmedKey := strings.TrimLeft(objectKey, "/")
	return fmt.Sprintf("%s/%s/%s", trimmedBase, s.bucket, trimmedKey)
}

func isAfterCursor(v video, cursor feedCursor) bool {
	if v.PublishedAt.Before(cursor.PublishedAt) {
		return true
	}
	if v.PublishedAt.Equal(cursor.PublishedAt) && v.ID < cursor.ID {
		return true
	}
	return false
}

func encodeCursor(c feedCursor) string {
	payload, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeCursor(raw string) (*feedCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var c feedCursor
	if err := json.Unmarshal(decoded, &c); err != nil {
		return nil, err
	}
	if c.ID == "" || c.PublishedAt.IsZero() {
		return nil, errors.New("invalid cursor payload")
	}
	return &c, nil
}

func seedFeed(size int) []video {
	seeded := make([]video, 0, size)
	now := time.Now().UTC()
	for i := 0; i < size; i++ {
		id := fmt.Sprintf("seed-%06d", i+1)
		creator := fmt.Sprintf("creator-%02d", (i%9)+1)
		audience := "all"
		switch i % 3 {
		case 1:
			audience = "personal"
		case 2:
			audience = "professional"
		}
		seeded = append(seeded, video{
			ID:              id,
			CreatorID:       creator,
			Title:           fmt.Sprintf("Video curto %d", i+1),
			Description:     "Conteudo vertical para feed infinito.",
			ObjectKey:       fmt.Sprintf("seed/%s/%s.mp4", creator, id),
			DurationSeconds: 12 + (i % 49),
			Audience:        audience,
			Tags:            []string{"short", "vertical", "nexora"},
			PublishedAt:     now.Add(-time.Duration(i) * time.Minute),
			Views:           int64(1000 + (i * 37)),
			Likes:           int64(100 + (i * 11)),
			Monetization: monetization{
				Enabled:     true,
				Model:       "ad_cpm",
				CPMUSD:      1.25,
				RevSharePct: 62.5,
			},
		})
	}
	return seeded
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		n := strings.ToLower(strings.TrimSpace(tag))
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func normalizeAudience(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "personal":
		return "personal"
	case "professional":
		return "professional"
	default:
		return "all"
	}
}

func normalizePersona(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "personal":
		return "personal"
	case "professional":
		return "professional"
	case "all":
		return "all"
	default:
		return ""
	}
}

func audienceMatchesPersona(audience, persona string) bool {
	a := normalizeAudience(audience)
	if persona == "all" {
		return true
	}
	if a == "all" {
		return true
	}
	return a == persona
}

func normalizeMonetization(m monetization) monetization {
	out := m
	if !out.Enabled {
		out.Model = "off"
		out.CPMUSD = 0
		out.RevSharePct = 0
		return out
	}
	if strings.TrimSpace(out.Model) == "" {
		out.Model = "ad_cpm"
	}
	if out.CPMUSD < 0 {
		out.CPMUSD = 0
	}
	if out.RevSharePct < 0 {
		out.RevSharePct = 0
	}
	if out.RevSharePct > 100 {
		out.RevSharePct = 100
	}
	return out
}

func maxInt64(v, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
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

func withServiceHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Service", "nexora-social")
		next.ServeHTTP(w, r)
	})
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envAsInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}
