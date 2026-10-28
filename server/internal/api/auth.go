package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/k4ran909/webssh/internal/store"
)

// Client-side KDF parameters. The server only validates bounds and hands them back.
type kdfParams struct {
	Alg string `json:"alg"`
	M   int    `json:"m"` // KiB
	T   int    `json:"t"`
	P   int    `json:"p"`
}

var defaultKDF = kdfParams{Alg: "argon2id", M: 65536, T: 3, P: 1}

func (k kdfParams) valid() bool {
	return k.Alg == "argon2id" && k.M >= 19456 && k.M <= 1<<20 && k.T >= 1 && k.T <= 10 && k.P >= 1 && k.P <= 8
}

var emailRe = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]{1,189}$`)

type keysResp struct {
	KDF        json.RawMessage `json:"kdf"`
	Salt       string          `json:"salt"`
	WrappedKey string          `json:"wrappedKey"`
}

type userResp struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	TOTP  bool   `json:"totp"`
}

type sessionResp struct {
	User       *userResp `json:"user"`
	Keys       *keysResp `json:"keys,omitempty"`
	SignupOpen bool      `json:"signupOpen"`
}

func toSession(u *store.User) sessionResp {
	return sessionResp{
		User: &userResp{u.ID, u.Email, u.TOTPSecret != nil},
		Keys: &keysResp{json.RawMessage(u.KDF), u.Salt, u.WrappedKey},
	}
}

func (a *API) signupOpen(r *http.Request) bool {
	if a.cfg.OpenSignup {
		return true
	}
	n, err := a.db.CountUsers(r.Context())
	return err == nil && n == 0
}

func (a *API) session(w http.ResponseWriter, r *http.Request) {
	p := a.lookup(r)
	if p == nil {
		writeJSON(w, http.StatusOK, sessionResp{SignupOpen: a.signupOpen(r)})
		return
	}
	writeJSON(w, http.StatusOK, toSession(p.user))
}

func (a *API) rateLimited(w http.ResponseWriter, r *http.Request, keys ...string) bool {
	now := a.now()
	ok := a.limit.allow("ip:"+a.ClientIP(r), 60, 15*time.Minute, now)
	for _, k := range keys {
		ok = a.limit.allow(k, 10, 15*time.Minute, now) && ok
	}
	if !ok {
		writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many attempts. Wait a few minutes and try again.")
	}
	return !ok
}

// prelogin returns the KDF salt for an email. Unknown emails get a stable
// decoy salt so the endpoint can't be used to discover accounts.
func (a *API) prelogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &req) || a.rateLimited(w, r) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	u, err := a.db.UserByEmail(r.Context(), email)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"kdf": json.RawMessage(u.KDF), "salt": u.Salt})
		return
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte("decoy-salt:" + email))
	writeJSON(w, http.StatusOK, map[string]any{"kdf": defaultKDF, "salt": base64.StdEncoding.EncodeToString(mac.Sum(nil)[:16])})
}

type credentials struct {
	KDF        kdfParams `json:"kdf"`
	Salt       string    `json:"salt"`
	AuthHash   string    `json:"authHash"`
	WrappedKey string    `json:"wrappedKey"`
}

// parse validates password-derived material and returns the server-side hash.
func (c *credentials) parse() (kdf string, authHash, authSalt []byte, err error) {
	if !c.KDF.valid() {
		return "", nil, nil, errors.New("kdf")
	}
	if _, err := b64(c.Salt, 16, 64); err != nil {
		return "", nil, nil, err
	}
	auth, err := b64(c.AuthHash, 32, 32)
	if err != nil {
		return "", nil, nil, err
	}
	if _, err := b64(c.WrappedKey, 40, 128); err != nil {
		return "", nil, nil, err
	}
	k, _ := json.Marshal(c.KDF)
	authSalt = randomBytes(16)
	return string(k), saltedHash(authSalt, auth), authSalt, nil
}

func (a *API) signupHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		credentials
		RecoveryWrappedKey string `json:"recoveryWrappedKey"`
		RecoveryAuth       string `json:"recoveryAuth"`
	}
	if !decode(w, r, &req) || a.rateLimited(w, r) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !emailRe.MatchString(email) {
		writeErr(w, http.StatusBadRequest, "BAD_EMAIL", "Enter a valid email address.")
		return
	}
	kdf, authHash, authSalt, err := req.credentials.parse()
	recAuth, err2 := b64(req.RecoveryAuth, 32, 32)
	_, err3 := b64(req.RecoveryWrappedKey, 40, 128)
	if errors.Join(err, err2, err3) != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "Malformed key material.")
		return
	}

	// Serialize so two racing "first" signups can't both claim the gateway.
	a.signup.Lock()
	defer a.signup.Unlock()
	if !a.signupOpen(r) {
		writeErr(w, http.StatusForbidden, "SIGNUP_CLOSED", "Sign-up is closed on this gateway.")
		return
	}
	recSalt := randomBytes(16)
	u := &store.User{
		ID: newID(), Email: email, KDF: kdf, Salt: req.Salt, AuthHash: authHash, AuthSalt: authSalt,
		WrappedKey: req.WrappedKey, RecoveryWrappedKey: req.RecoveryWrappedKey,
		RecoveryHash: saltedHash(recSalt, recAuth), RecoverySalt: recSalt, CreatedAt: a.now(),
	}
	if err := a.db.CreateUser(r.Context(), u); err != nil {
		if errors.Is(err, store.ErrExists) {
			writeErr(w, http.StatusConflict, "EMAIL_TAKEN", "An account with this email already exists.")
			return
		}
		a.fail(w, err)
		return
	}
	if err := a.startSession(w, r, u.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(u.ID, "account.created", a.ClientIP(r), nil)
	writeJSON(w, http.StatusOK, toSession(u))
}

// dummy work for unknown accounts so timing doesn't reveal which emails exist
var dummySalt = randomBytes(16)

func (a *API) checkAuth(u *store.User, authHash string) bool {
	auth, err := b64(authHash, 32, 32)
	if err != nil {
		auth = make([]byte, 32)
	}
	if u == nil {
		saltedHash(dummySalt, auth)
		return false
	}
	return subtle.ConstantTimeCompare(saltedHash(u.AuthSalt, auth), u.AuthHash) == 1 && err == nil
}

// checkTOTP verifies a code against an enabled secret and burns its time step.
func (a *API) checkTOTP(r *http.Request, u *store.User, code string) (bool, error) {
	secret, err := unseal(a.secret, u.TOTPSecret)
	if err != nil {
		return false, err
	}
	step, ok := verifyTOTP(secret, strings.TrimSpace(code), a.now())
	if !ok {
		return false, nil
	}
	return a.db.UseTOTPStep(r.Context(), u.ID, step)
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		AuthHash string `json:"authHash"`
		TOTP     string `json:"totp,omitempty"`
	}
	if !decode(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if a.rateLimited(w, r, "login:"+email) {
		return
	}
	u, err := a.db.UserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.fail(w, err)
		return
	}
	if !a.checkAuth(u, req.AuthHash) {
		if u != nil {
			a.Record(u.ID, "login.failed", a.ClientIP(r), map[string]string{"reason": "password"})
		}
		writeErr(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Wrong email or password.")
		return
	}
	if u.TOTPSecret != nil {
		if req.TOTP == "" {
			writeErr(w, http.StatusUnauthorized, "TOTP_REQUIRED", "Enter the code from your authenticator app.")
			return
		}
		ok, err := a.checkTOTP(r, u, req.TOTP)
		if err != nil {
			a.fail(w, err)
			return
		}
		if !ok {
			a.Record(u.ID, "login.failed", a.ClientIP(r), map[string]string{"reason": "totp"})
			writeErr(w, http.StatusUnauthorized, "TOTP_INVALID", "That code is wrong or was already used.")
			return
		}
	}
	if err := a.startSession(w, r, u.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(u.ID, "login", a.ClientIP(r), map[string]string{"userAgent": r.UserAgent()})
	writeJSON(w, http.StatusOK, toSession(u))
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if p := a.lookup(r); p != nil {
		_ = a.db.DeleteSession(r.Context(), p.tokenHash)
		a.Record(p.user.ID, "logout", a.ClientIP(r), nil)
	}
	a.clearCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	var req struct {
		CurrentAuthHash string `json:"currentAuthHash"`
		credentials
	}
	if !decode(w, r, &req) || a.rateLimited(w, r, "pw:"+p.user.ID) {
		return
	}
	if !a.checkAuth(p.user, req.CurrentAuthHash) {
		writeErr(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Current password is wrong.")
		return
	}
	kdf, authHash, authSalt, err := req.credentials.parse()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "Malformed key material.")
		return
	}
	if err := a.db.SetCredentials(r.Context(), p.user.ID, kdf, req.Salt, authHash, authSalt, req.WrappedKey); err != nil {
		a.fail(w, err)
		return
	}
	_ = a.db.DeleteUserSessions(r.Context(), p.user.ID, p.tokenHash)
	a.Record(p.user.ID, "password.changed", a.ClientIP(r), nil)
	u, _ := a.db.UserByID(r.Context(), p.user.ID)
	writeJSON(w, http.StatusOK, toSession(u))
}

func (a *API) totpSetup(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	secret := randomBytes(20)
	if err := a.db.SetTOTPPending(r.Context(), p.user.ID, seal(a.secret, secret)); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": b32.EncodeToString(secret), "uri": totpURI(secret, p.user.Email)})
}

func (a *API) totpEnable(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) || a.rateLimited(w, r, "totp:"+p.user.ID) {
		return
	}
	if p.user.TOTPPending == nil {
		writeErr(w, http.StatusBadRequest, "NO_PENDING", "Start two-factor setup first.")
		return
	}
	secret, err := unseal(a.secret, p.user.TOTPPending)
	if err != nil {
		a.fail(w, err)
		return
	}
	step, ok := verifyTOTP(secret, strings.TrimSpace(req.Code), a.now())
	if !ok {
		writeErr(w, http.StatusBadRequest, "TOTP_INVALID", "That code doesn't match. Check the time on your phone.")
		return
	}
	if err := a.db.SetTOTP(r.Context(), p.user.ID, p.user.TOTPPending, step); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(p.user.ID, "totp.enabled", a.ClientIP(r), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"totp": true})
}

func (a *API) totpDisable(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	var req struct {
		AuthHash string `json:"authHash"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &req) || a.rateLimited(w, r, "totp:"+p.user.ID) {
		return
	}
	if p.user.TOTPSecret == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"totp": false})
		return
	}
	if !a.checkAuth(p.user, req.AuthHash) {
		writeErr(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Password is wrong.")
		return
	}
	if ok, err := a.checkTOTP(r, p.user, req.Code); err != nil || !ok {
		writeErr(w, http.StatusUnauthorized, "TOTP_INVALID", "That code is wrong or was already used.")
		return
	}
	if err := a.db.SetTOTP(r.Context(), p.user.ID, nil, 0); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(p.user.ID, "totp.disabled", a.ClientIP(r), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"totp": false})
}

