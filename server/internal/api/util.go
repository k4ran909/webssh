package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxBody = 512 << 10

type apiError struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{code, msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "Malformed request.")
		return false
	}
	return true
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the OS RNG failing is not recoverable
	}
	return b
}

func newID() string { return hex.EncodeToString(randomBytes(16)) }

func saltedHash(salt, secret []byte) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write(secret)
	return h.Sum(nil)
}

// b64 decodes a standard base64 field and checks its decoded length.
func b64(s string, minLen, maxLen int) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) < minLen || len(b) > maxLen {
		return nil, errors.New("invalid encoding")
	}
	return b, nil
}

// LoadSecret reads the server secret (used to encrypt TOTP seeds and derive
// decoy salts), creating it on first run. It lives outside the database so a
// leaked database alone does not expose TOTP seeds.
func LoadSecret(dir string) ([]byte, error) {
	path := filepath.Join(dir, "server.key")
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("%s: expected 32 bytes", path)
		}
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b := randomBytes(32)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}

func seal(key, plain []byte) []byte {
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := randomBytes(gcm.NonceSize())
	return gcm.Seal(nonce, nonce, plain, nil)
}

func unseal(key, data []byte) ([]byte, error) {
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if len(data) < gcm.NonceSize() {
		return nil, io.ErrUnexpectedEOF
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}

// limiter is a fixed-window counter keyed by IP or email.
type limiter struct {
	mu sync.Mutex
	m  map[string]*window
}

type window struct {
	reset time.Time
	n     int
}

func newLimiter() *limiter { return &limiter{m: map[string]*window{}} }

func (l *limiter) allow(key string, max int, per time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) > 50_000 {
		for k, w := range l.m {
			if now.After(w.reset) {
				delete(l.m, k)
			}
		}
	}
	w := l.m[key]
	if w == nil || now.After(w.reset) {
		w = &window{reset: now.Add(per)}
		l.m[key] = w
	}
	w.n++
	return w.n <= max
}

// tickets are short-lived, single-use tokens (account recovery).
type tickets struct {
	mu sync.Mutex
	m  map[string]ticket
}

type ticket struct {
	userID string
	exp    time.Time
}

func (t *tickets) issue(userID string, ttl time.Duration, now time.Time) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if now.After(v.exp) {
			delete(t.m, k)
		}
	}
	id := base64.RawURLEncoding.EncodeToString(randomBytes(24))
	t.m[id] = ticket{userID, now.Add(ttl)}
	return id
}

func (t *tickets) take(id string, now time.Time) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[id]
	delete(t.m, id)
	if !ok || now.After(v.exp) {
		return "", false
	}
	return v.userID, true
}
