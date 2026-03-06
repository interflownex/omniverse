package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	personaPersonal     = "personal"
	personaProfessional = "professional"
)

type inboundMessage struct {
	Type     string `json:"type"`
	ToUserID string `json:"to_user_id"`
	Text     string `json:"text"`
	Persona  string `json:"persona"`
}

type chatMessage struct {
	ID       string    `json:"id"`
	FromUser string    `json:"from_user"`
	ToUser   string    `json:"to_user"`
	Text     string    `json:"text"`
	Persona  string    `json:"persona"`
	SentAt   time.Time `json:"sent_at"`
}

type shieldPolicy struct {
	Blocked   bool      `json:"blocked"`
	Reason    string    `json:"reason"`
	Until     time.Time `json:"until"`
	UpdatedAt time.Time `json:"updated_at"`
}

type client struct {
	hub     *hub
	conn    *websocket.Conn
	send    chan []byte
	userID  string
	persona string
}

type hub struct {
	mu         sync.RWMutex
	clients    map[*client]struct{}
	byUser     map[string]map[*client]struct{}
	history    map[string][]chatMessage
	histLimit  int
	shield     map[string]shieldPolicy
	shieldToken string
}

func newHub(limit int, shieldToken string) *hub {
	if limit < 50 {
		limit = 50
	}
	return &hub{
		clients:     make(map[*client]struct{}),
		byUser:      make(map[string]map[*client]struct{}),
		history:     make(map[string][]chatMessage),
		histLimit:   limit,
		shield:      make(map[string]shieldPolicy),
		shieldToken: strings.TrimSpace(shieldToken),
	}
}

func (h *hub) addClient(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
	if _, ok := h.byUser[c.userID]; !ok {
		h.byUser[c.userID] = make(map[*client]struct{})
	}
	h.byUser[c.userID][c] = struct{}{}
}

func (h *hub) removeClient(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
	if set, ok := h.byUser[c.userID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.byUser, c.userID)
		}
	}
	close(c.send)
}

func (h *hub) conversationKey(a, b, persona string) string {
	users := []string{a, b}
	sort.Strings(users)
	return users[0] + "|" + users[1] + "|" + persona
}

func (h *hub) appendHistory(msg chatMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := h.conversationKey(msg.FromUser, msg.ToUser, msg.Persona)
	h.history[key] = append(h.history[key], msg)
	if len(h.history[key]) > h.histLimit {
		h.history[key] = h.history[key][len(h.history[key])-h.histLimit:]
	}
}

func (h *hub) getHistory(a, b, persona string, limit int) []chatMessage {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	key := h.conversationKey(a, b, persona)
	items := h.history[key]
	if len(items) <= limit {
		out := make([]chatMessage, len(items))
		copy(out, items)
		return out
	}
	out := make([]chatMessage, limit)
	copy(out, items[len(items)-limit:])
	return out
}

func (h *hub) presenceSnapshot() map[string][]string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make(map[string][]string)
	for userID, set := range h.byUser {
		personas := make(map[string]struct{})
		for c := range set {
			personas[c.persona] = struct{}{}
		}
		list := make([]string, 0, len(personas))
		for persona := range personas {
			list = append(list, persona)
		}
		sort.Strings(list)
		result[userID] = list
	}
	return result
}

func (h *hub) deliver(msg chatMessage) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	recipients := map[string]struct{}{msg.FromUser: {}, msg.ToUser: {}}
	delivered := 0
	payload, _ := json.Marshal(map[string]any{
		"type":    "chat_message",
		"message": msg,
	})
	for userID := range recipients {
		for c := range h.byUser[userID] {
			if c.persona != msg.Persona {
				continue
			}
			select {
			case c.send <- payload:
				delivered++
			default:
			}
		}
	}
	return delivered
}

func (h *hub) setShieldPolicy(userID string, blocked bool, reason string, until time.Time) shieldPolicy {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !blocked {
		delete(h.shield, userID)
		return shieldPolicy{Blocked: false, UpdatedAt: time.Now().UTC()}
	}
	policy := shieldPolicy{
		Blocked:   true,
		Reason:    strings.TrimSpace(reason),
		Until:     until,
		UpdatedAt: time.Now().UTC(),
	}
	h.shield[userID] = policy
	return policy
}

func (h *hub) isShieldBlocked(userID string, now time.Time) (bool, string, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	policy, ok := h.shield[userID]
	if !ok || !policy.Blocked {
		return false, "", time.Time{}
	}
	if !policy.Until.IsZero() && !now.Before(policy.Until) {
		delete(h.shield, userID)
		return false, "", time.Time{}
	}
	return true, policy.Reason, policy.Until
}

