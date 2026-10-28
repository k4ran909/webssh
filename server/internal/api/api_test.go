package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/k4ran909/webssh/internal/config"
	"github.com/k4ran909/webssh/internal/store"
)

// The server treats all key material as opaque, so tests use random bytes
// in place of real Argon2/HKDF output.

type env struct {
	t   *testing.T
	srv *httptest.Server
	api *API
}

func newEnv(t *testing.T, mut ...func(*config.Config)) *env {
	t.Helper()
	cfg := config.Config{SessionTTL: time.Hour, MaxSessions: 4, MaxUserSessions: 4}
	for _, m := range mut {
		m(&cfg)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), randomBytes(32))
	mux := http.NewServeMux()
	a.Routes(mux)
	srv := httptest.NewServer(a.SameOrigin(mux))
	t.Cleanup(srv.Close)
	return &env{t, srv, a}
}

type client struct {
	e    *env
	http *http.Client
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e, &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any, out any, headers ...string) int {
	c.e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func rnd(n int) string { return base64.StdEncoding.EncodeToString(randomBytes(n)) }

type account struct {
	email, authHash, recoveryAuth string
}

func (c *client) signup(email string) (account, int) {
	acct := account{email, rnd(32), rnd(32)}
	code := c.do("POST", "/api/auth/signup", map[string]any{
		"email": email, "kdf": defaultKDF, "salt": rnd(16), "authHash": acct.authHash,
		"wrappedKey": rnd(60), "recoveryWrappedKey": rnd(60), "recoveryAuth": acct.recoveryAuth,
	}, nil)
	return acct, code
}

func (c *client) session() sessionResp {
	var s sessionResp
	c.do("GET", "/api/session", nil, &s)
	return s
}

func TestSignupLoginAndVault(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	if s := c.session(); s.User != nil || !s.SignupOpen {
		t.Fatalf("fresh gateway: want signed out with signup open, got %+v", s)
	}
	acct, code := c.signup("Alice@Example.com")
	if code != 200 {
		t.Fatalf("signup: %d", code)
	}
	if s := c.session(); s.User == nil || s.User.Email != "alice@example.com" || s.Keys == nil {
		t.Fatalf("after signup: %+v", s)
	}

	// vault round trip
	var put struct{ Revision int64 }
	if c.do("PUT", "/api/vault/items/item-0001", map[string]string{"data": rnd(80)}, &put); put.Revision != 1 {
		t.Fatalf("first put revision = %d", put.Revision)
	}
	c.do("PUT", "/api/vault/items/item-0002", map[string]string{"data": rnd(80)}, &put)
	var list struct {
		Items    []store.Item
		Revision int64
	}
	c.do("GET", "/api/vault?since=0", nil, &list)
	if len(list.Items) != 2 || list.Revision != 2 {
		t.Fatalf("list: %+v", list)
	}
	c.do("DELETE", "/api/vault/items/item-0001", nil, &put)
	list.Items = nil // json reuses slice elements, which would keep the old Data
	c.do("GET", "/api/vault?since=2", nil, &list)
	if len(list.Items) != 1 || !list.Items[0].Deleted || list.Items[0].Data != "" {
		t.Fatalf("delta after delete should be one tombstone: %+v", list.Items)
	}
	if code := c.do("PUT", "/api/vault/items/bad id!", map[string]string{"data": rnd(80)}, nil); code != 400 && code != 404 {
		t.Fatalf("bad id accepted: %d", code)
	}

	// logout, then log back in
	c.do("POST", "/api/auth/logout", nil, nil)
	if s := c.session(); s.User != nil {
		t.Fatal("still signed in after logout")
	}
	if code := c.do("GET", "/api/vault", nil, nil); code != 401 {
		t.Fatalf("vault without session: %d", code)
	}
	var errResp apiError
	if code := c.do("POST", "/api/auth/login", map[string]string{"email": acct.email, "authHash": rnd(32)}, &errResp); code != 401 || errResp.Code != "INVALID_CREDENTIALS" {
		t.Fatalf("wrong password: %d %+v", code, errResp)
	}
	if code := c.do("POST", "/api/auth/login", map[string]string{"email": "ALICE@example.com", "authHash": acct.authHash}, nil); code != 200 {
		t.Fatalf("login: %d", code)
	}
	if s := c.session(); s.User == nil {
		t.Fatal("not signed in after login")
	}
}

func TestSignupClosesAfterFirstAccount(t *testing.T) {
	e := newEnv(t)
	if _, code := e.client().signup("owner@example.com"); code != 200 {
		t.Fatal(code)
	}
	c2 := e.client()
	if s := c2.session(); s.SignupOpen {
		t.Fatal("signup still advertised as open")
	}
	if _, code := c2.signup("intruder@example.com"); code != 403 {
		t.Fatalf("second signup: want 403, got %d", code)
	}

	open := newEnv(t, func(c *config.Config) { c.OpenSignup = true })
	open.client().signup("a@example.com")
	if _, code := open.client().signup("b@example.com"); code != 200 {
		t.Fatalf("open signup: %d", code)
	}
	if _, code := open.client().signup("b@example.com"); code != 409 {
		t.Fatalf("duplicate email: want 409, got %d", code)
	}
}

func TestPreloginDoesNotRevealAccounts(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.signup("real@example.com")
	var a, b struct{ Salt string }
	c.do("POST", "/api/auth/prelogin", map[string]string{"email": "ghost@example.com"}, &a)
	c.do("POST", "/api/auth/prelogin", map[string]string{"email": "ghost@example.com"}, &b)
	if a.Salt == "" || a.Salt != b.Salt {
		t.Fatalf("decoy salt must be stable: %q %q", a.Salt, b.Salt)
	}
}

func TestTOTP(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	acct, _ := c.signup("t@example.com")

	var setup struct{ Secret, URI string }
	c.do("POST", "/api/auth/totp/setup", map[string]string{}, &setup)
	secret, err := b32.DecodeString(setup.Secret)
	if err != nil || len(secret) != 20 {
		t.Fatalf("setup secret: %v", err)
	}
	now := time.Now()
	if code := c.do("POST", "/api/auth/totp/enable", map[string]string{"code": "000000"}, nil); code != 400 {
		t.Fatalf("wrong enable code: %d", code)
	}
	step := now.Unix() / totpPeriod
	if code := c.do("POST", "/api/auth/totp/enable", map[string]string{"code": totpCode(secret, step)}, nil); code != 200 {
		t.Fatalf("enable: %d", code)
	}

	c2 := e.client()
	var errResp apiError
	c2.do("POST", "/api/auth/login", map[string]string{"email": acct.email, "authHash": acct.authHash}, &errResp)
	if errResp.Code != "TOTP_REQUIRED" {
		t.Fatalf("want TOTP_REQUIRED, got %+v", errResp)
	}
	// The step used to enable is burned; the next one works once.
	c2.do("POST", "/api/auth/login", map[string]string{"email": acct.email, "authHash": acct.authHash, "totp": totpCode(secret, step)}, &errResp)
	if errResp.Code != "TOTP_INVALID" {
		t.Fatalf("replayed code accepted: %+v", errResp)
	}
	next := totpCode(secret, step+1)
	if code := c2.do("POST", "/api/auth/login", map[string]string{"email": acct.email, "authHash": acct.authHash, "totp": next}, nil); code != 200 {
		t.Fatalf("login with next code: %d", code)
	}
	if code := e.client().do("POST", "/api/auth/login", map[string]string{"email": acct.email, "authHash": acct.authHash, "totp": next}, nil); code != 401 {
		t.Fatalf("same code twice: %d", code)
	}
}

func TestRecovery(t *testing.T) {
	e := newEnv(t)
	owner := e.client()
	acct, _ := owner.signup("r@example.com")

	c := e.client()
	if code := c.do("POST", "/api/auth/recover/begin", map[string]string{"email": acct.email, "recoveryAuth": rnd(32)}, nil); code != 401 {
		t.Fatalf("wrong recovery code: %d", code)
	}
	var begin struct{ RecoveryWrappedKey, Ticket string }
	if code := c.do("POST", "/api/auth/recover/begin", map[string]string{"email": acct.email, "recoveryAuth": acct.recoveryAuth}, &begin); code != 200 || begin.Ticket == "" {
		t.Fatalf("recover begin: %d", code)
	}
	newAuth, newRecovery := rnd(32), rnd(32)
	finish := map[string]any{"ticket": begin.Ticket, "kdf": defaultKDF, "salt": rnd(16), "authHash": newAuth, "wrappedKey": rnd(60),
		"recoveryWrappedKey": rnd(60), "recoveryAuth": newRecovery}
	if code := c.do("POST", "/api/auth/recover/finish", finish, nil); code != 200 {
		t.Fatalf("recover finish: %d", code)
	}
	// The used recovery code is retired; the new one works.
	if code := e.client().do("POST", "/api/auth/recover/begin", map[string]string{"email": acct.email, "recoveryAuth": acct.recoveryAuth}, nil); code != 401 {
		t.Fatalf("old recovery code still valid: %d", code)
	}
	if code := e.client().do("POST", "/api/auth/recover/begin", map[string]string{"email": acct.email, "recoveryAuth": newRecovery}, nil); code != 200 {
		t.Fatalf("new recovery code rejected: %d", code)
	}
	if code := c.do("POST", "/api/auth/recover/finish", finish, nil); code != 401 {
		t.Fatalf("ticket reused: %d", code)
	}
	if s := owner.session(); s.User != nil {
		t.Fatal("other sessions must be signed out after recovery")
	}
	if code := e.client().do("POST", "/api/auth/login", map[string]string{"email": acct.email, "authHash": acct.authHash}, nil); code != 401 {
		t.Fatal("old password still works")
	}
	if code := e.client().do("POST", "/api/auth/login", map[string]string{"email": acct.email, "authHash": newAuth}, nil); code != 200 {
		t.Fatal("new password rejected")
	}
}

func TestChangePasswordSignsOutOthers(t *testing.T) {
	e := newEnv(t)
	a := e.client()
	acct, _ := a.signup("p@example.com")
	b := e.client()
	b.do("POST", "/api/auth/login", map[string]string{"email": acct.email, "authHash": acct.authHash}, nil)

	newAuth := rnd(32)
	body := map[string]any{"currentAuthHash": rnd(32), "kdf": defaultKDF, "salt": rnd(16), "authHash": newAuth, "wrappedKey": rnd(60)}
	if code := a.do("POST", "/api/auth/password", body, nil); code != 401 {
		t.Fatalf("wrong current password accepted: %d", code)
	}
	body["currentAuthHash"] = acct.authHash
	if code := a.do("POST", "/api/auth/password", body, nil); code != 200 {
		t.Fatalf("change password: %d", code)
	}
	if a.session().User == nil {
		t.Fatal("current session should survive")
	}
	if b.session().User != nil {
		t.Fatal("other session should be signed out")
	}
}

func TestCrossOriginRefused(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	if code := c.do("POST", "/api/auth/prelogin", map[string]string{"email": "x@example.com"}, nil, "Origin", "https://evil.example"); code != 403 {
		t.Fatalf("cross-origin POST: want 403, got %d", code)
	}
	if code := c.do("POST", "/api/auth/prelogin", map[string]string{"email": "x@example.com"}, nil, "Origin", e.srv.URL); code != 200 {
		t.Fatalf("same-origin POST: %d", code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	e.client().signup("rl@example.com")
	c := e.client()
	last := 0
	for i := 0; i < 11; i++ {
		last = c.do("POST", "/api/auth/login", map[string]string{"email": "rl@example.com", "authHash": rnd(32)}, nil)
	}
	if last != 429 {
		t.Fatalf("11th attempt: want 429, got %d", last)
	}
}

func TestAuditRecorded(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.signup("audit@example.com")
	var out struct{ Events []store.AuditEvent }
	c.do("GET", "/api/audit", nil, &out)
	if len(out.Events) == 0 || out.Events[0].Action != "account.created" {
		t.Fatalf("audit: %+v", out.Events)
	}
}

func TestTOTPVectors(t *testing.T) {
	// RFC 6238 appendix B, SHA-1, truncated to 6 digits.
	secret := []byte("12345678901234567890")
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := totpCode(secret, ts/totpPeriod); got != want {
			t.Errorf("t=%d: got %s want %s", ts, got, want)
		}
	}
}
