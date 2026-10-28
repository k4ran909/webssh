// Package api serves accounts, sessions, the encrypted vault and the audit log.
//
// Zero-knowledge model: the browser derives two keys from the master
// password. The auth key is sent as a login token (stored here only as a
// salted hash); the wrap key never leaves the browser and encrypts the
// random vault key. Vault items arrive as opaque ciphertext.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/k4ran909/webssh/internal/config"
	"github.com/k4ran909/webssh/internal/store"
)

const cookieName = "webssh_session"

type API struct {
	db      *store.DB
	cfg     config.Config
	log     *slog.Logger
	secret  []byte
	limit   *limiter
	recover *tickets
	signup  sync.Mutex
	now     func() time.Time
}

func New(db *store.DB, cfg config.Config, log *slog.Logger, secret []byte) *API {
	return &API{db: db, cfg: cfg, log: log, secret: secret, limit: newLimiter(), recover: &tickets{m: map[string]ticket{}}, now: time.Now}
}

func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/session", a.session)
	mux.HandleFunc("POST /api/auth/prelogin", a.prelogin)
	mux.HandleFunc("POST /api/auth/signup", a.signupHandler)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.HandleFunc("POST /api/auth/recover/begin", a.recoverBegin)
	mux.HandleFunc("POST /api/auth/recover/finish", a.recoverFinish)
	mux.Handle("POST /api/auth/password", a.RequireUser(http.HandlerFunc(a.changePassword)))
	mux.Handle("POST /api/auth/recovery", a.RequireUser(http.HandlerFunc(a.regenerateRecovery)))
	mux.Handle("POST /api/auth/totp/setup", a.RequireUser(http.HandlerFunc(a.totpSetup)))
	mux.Handle("POST /api/auth/totp/enable", a.RequireUser(http.HandlerFunc(a.totpEnable)))
	mux.Handle("POST /api/auth/totp/disable", a.RequireUser(http.HandlerFunc(a.totpDisable)))
	mux.Handle("GET /api/vault", a.RequireUser(http.HandlerFunc(a.vaultList)))
	mux.Handle("PUT /api/vault/items/{id}", a.RequireUser(http.HandlerFunc(a.vaultPut)))
	mux.Handle("DELETE /api/vault/items/{id}", a.RequireUser(http.HandlerFunc(a.vaultDelete)))
	mux.Handle("GET /api/audit", a.RequireUser(http.HandlerFunc(a.auditList)))
}

// ── request context ──

type ctxKey struct{}

type principal struct {
	user      *store.User
	tokenHash []byte
}

func current(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxKey{}).(*principal)
	return p
}

// UserID returns the signed-in user for requests that passed RequireUser.
func UserID(r *http.Request) (string, bool) {
	if p := current(r.Context()); p != nil {
		return p.user.ID, true
	}
	return "", false
}

func (a *API) lookup(r *http.Request) *principal {
	c, err := r.Cookie(cookieName)
	if err != nil || len(c.Value) < 20 || len(c.Value) > 100 {
		return nil
	}
	h := sha256.Sum256([]byte(c.Value))
	now := a.now()
	s, err := a.db.Session(r.Context(), h[:], now)
	if err != nil {
		return nil
	}
	u, err := a.db.UserByID(r.Context(), s.UserID)
	if err != nil {
		return nil
	}
	// Sliding expiry, written at most hourly.
	if now.Sub(s.LastSeen) > time.Hour {
		_ = a.db.TouchSession(r.Context(), h[:], now, now.Add(a.cfg.SessionTTL))
	}
	return &principal{u, h[:]}
}

func (a *API) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := a.lookup(r)
		if p == nil {
			writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in first.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

// SameOrigin rejects cross-site state-changing API requests. Cookies are
// SameSite=Strict as well; this is the second lock on the door.
func (a *API) SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && strings.HasPrefix(r.URL.Path, "/api/") {
			if o := r.Header.Get("Origin"); o != "" && !a.originAllowed(o, r.Host) {
				writeErr(w, http.StatusForbidden, "BAD_ORIGIN", "Cross-origin request refused.")
				return
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeErr(w, http.StatusForbidden, "BAD_ORIGIN", "Cross-origin request refused.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) originAllowed(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	for _, o := range a.cfg.AllowedOrigins {
		if strings.EqualFold(u.Host, o) {
			return true
		}
	}
	return false
}

// ClientIP is the peer address, or the first X-Forwarded-For hop behind a trusted proxy.
func (a *API) ClientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *API) secureRequest(r *http.Request) bool {
	return r.TLS != nil || (a.cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https")
}

// Record writes an audit event; detail must not contain secrets.
func (a *API) Record(userID, action, ip string, detail any) {
	b, _ := json.Marshal(detail)
	if detail == nil {
		b = []byte("{}")
	}
	if err := a.db.AddAudit(context.Background(), userID, action, ip, string(b)); err != nil {
		a.log.Error("audit write failed", "err", err)
	}
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request, userID string) error {
	token := randomToken()
	h := sha256.Sum256([]byte(token))
	now := a.now()
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	if err := a.db.CreateSession(r.Context(), &store.Session{
		TokenHash: h[:], UserID: userID, CreatedAt: now, LastSeen: now,
		ExpiresAt: now.Add(a.cfg.SessionTTL), IP: a.ClientIP(r), UserAgent: ua,
	}); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: a.secureRequest(r), MaxAge: int(a.cfg.SessionTTL.Seconds()),
	})
	return nil
}

func (a *API) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.secureRequest(r), MaxAge: -1})
}

func randomToken() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := randomBytes(40)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// StartJanitor prunes expired sessions periodically.
func (a *API) StartJanitor(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			_ = a.db.PruneSessions(ctx, a.now())
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}
