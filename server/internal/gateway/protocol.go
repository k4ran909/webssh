package gateway

import (
	"errors"
	"regexp"
	"strings"
)

// WebSocket protocol, one socket per terminal session.
//
//	client → server  text   {"t":"connect", host, port, user, auth, knownHostKey, jump:[hop…], cols, rows, term}
//	                 text   {"t":"resize", cols, rows}
//	                 text   {"t":"hostkey-accept"} | {"t":"hostkey-reject"}
//	                 binary keystrokes / pasted bytes
//	server → client  text   {"t":"hostkey", status:"new"|"changed", host, port, fingerprint, algo}
//	                 text   {"t":"ready", fingerprint, algo, sftp}
//	                 text   {"t":"exit", exitCode}
//	                 text   {"t":"error", code, msg}
//	                 binary PTY output
//
// jump lists bastions in order (ProxyJump); each hop is verified and
// authenticated on its own. sftp is a handle for /api/sftp/{handle}/… that
// reuses this session's SSH connection.
type clientMsg struct {
	T            string    `json:"t"`
	Host         string    `json:"host,omitempty"`
	Port         int       `json:"port,omitempty"`
	User         string    `json:"user,omitempty"`
	Auth         *authInfo `json:"auth,omitempty"`
	KnownHostKey string    `json:"knownHostKey,omitempty"`
	Jump         []hop     `json:"jump,omitempty"`
	Cols         int       `json:"cols,omitempty"`
	Rows         int       `json:"rows,omitempty"`
	Term         string    `json:"term,omitempty"`
}

type hop struct {
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	User         string    `json:"user"`
	Auth         *authInfo `json:"auth"`
	KnownHostKey string    `json:"knownHostKey,omitempty"`
}

const maxJumps = 4

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
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	SFTP        string `json:"sftp,omitempty"`
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

func (h *hop) validate() error {
	h.Host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(h.Host), "["), "]")
	if h.Host == "" || len(h.Host) > 253 || strings.ContainsAny(h.Host, " \t\r\n/\\@") {
		return errors.New("invalid host")
	}
	if h.Port == 0 {
		h.Port = 22
	}
	if h.Port < 1 || h.Port > 65535 {
		return errors.New("invalid port")
	}
	if h.User == "" || len(h.User) > 255 || strings.ContainsAny(h.User, "\x00\r\n") {
		return errors.New("invalid username")
	}
	if h.Auth == nil {
		return errors.New("missing credentials")
	}
	switch h.Auth.Type {
	case "password":
		if h.Auth.Password == "" {
			return errors.New("missing password")
		}
	case "key":
		if h.Auth.Key == "" {
			return errors.New("missing private key")
		}
	default:
		return errors.New("unknown auth type")
	}
	return nil
}

// target is the final destination as a hop.
func (m *clientMsg) target() *hop {
	return &hop{Host: m.Host, Port: m.Port, User: m.User, Auth: m.Auth, KnownHostKey: m.KnownHostKey}
}

func (m *clientMsg) validate() error {
	t := m.target()
	if err := t.validate(); err != nil {
		return err
	}
	m.Host, m.Port = t.Host, t.Port
	if len(m.Jump) > maxJumps {
		return errors.New("too many jump hosts")
	}
	for i := range m.Jump {
		if err := m.Jump[i].validate(); err != nil {
			return errors.New("jump host: " + err.Error())
		}
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