func (h *hub) getShieldPolicy(userID string) shieldPolicy {
	blocked, reason, until := h.isShieldBlocked(userID, time.Now().UTC())
	if !blocked {
		return shieldPolicy{Blocked: false}
	}
	return shieldPolicy{Blocked: true, Reason: reason, Until: until}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func main() {
	port := envOrDefault("PORT", "8086")
	historyLimit := envAsInt("CHAT_HISTORY_LIMIT", 500)
	shieldToken := envOrDefault("CHAT_SHIELD_TOKEN", "persona-ai-token")

	h := newHub(historyLimit, shieldToken)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/v1/chat/history", h.handleHistory)
	mux.HandleFunc("/v1/chat/presence", h.handlePresence)
	mux.HandleFunc("/v1/chat/policy/shield", h.handleShieldPolicy)
	mux.HandleFunc("/ws", h.handleWebSocket)
	mux.Handle("/app/", http.StripPrefix("/app/", http.FileServer(http.Dir("web"))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/app/", http.StatusTemporaryRedirect)
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withServiceHeader(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("nexora-chat listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "nexora-chat"})
}

func (h *hub) handlePresence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/chat/presence" {
		http.NotFound(w, r)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"presence": h.presenceSnapshot(),
	})
}

func (h *hub) handleShieldPolicy(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/policy/shield" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		token := strings.TrimSpace(r.Header.Get("x-shield-token"))
		if h.shieldToken != "" && token != h.shieldToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid shield token"})
			return
		}
		var req struct {
			UserID  string `json:"user_id"`
			Blocked bool   `json:"blocked"`
			Reason  string `json:"reason"`
			Until   string `json:"until"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		userID := normalizeID(req.UserID)
		if userID == "" {
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

		policy := h.setShieldPolicy(userID, req.Blocked, strings.TrimSpace(req.Reason), until)
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "updated",
			"user_id": userID,
			"policy":  policy,
		})
	case http.MethodGet:
		userID := normalizeID(r.URL.Query().Get("user_id"))
		if userID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
			return
		}
		policy := h.getShieldPolicy(userID)
		writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "policy": policy})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *hub) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/v1/chat/history" {
		http.NotFound(w, r)
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	peerID := strings.TrimSpace(r.URL.Query().Get("peer_id"))
	persona := normalizePersona(r.URL.Query().Get("persona"))
	limit := envAsInt("CHAT_HISTORY_QUERY_LIMIT", 50)
	if q := strings.TrimSpace(r.URL.Query().Get("limit")); q != "" {
		if parsed, err := strconv.Atoi(q); err == nil {
			limit = parsed
		}
	}
	if userID == "" || peerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and peer_id are required"})
		return
	}
	if persona == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "persona must be personal or professional"})
		return
	}

	history := h.getHistory(userID, peerID, persona, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":  userID,
		"peer_id":  peerID,
		"persona":  persona,
		"messages": history,
	})
}

func (h *hub) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ws" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	persona := normalizePersona(r.URL.Query().Get("persona"))
	if persona == "" {
		persona = personaPersonal
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	c := &client{
		hub:     h,
		conn:    conn,
		send:    make(chan []byte, 64),
		userID:  normalizeID(userID),
		persona: persona,
	}
	h.addClient(c)

	welcome, _ := json.Marshal(map[string]any{
		"type":    "session_ready",
		"user_id": c.userID,
		"persona": c.persona,
	})
	c.send <- welcome

	go c.writePump()
	c.readPump()
}

func (c *client) readPump() {
	defer func() {
		c.hub.removeClient(c)
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(8192)
	_ = c.conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	c.conn.SetPongHandler(func(_ string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(70 * time.Second))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var in inboundMessage
		if err := json.Unmarshal(raw, &in); err != nil {
			c.pushJSON(map[string]any{"type": "error", "error": "invalid json"})
			continue
		}

		switch strings.ToLower(strings.TrimSpace(in.Type)) {
		case "switch_persona":
			newPersona := normalizePersona(in.Persona)
			if newPersona == "" {
				c.pushJSON(map[string]any{"type": "error", "error": "persona must be personal or professional"})
				continue
			}
			c.persona = newPersona
			c.pushJSON(map[string]any{"type": "persona_switched", "persona": c.persona})
		case "send_message":
			toUser := normalizeID(in.ToUserID)
			text := strings.TrimSpace(in.Text)
			if toUser == "" || text == "" {
				c.pushJSON(map[string]any{"type": "error", "error": "to_user_id and text are required"})
				continue
			}
			if len(text) > 1000 {
				c.pushJSON(map[string]any{"type": "error", "error": "text too long"})
				continue
			}
			persona := normalizePersona(in.Persona)
			if persona == "" {
				persona = c.persona
			}

			if persona == personaProfessional {
				if blocked, reason, until := c.hub.isShieldBlocked(c.userID, time.Now().UTC()); blocked {
					c.pushJSON(map[string]any{
						"type":   "notification_blocked",
						"scope":  "sender",
						"reason": reason,
						"until":  until,
					})
					continue
				}
				if blocked, reason, until := c.hub.isShieldBlocked(toUser, time.Now().UTC()); blocked {
					c.pushJSON(map[string]any{
						"type":   "notification_blocked",
						"scope":  "recipient",
						"reason": reason,
						"until":  until,
					})
					continue
				}
			}

			msgID, err := randomHex(12)
			if err != nil {
				c.pushJSON(map[string]any{"type": "error", "error": "failed to generate message id"})
				continue
			}

			msg := chatMessage{
				ID:       "msg-" + msgID,
				FromUser: c.userID,
				ToUser:   toUser,
				Text:     text,
				Persona:  persona,
				SentAt:   time.Now().UTC(),
			}

			c.hub.appendHistory(msg)
			delivered := c.hub.deliver(msg)
			c.pushJSON(map[string]any{
				"type":            "send_ack",
				"message_id":      msg.ID,
				"delivered_count": delivered,
				"persona":         msg.Persona,
			})
		default:
			c.pushJSON(map[string]any{"type": "error", "error": "unsupported message type"})
		}
	}
}

func (c *client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case payload, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *client) pushJSON(payload any) {
	encoded, _ := json.Marshal(payload)
	select {
	case c.send <- encoded:
	default:
	}
}

func randomHex(n int) (string, error) {
	if n <= 0 {
		return "", errors.New("invalid length")
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func withServiceHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-service", "nexora-chat")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func normalizePersona(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case personaPersonal:
		return personaPersonal
	case personaProfessional:
		return personaProfessional
	default:
		return ""
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

func envAsInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
