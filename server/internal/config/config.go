// Package config loads gateway settings from the environment.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Addr is the HTTP listen address.
	Addr string
	// DataDir holds the SQLite database and the server secret.
	DataDir string
	// AllowPrivate lets users connect to loopback, private and link-local
	// targets. Defaults to true only when Addr is a loopback address.
	AllowPrivate bool
	// OpenSignup lets anyone create an account. When false, only the first
	// account can be created (it owns this self-hosted gateway).
	OpenSignup bool
	// TrustProxy honours X-Forwarded-For / X-Forwarded-Proto from a reverse proxy.
	TrustProxy bool
	// MaxSessions caps concurrent SSH sessions on this gateway; MaxUserSessions per account.
	MaxSessions     int
	MaxUserSessions int
	// AllowedOrigins are extra origin patterns (host[:port]) for the API and
	// WebSocket. Same-origin requests are always allowed.
	AllowedOrigins []string
	SessionTTL     time.Duration
	DialTimeout    time.Duration
}

func FromEnv() (Config, error) {
	c := Config{
		Addr:            getenv("WEBSSH_ADDR", "127.0.0.1:7022"),
		DataDir:         getenv("WEBSSH_DATA_DIR", "data"),
		MaxSessions:     50,
		MaxUserSessions: 20,
		SessionTTL:      14 * 24 * time.Hour,
		DialTimeout:     10 * time.Second,
	}

	var err error
	c.AllowPrivate, err = envBool("WEBSSH_ALLOW_PRIVATE", IsLoopback(c.Addr))
	if err != nil {
		return c, err
	}
	if c.OpenSignup, err = envBool("WEBSSH_OPEN_SIGNUP", false); err != nil {
		return c, err
	}
	if c.TrustProxy, err = envBool("WEBSSH_TRUST_PROXY", false); err != nil {
		return c, err
	}
	for k, dst := range map[string]*int{"WEBSSH_MAX_SESSIONS": &c.MaxSessions, "WEBSSH_MAX_USER_SESSIONS": &c.MaxUserSessions} {
		if v := os.Getenv(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return c, fmt.Errorf("%s must be a positive integer", k)
			}
			*dst = n
		}
	}
	if v := os.Getenv("WEBSSH_ALLOWED_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.AllowedOrigins = append(c.AllowedOrigins, o)
			}
		}
	}
	if c.DataDir == "" {
		return c, errors.New("WEBSSH_DATA_DIR must not be empty")
	}
	return c, nil
}

// IsLoopback reports whether a listen address only accepts local connections.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) (bool, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", k, err)
	}
	return b, nil
}
