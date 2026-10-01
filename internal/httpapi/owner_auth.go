package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joel299/agentic-voice-sdr/internal/ownerauth"
)

type ownerSession struct {
	id, username         string
	createdAt, expiresAt time.Time
	revoked              bool
}

type loginAttempt struct {
	window   time.Time
	failures int
}

const maxLoginIdentities = 4096

// OwnerAuthService manages interactive sessions in memory; only token digests are retained.
type OwnerAuthService struct {
	mu                     sync.Mutex
	username, passwordHash string
	ttl                    time.Duration
	legacy                 OwnerAuthorizer
	sessions               map[[32]byte]ownerSession
	attempts               map[string]loginAttempt
}

func NewOwnerAuthService(username, passwordHash string, ttl time.Duration, legacy OwnerAuthorizer) *OwnerAuthService {
	if username == "" {
		username = "owner"
	}
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	return &OwnerAuthService{username: username, passwordHash: passwordHash, ttl: ttl, legacy: legacy, sessions: make(map[[32]byte]ownerSession), attempts: make(map[string]loginAttempt)}
}

func (a *OwnerAuthService) Authorize(r *http.Request) bool {
	if a == nil || r == nil {
		return false
	}
	if a.legacy != nil && a.legacy.Authorize(r) {
		return true
	}
	digest, ok := bearerDigest(r)
	if !ok {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, exists := a.sessions[digest]
	return exists && !session.revoked && time.Now().Before(session.expiresAt)
}

func (a *OwnerAuthService) authenticate(r *http.Request) (username, kind string, expires time.Time, ok bool) {
	if a.legacy != nil && a.legacy.Authorize(r) {
		return "owner", "legacy", time.Time{}, true
	}
	digest, valid := bearerDigest(r)
	if !valid {
		return "", "", time.Time{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, exists := a.sessions[digest]
	if !exists || session.revoked || !time.Now().Before(session.expiresAt) {
		delete(a.sessions, digest)
		return "", "", time.Time{}, false
	}
	return session.username, "interactive", session.expiresAt, true
}

func (a *OwnerAuthService) login(w http.ResponseWriter, r *http.Request) {
	identity := clientIdentity(r)
	if a.rateLimited(identity, time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many login attempts"})
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeJSON(w, r, &input) != nil {
		return
	}
	passwordOK := ownerauth.VerifyPassword(a.passwordHash, input.Password)
	if a.passwordHash == "" || input.Username != a.username || !passwordOK {
		a.recordFailed(identity, time.Now())
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "login unavailable"})
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	digest := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	expires := now.Add(a.ttl)
	var sidBytes [16]byte
	if _, err := rand.Read(sidBytes[:]); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "login unavailable"})
		return
	}
	sid := hex.EncodeToString(sidBytes[:])
	a.mu.Lock()
	for key, session := range a.sessions {
		if session.revoked || !now.Before(session.expiresAt) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 4096 {
		a.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session capacity reached"})
		return
	}
	a.sessions[digest] = ownerSession{id: sid, username: a.username, createdAt: now, expiresAt: expires}
	delete(a.attempts, identity)
	a.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	expiresIn := int64((a.ttl + time.Second - 1) / time.Second)
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": expiresIn, "expires_at": expires})
}

func (a *OwnerAuthService) session(w http.ResponseWriter, r *http.Request) {
	username, kind, expires, ok := a.authenticate(r)
	if !ok {
		unauthorized(w)
		return
	}
	body := map[string]any{"authenticated": true, "username": username, "session_type": kind}
	if !expires.IsZero() {
		body["expires_at"] = expires
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *OwnerAuthService) logout(w http.ResponseWriter, r *http.Request) {
	digest, ok := bearerDigest(r)
	if !ok {
		unauthorized(w)
		return
	}
	a.mu.Lock()
	session, exists := a.sessions[digest]
	valid := exists && !session.revoked && time.Now().Before(session.expiresAt)
	if valid {
		session.revoked = true
		a.sessions[digest] = session
	}
	a.mu.Unlock()
	if !valid {
		unauthorized(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"logged_out": true})
}

func (a *OwnerAuthService) rateLimited(identity string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, known := a.attempts[identity]; !known && len(a.attempts) >= maxLoginIdentities {
		for key, value := range a.attempts {
			if now.Sub(value.window) >= time.Minute {
				delete(a.attempts, key)
			}
		}
		if len(a.attempts) >= maxLoginIdentities {
			return true
		}
	}
	attempt := a.attempts[identity]
	if now.Sub(attempt.window) >= time.Minute {
		delete(a.attempts, identity)
		return false
	}
	return attempt.failures >= 5
}
func (a *OwnerAuthService) recordFailed(identity string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.attempts) >= maxLoginIdentities {
		for key, value := range a.attempts {
			if now.Sub(value.window) >= time.Minute {
				delete(a.attempts, key)
			}
		}
		if len(a.attempts) >= 4096 {
			return
		}
	}
	entry := a.attempts[identity]
	if now.Sub(entry.window) >= time.Minute {
		entry = loginAttempt{window: now}
	}
	entry.failures++
	a.attempts[identity] = entry
}

func registerOwnerAuthRoutes(router chi.Router, auth *OwnerAuthService) {
	router.Route("/v1/auth", func(r chi.Router) {
		r.Post("/login", auth.login)
		r.With(ownerOnly(auth)).Get("/session", auth.session)
		r.With(ownerOnly(auth)).Post("/logout", auth.logout)
	})
}
func bearerDigest(r *http.Request) ([32]byte, bool) {
	var zero [32]byte
	header := r.Header.Get("Authorization")
	separator := strings.IndexByte(header, ' ')
	if separator <= 0 || !strings.EqualFold(header[:separator], "Bearer") {
		return zero, false
	}
	token := header[separator+1:]
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' || r == '\n' }) >= 0 {
		return zero, false
	}
	return sha256.Sum256([]byte(token)), true
}
func clientIdentity(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}
