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
	// AllowPrivate lets users connect to loopback, private and link-local
	// targets. Defaults to true only when Addr is a loopback address.
	AllowPrivate bool
	// MaxSessions caps concurrent SSH sessions on this gateway.
	MaxSessions int
	// AllowedOrigins are extra WebSocket origin patterns (host[:port]).
	// Same-origin requests are always allowed.
	AllowedOrigins []string
	DialTimeout    time.Duration
}

func FromEnv() (Config, error) {
	c := Config{
		Addr:        getenv("WEBSSH_ADDR", "127.0.0.1:7022"),
		MaxSessions: 20,
		DialTimeout: 10 * time.Second,
	}

	loopback := IsLoopback(c.Addr)
	// Phase 0 has no login. Anyone who can reach the port can open SSH
	// sessions through it, so refuse non-loopback binds unless forced.
	if !loopback && os.Getenv("WEBSSH_ALLOW_PUBLIC_NO_AUTH") != "1" {
		return c, fmt.Errorf("refusing to listen on %q: this build has no login, bind to 127.0.0.1 "+
			"or set WEBSSH_ALLOW_PUBLIC_NO_AUTH=1 if you really mean it", c.Addr)
	}

	c.AllowPrivate = loopback
	if v := os.Getenv("WEBSSH_ALLOW_PRIVATE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("WEBSSH_ALLOW_PRIVATE: %w", err)
		}
		c.AllowPrivate = b
	}
	if v := os.Getenv("WEBSSH_MAX_SESSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return c, errors.New("WEBSSH_MAX_SESSIONS must be a positive integer")
		}
		c.MaxSessions = n
	}
	if v := os.Getenv("WEBSSH_ALLOWED_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.AllowedOrigins = append(c.AllowedOrigins, o)
			}
		}
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
