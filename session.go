package folio

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const flashKey = "_flash"

// maxCookieSize is the most browsers reliably store per cookie.
const maxCookieSize = 4096

// Session is a small key-value store for one visitor. Changes are saved when
// the response headers are written.
//
// By default the values live in an encrypted, signed cookie: no server
// storage is needed, but a session can't be ended from the server before it
// expires and everything must fit in 4 KB. Set Config.SessionStore to keep
// values on the server instead; the cookie then holds only a random ID.
type Session struct {
	values  map[string]string
	changed bool
	renew   bool
	load    func() map[string]string
}

func (s *Session) ensure() {
	if s.values == nil {
		if s.load != nil {
			s.values = s.load()
		}
		if s.values == nil {
			s.values = map[string]string{}
		}
	}
}

func (s *Session) has(key string) bool {
	s.ensure()
	_, ok := s.values[key]
	return ok
}

// Get returns the value for key, or "" if it isn't set.
func (s *Session) Get(key string) string {
	s.ensure()
	return s.values[key]
}

// Set stores a value.
func (s *Session) Set(key, value string) {
	s.ensure()
	s.values[key] = value
	s.changed = true
}

// Delete removes a value.
func (s *Session) Delete(key string) {
	s.ensure()
	if _, ok := s.values[key]; ok {
		delete(s.values, key)
		s.changed = true
	}
}

// Pop returns the value for key and removes it.
func (s *Session) Pop(key string) string {
	v := s.Get(key)
	s.Delete(key)
	return v
}

// Clear removes every value and ends the session; with a SessionStore the
// server-side record is deleted too. Call it on logout.
func (s *Session) Clear() {
	s.ensure()
	clear(s.values)
	s.changed = true
}

// Renew keeps the values but moves them to a new session ID. Call it right
// after login so an ID planted before login (session fixation) stops working.
func (s *Session) Renew() {
	s.ensure()
	s.renew = true
	s.changed = true
}

type sessionCtxKey struct{}

// GetSession returns the request's session. Requests that didn't go through
// App.Handler get an empty session whose changes are discarded.
func GetSession(r *http.Request) *Session {
	if sw, ok := r.Context().Value(sessionCtxKey{}).(*sessionWriter); ok {
		return sw.session
	}
	return &Session{}
}

// SessionStore keeps session values on the server. Implement it over your
// database to share sessions between instances or to end a user's sessions
// from the server (e.g. after a password change).
type SessionStore interface {
	// Load returns the values for id, or nil if there's no live session.
	Load(ctx context.Context, id string) (map[string]string, error)
	// Save stores values for id until expires.
	Save(ctx context.Context, id string, values map[string]string, expires time.Time) error
	// Delete removes the session; deleting a missing session is not an error.
	Delete(ctx context.Context, id string) error
}

// MemoryStore is a SessionStore kept in process memory. Sessions are lost on
// restart and aren't shared between instances; use it for development or
// single-instance apps.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]memorySession
	saves    int
}

type memorySession struct {
	values  map[string]string
	expires time.Time
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: map[string]memorySession{}}
}

func (m *MemoryStore) Load(_ context.Context, id string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || time.Now().After(s.expires) {
		delete(m.sessions, id)
		return nil, nil
	}
	values := make(map[string]string, len(s.values))
	for k, v := range s.values {
		values[k] = v
	}
	return values, nil
}

func (m *MemoryStore) Save(_ context.Context, id string, values map[string]string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make(map[string]string, len(values))
	for k, v := range values {
		copied[k] = v
	}
	m.sessions[id] = memorySession{values: copied, expires: expires}
	// Sweep expired sessions now and then so memory doesn't grow forever.
	if m.saves++; m.saves%1000 == 0 {
		now := time.Now()
		for k, s := range m.sessions {
			if now.After(s.expires) {
				delete(m.sessions, k)
			}
		}
	}
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	return nil
}

type sessionManager struct {
	aead   cipher.AEAD // cookie mode
	store  SessionStore
	name   string
	maxAge time.Duration
	logger *slog.Logger
	random bool
	warn   sync.Once
}