// Recovery: the recovery code (shown once at signup) derives both an auth
// token and the key that wraps a second copy of the vault key. Proving the
// token returns that copy; the browser unwraps it and sets a new password.

func (a *API) recoverBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email        string `json:"email"`
		RecoveryAuth string `json:"recoveryAuth"`
	}
	if !decode(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	now := a.now()
	if !a.limit.allow("recover:"+email, 5, time.Hour, now) || !a.limit.allow("recover-ip:"+a.ClientIP(r), 10, time.Hour, now) {
		writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many attempts. Try again later.")
		return
	}
	u, _ := a.db.UserByEmail(r.Context(), email)
	auth, err := b64(req.RecoveryAuth, 32, 32)
	if u == nil || err != nil || subtle.ConstantTimeCompare(saltedHash(u.RecoverySalt, auth), u.RecoveryHash) != 1 {
		if u != nil {
			a.Record(u.ID, "recovery.failed", a.ClientIP(r), nil)
		}
		writeErr(w, http.StatusUnauthorized, "INVALID_RECOVERY", "Wrong email or recovery code.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"recoveryWrappedKey": u.RecoveryWrappedKey,
		"ticket":             a.recover.issue(u.ID, 10*time.Minute, now),
	})
}

type recoveryMaterial struct {
	RecoveryWrappedKey string `json:"recoveryWrappedKey"`
	RecoveryAuth       string `json:"recoveryAuth"`
}

