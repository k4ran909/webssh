// Package sshcore is the SSH client shared by the gateway and the in-browser
// (WebAssembly) client used for end-to-end sessions: the terminal protocol
// spoken with the browser, multi-hop dialing with host key checks, auth, and
// shell setup. Both sides speak the exact same messages, so the frontend
// can't tell them apart.
package sshcore

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Terminal protocol, one socket per terminal session.
//
//	client → server  text   {"t":"connect", host, port, user, auth, knownHostKey, jump:[hop…], agent, cols, rows, term}
//	                 text   {"t":"resume", token, offset, cols, rows}   (first message on a new socket)
//	                 text   {"t":"resize", cols, rows}
//	                 text   {"t":"hostkey-accept"} | {"t":"hostkey-reject"}
//	                 binary keystrokes / pasted bytes
//	server → client  text   {"t":"hostkey", status:"new"|"changed", host, port, fingerprint, algo}
//	                 text   {"t":"ready", fingerprint, algo, sftp, resume}
//	                 text   {"t":"resumed", offset, gap, sftp}   then the missed output from offset
//	                 text   {"t":"exit", exitCode}
//	                 text   {"t":"error", code, msg}
//	                 binary PTY output
//
// jump lists bastions in order (ProxyJump); each hop is verified and
// authenticated on its own. sftp is a handle for the file API that reuses
// this session's SSH connection.
type ClientMsg struct {
	T string `json:"t"`
	// Proto is "" (SSH) or "telnet"; telnet has no credentials or jump hosts.
	Proto        string `json:"proto,omitempty"`
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
	User         string `json:"user,omitempty"`
	Auth         *Auth  `json:"auth,omitempty"`
	KnownHostKey string `json:"knownHostKey,omitempty"`
	Jump         []Hop  `json:"jump,omitempty"`
	Agent        bool   `json:"agent,omitempty"`
	Cols         int    `json:"cols,omitempty"`
	Rows         int    `json:"rows,omitempty"`
	Term         string `json:"term,omitempty"`
	// resume: token from "ready", and how many output bytes arrived since "ready".
	Token  string `json:"token,omitempty"`
	Offset int64  `json:"offset,omitempty"`
}

func DecodeMsg(b []byte, m *ClientMsg) bool { return json.Unmarshal(b, m) == nil }

type Hop struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	Auth         *Auth  `json:"auth"`
	KnownHostKey string `json:"knownHostKey,omitempty"`
}

const MaxJumps = 4

// Auth holds the credential for one handshake. It lives only in memory for
// the duration of the connect attempt and must never be logged.
type Auth struct {
	Type       string `json:"type"` // "password" | "key"
	Password   string `json:"password,omitempty"`
	Key        string `json:"key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

type ServerMsg struct {
	T           string `json:"t"`
	Status      string `json:"status,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	SFTP        string `json:"sftp,omitempty"`
	Resume      string `json:"resume,omitempty"`
	Offset      int64  `json:"offset,omitempty"`
	Gap         bool   `json:"gap,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Algo        string `json:"algo,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	Code        string `json:"code,omitempty"`
	Msg         string `json:"msg,omitempty"`
}

const (
	CodeBadRequest      = "BAD_REQUEST"
	CodeBusy            = "BUSY"
	CodeDNS             = "DNS_FAILED"
	CodeBlockedDest     = "BLOCKED_DEST"
	CodeUnreachable     = "UNREACHABLE"
	CodeHostKeyChanged  = "HOSTKEY_CHANGED"
	CodeHostKeyRejected = "HOSTKEY_REJECTED"
	CodeAuthFailed      = "AUTH_FAILED"
	CodeKeyInvalid      = "KEY_INVALID"
	CodeKeyPassphrase   = "KEY_NEEDS_PASSPHRASE"
	CodeHandshake       = "HANDSHAKE_FAILED"
	CodeSession         = "SESSION_FAILED"
	CodeResumeGone      = "RESUME_GONE"
)

// Error is an error the browser is allowed to see.
type Error struct{ Code, Msg string }

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// AsError turns any error into one that is safe to show, with fallback as its code.
func AsError(err error, fallback string) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{fallback, err.Error()}
}

var termRe = regexp.MustCompile(`^[A-Za-z0-9.+-]{1,64}$`)

// validHostPort normalises and checks a host and port (0 means def).
func validHostPort(host *string, port *int, def int) error {
	*host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(*host), "["), "]")
	if *host == "" || len(*host) > 253 || strings.ContainsAny(*host, " \t\r\n/\\@") {
		return errors.New("invalid host")
	}
	if *port == 0 {
		*port = def
	}
	if *port < 1 || *port > 65535 {
		return errors.New("invalid port")
	}
	return nil
}

func (h *Hop) Validate() error {
	if err := validHostPort(&h.Host, &h.Port, 22); err != nil {
		return err
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

// Target is the final destination as a hop.
func (m *ClientMsg) Target() *Hop {
	return &Hop{Host: m.Host, Port: m.Port, User: m.User, Auth: m.Auth, KnownHostKey: m.KnownHostKey}
}

// Hops lists every hop in dialing order: jump hosts, then the target.
func (m *ClientMsg) Hops() []Hop { return append(append([]Hop{}, m.Jump...), *m.Target()) }

// DropCredentials forgets every password and key once the handshakes are over.
func (m *ClientMsg) DropCredentials() {
	m.Auth = nil
	for i := range m.Jump {
		m.Jump[i].Auth = nil
	}
}

func (m *ClientMsg) Validate() error {
	switch m.Proto {
	case "":
	case "telnet":
		if err := validHostPort(&m.Host, &m.Port, 23); err != nil {
			return err
		}
		m.Auth, m.Jump, m.Agent = nil, nil, false
		return m.validTerm()
	default:
		return errors.New("unknown protocol")
	}
	t := m.Target()
	if err := t.Validate(); err != nil {
		return err
	}
	m.Host, m.Port = t.Host, t.Port
	if len(m.Jump) > MaxJumps {
		return errors.New("too many jump hosts")
	}
	for i := range m.Jump {
		if err := m.Jump[i].Validate(); err != nil {
			return errors.New("jump host: " + err.Error())
		}
	}
	return m.validTerm()
}

func (m *ClientMsg) validTerm() error {
	if m.Term == "" {
		m.Term = "xterm-256color"
	} else if !termRe.MatchString(m.Term) {
		return errors.New("invalid terminal type")
	}
	if !ValidSize(m.Cols, m.Rows) {
		m.Cols, m.Rows = 80, 24
	}
	return nil
}

func ValidSize(cols, rows int) bool {
	return cols > 0 && cols <= 1000 && rows > 0 && rows <= 500
}

func ToCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}