func newSessionManager(cfg Config, logger *slog.Logger) (*sessionManager, error) {
	m := &sessionManager{store: cfg.SessionStore, name: cfg.SessionCookie, maxAge: cfg.SessionMaxAge, logger: logger}
	if m.store != nil {
		return m, nil
	}

	secret := cfg.SecretKey
	if len(secret) == 0 {
		m.random = true
		secret = make([]byte, 32)
		rand.Read(secret)
	} else if len(secret) < 32 {
		return nil, errors.New("folio: SecretKey must be at least 32 bytes")
	}
	key := sha256.Sum256(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	if m.aead, err = cipher.NewGCM(block); err != nil {
		return nil, err
	}
	return m, nil
}

type sessionPayload struct {
	Values  map[string]string `json:"v"`
	Expires int64             `json:"e"`
}

func (m *sessionManager) encode(values map[string]string) (string, error) {
	plain, err := json.Marshal(sessionPayload{Values: values, Expires: time.Now().Add(m.maxAge).Unix()})
	if err != nil {
		return "", err
	}
	nonce := make([]byte, m.aead.NonceSize(), m.aead.NonceSize()+len(plain)+m.aead.Overhead())
	rand.Read(nonce)
	// The cookie name is authenticated too, so a value can't be moved between cookies.
	sealed := m.aead.Seal(nonce, nonce, plain, []byte(m.name))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (m *sessionManager) decode(value string) map[string]string {
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(sealed) < m.aead.NonceSize() {
		return nil
	}
	nonce, ciphertext := sealed[:m.aead.NonceSize()], sealed[m.aead.NonceSize():]
	plain, err := m.aead.Open(nil, nonce, ciphertext, []byte(m.name))
	if err != nil {
		return nil
	}
	var p sessionPayload
	if json.Unmarshal(plain, &p) != nil || time.Now().Unix() > p.Expires {
		return nil
	}
	return p.Values
}

func newSessionID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (m *sessionManager) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &sessionWriter{ResponseWriter: w, m: m}
		sw.session = &Session{load: sw.read}
		r = r.WithContext(context.WithValue(r.Context(), sessionCtxKey{}, sw))
		sw.r = r
		next.ServeHTTP(sw, r)
		if !sw.wroteHeader && sw.session.changed {
			sw.WriteHeader(http.StatusOK)
		}
	})
}

// sessionWriter saves the session just before the response headers go out.
type sessionWriter struct {
	http.ResponseWriter
	m           *sessionManager
	r           *http.Request
	session     *Session
	id          string // store mode: the ID the request arrived with
	wroteHeader bool
}

func (sw *sessionWriter) read() map[string]string {
	cookie, err := sw.r.Cookie(sw.m.name)
	if err != nil || cookie.Value == "" {
		return nil
	}
	if sw.m.store == nil {
		return sw.m.decode(cookie.Value)
	}
	values, err := sw.m.store.Load(sw.r.Context(), cookie.Value)
	if err != nil {
		sw.m.logger.Error("loading session", "error", err)
		return nil
	}
	if values != nil {
		sw.id = cookie.Value
	}
	return values
}

func (sw *sessionWriter) WriteHeader(code int) {
	if !sw.wroteHeader {
		sw.wroteHeader = true
		sw.save()
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *sessionWriter) Write(b []byte) (int, error) {
	if !sw.wroteHeader {
		sw.WriteHeader(http.StatusOK)
	}
	return sw.ResponseWriter.Write(b)
}

func (sw *sessionWriter) Unwrap() http.ResponseWriter { return sw.ResponseWriter }

func (sw *sessionWriter) save() {
	s := sw.session
	if !s.changed {
		return
	}
	m := sw.m
	cookie := &http.Cookie{
		Name:     m.name,
		Path:     "/",
		HttpOnly: true,
		Secure:   sw.r.TLS != nil || sw.r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(m.maxAge / time.Second),
	}

	var err error
	switch {
	case len(s.values) == 0:
		if m.store != nil && sw.id != "" {
			err = m.store.Delete(sw.r.Context(), sw.id)
		}
		cookie.MaxAge = -1
	case m.store != nil:
		if sw.id != "" && s.renew {
			err = m.store.Delete(sw.r.Context(), sw.id)
		}
		if sw.id == "" || s.renew {
			sw.id = newSessionID()
		}
		if err == nil {
			err = m.store.Save(sw.r.Context(), sw.id, s.values, time.Now().Add(m.maxAge))
		}
		cookie.Value = sw.id
	default:
		cookie.Value, err = m.encode(s.values)
		if err == nil && len(cookie.Value) > maxCookieSize {
			err = errors.New("session cookie larger than 4 KB; store less or use a SessionStore")
		}
		if m.random {
			m.warn.Do(func() {
				m.logger.Warn("folio: no SecretKey configured; sessions end when the process restarts")
			})
		}
	}
	if err != nil {
		m.logger.Error("session not saved", "error", err)
		return
	}

	// The response depends on the cookie, so shared caches mustn't store it.
	sw.Header().Add("Vary", "Cookie")
	sw.Header().Set("Cache-Control", "private, no-cache")
	http.SetCookie(sw.ResponseWriter, cookie)
}
