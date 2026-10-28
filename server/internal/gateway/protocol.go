package gateway

import (
	"errors"
	"regexp"
	"strings"
)

// WebSocket protocol, one socket per terminal session.
//
//	client → server  text   {"t":"connect", host, port, user, auth, knownHostKey, cols, rows, term}
//	                 text   {"t":"resize", cols, rows}
//	                 text   {"t":"hostkey-accept"} | {"t":"hostkey-reject"}
//	                 binary keystrokes / pasted bytes
//	server → client  text   {"t":"hostkey", status:"new"|"changed", fingerprint, algo}
//	                 text   {"t":"ready", fingerprint, algo}
//	                 text   {"t":"exit", exitCode}
//	                 text   {"t":"error", code, msg}
//	                 binary PTY output
type clientMsg struct {
	T            string    `json:"t"`
	Host         string    `json:"host,omitempty"`
	Port         int       `json:"port,omitempty"`
	User         string    `json:"user,omitempty"`
	Auth         *authInfo `json:"auth,omitempty"`
	KnownHostKey string    `json:"knownHostKey,omitempty"`
	Cols         int       `json:"cols,omitempty"`
	Rows         int       `json:"rows,omitempty"`
	Term         string    `json:"term,omitempty"`
}

// authInfo holds the credential for one handshake. It lives only in memory
// for the duration of the connect attempt and must never be logged.
type authInfo struct {
	Type       string `json:"type"` // "password" | "key"
	Password   string `json:"password,omitempty"`
	Key        string `json:"key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

type serverMsg struct {
	T           string `json:"t"`
	Status      string `json:"status,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Algo        string `json:"algo,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	Code        string `json:"code,omitempty"`
	Msg         string `json:"msg,omitempty"`
}

const (
	codeBadRequest      = "BAD_REQUEST"
	codeBusy            = "BUSY"
	codeDNS             = "DNS_FAILED"
	codeBlockedDest     = "BLOCKED_DEST"
	codeUnreachable     = "UNREACHABLE"
	codeHostKeyChanged  = "HOSTKEY_CHANGED"
	codeHostKeyRejected = "HOSTKEY_REJECTED"
	codeAuthFailed      = "AUTH_FAILED"
	codeKeyInvalid      = "KEY_INVALID"
	codeKeyPassphrase   = "KEY_NEEDS_PASSPHRASE"
	codeHandshake       = "HANDSHAKE_FAILED"
	codeSession         = "SESSION_FAILED"
)

// gwError is an error the browser is allowed to see.
type gwError struct{ code, msg string }

func (e *gwError) Error() string { return e.code + ": " + e.msg }

var termRe = regexp.MustCompile(`^[A-Za-z0-9.+-]{1,64}$`)

func (m *clientMsg) validate() error {
	m.Host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(m.Host), "["), "]")
	if m.Host == "" || len(m.Host) > 253 || strings.ContainsAny(m.Host, " \t\r\n/\\@") {
		return errors.New("invalid host")
	}
	if m.Port == 0 {
		m.Port = 22
	}
	if m.Port < 1 || m.Port > 65535 {
		return errors.New("invalid port")
	}
	if m.User == "" || len(m.User) > 255 || strings.ContainsAny(m.User, "\x00\r\n") {
		return errors.New("invalid username")
	}
	if m.Auth == nil {
		return errors.New("missing credentials")
	}
	switch m.Auth.Type {
	case "password":
		if m.Auth.Password == "" {
			return errors.New("missing password")
		}
	case "key":
		if m.Auth.Key == "" {
			return errors.New("missing private key")
		}
	default:
		return errors.New("unknown auth type")
	}
	if m.Term == "" {
		m.Term = "xterm-256color"
	} else if !termRe.MatchString(m.Term) {
		return errors.New("invalid terminal type")
	}
	if !validSize(m.Cols, m.Rows) {
		m.Cols, m.Rows = 80, 24
	}
	return nil
}

func validSize(cols, rows int) bool {
	return cols > 0 && cols <= 1000 && rows > 0 && rows <= 500
}