func (m recoveryMaterial) parse() (wrapped string, hash, salt []byte, err error) {
	auth, err := b64(m.RecoveryAuth, 32, 32)
	if err != nil {
		return "", nil, nil, err
	}
	if _, err := b64(m.RecoveryWrappedKey, 40, 128); err != nil {
		return "", nil, nil, err
	}
	salt = randomBytes(16)
	return m.RecoveryWrappedKey, saltedHash(salt, auth), salt, nil
}

func (a *API) recoverFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket string `json:"ticket"`
		credentials
		// A used recovery code may have been seen; recovery always issues a new one.
		recoveryMaterial
	}
	if !decode(w, r, &req) {
		return
	}
	userID, ok := a.recover.take(req.Ticket, a.now())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "INVALID_TICKET", "Recovery expired. Start again.")
		return
	}
	kdf, authHash, authSalt, err := req.credentials.parse()
	recWrapped, recHash, recSalt, err2 := req.recoveryMaterial.parse()
	if errors.Join(err, err2) != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "Malformed key material.")
		return
	}
	ctx := r.Context()
	if err := a.db.SetCredentials(ctx, userID, kdf, req.Salt, authHash, authSalt, req.WrappedKey); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.db.SetRecovery(ctx, userID, recWrapped, recHash, recSalt); err != nil {
		a.fail(w, err)
		return
	}
	// The recovery code is a full-account secret: it also resets 2FA and
	// signs out every other device.
	_ = a.db.SetTOTP(ctx, userID, nil, 0)
	_ = a.db.DeleteUserSessions(ctx, userID, nil)
	if err := a.startSession(w, r, userID); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(userID, "account.recovered", a.ClientIP(r), nil)
	u, _ := a.db.UserByID(ctx, userID)
	writeJSON(w, http.StatusOK, toSession(u))
}

// regenerateRecovery replaces the recovery code (requires the master password).
func (a *API) regenerateRecovery(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	var req struct {
		AuthHash string `json:"authHash"`
		recoveryMaterial
	}
	if !decode(w, r, &req) || a.rateLimited(w, r, "pw:"+p.user.ID) {
		return
	}
	if !a.checkAuth(p.user, req.AuthHash) {
		writeErr(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Password is wrong.")
		return
	}
	wrapped, hash, salt, err := req.recoveryMaterial.parse()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "Malformed key material.")
		return
	}
	if err := a.db.SetRecovery(r.Context(), p.user.ID, wrapped, hash, salt); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(p.user.ID, "recovery.regenerated", a.ClientIP(r), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) fail(w http.ResponseWriter, err error) {
	a.log.Error("api", "err", err)
	writeErr(w, http.StatusInternalServerError, "INTERNAL", "Something went wrong.")
}
