// Package gateway bridges browser WebSockets to SSH sessions.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/k4ran909/webssh/internal/config"
)

const (
	connectTimeout   = 10 * time.Second
	handshakeTimeout = 2 * time.Minute // includes the user reading the host key prompt
	hostKeyTimeout   = 90 * time.Second
	keepaliveEvery   = 30 * time.Second
	maxFrameBytes    = 1 << 20
	outputChunk      = 32 << 10
)

// Hooks connect the gateway to accounts and the audit log without importing them.
type Hooks struct {
	// User identifies the signed-in account; requests without one are refused.
	User     func(*http.Request) (string, bool)
	ClientIP func(*http.Request) string
	// Audit records metadata only (never keystrokes or credentials).
	Audit func(userID, action, ip string, detail any)
}

type Gateway struct {
	cfg   config.Config
	log   *slog.Logger
	hooks Hooks
	slots chan struct{}

	mu      sync.Mutex
	perUser map[string]int
}

func New(cfg config.Config, log *slog.Logger, hooks Hooks) *Gateway {
	if hooks.ClientIP == nil {
		hooks.ClientIP = func(r *http.Request) string { return r.RemoteAddr }
	}
	if hooks.Audit == nil {
		hooks.Audit = func(string, string, string, any) {}
	}
	return &Gateway{cfg: cfg, log: log, hooks: hooks, slots: make(chan struct{}, cfg.MaxSessions), perUser: map[string]int{}}
}

func (g *Gateway) claim(userID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.perUser[userID] >= g.cfg.MaxUserSessions {
		return false
	}
	g.perUser[userID]++
	return true
}

func (g *Gateway) release(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.perUser[userID]--; g.perUser[userID] <= 0 {
		delete(g.perUser, userID)
	}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := g.hooks.User(r)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	// Same-origin is always allowed; anything else must be listed explicitly.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: g.cfg.AllowedOrigins})
	if err != nil {
		g.log.Warn("websocket rejected", "origin", r.Header.Get("Origin"), "err", err)
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxFrameBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	ip := g.hooks.ClientIP(r)
	s := &session{
		ws:     ws,
		cfg:    g.cfg,
		log:    g.log.With("remote", ip, "account", userID),
		in:     make(chan frame, 64),
		userID: userID,
		ip:     ip,
		audit:  g.hooks.Audit,
	}
	go s.readLoop(ctx, cancel)

	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		s.fail(ctx, codeBusy, "Too many open sessions on this gateway. Try again later.")
		ws.Close(websocket.StatusTryAgainLater, "busy")
		return
	}
	if !g.claim(userID) {
		s.fail(ctx, codeBusy, fmt.Sprintf("You already have %d open sessions. Close one first.", g.cfg.MaxUserSessions))
		ws.Close(websocket.StatusTryAgainLater, "busy")
		return
	}
	defer g.release(userID)

	status, reason := s.run(ctx)
	ws.Close(status, reason)
}

type frame struct {
	typ  websocket.MessageType
	data []byte
}

type session struct {
	ws     *websocket.Conn
	cfg    config.Config
	log    *slog.Logger
	in     chan frame
	userID string
	ip     string
	audit  func(userID, action, ip string, detail any)

	bytesIn, bytesOut atomic.Int64

	// Set by the host key callback during the handshake.
	fingerprint string
	hostKeyAlgo string
	hostKeyErr  *gwError
}

// readLoop is the only reader of the socket; coder/websocket also needs it
// running to process pings and the close handshake.
func (s *session) readLoop(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	defer close(s.in)
	for {
		typ, data, err := s.ws.Read(ctx)
		if err != nil {
			return
		}
		select {
		case s.in <- frame{typ, data}:
		case <-ctx.Done():
			return
		}
	}
}

func (s *session) send(ctx context.Context, m serverMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.ws.Write(wctx, websocket.MessageText, b)
}

func (s *session) fail(ctx context.Context, code, msg string) {
	_ = s.send(ctx, serverMsg{T: "error", Code: code, Msg: msg})
}

func (s *session) run(ctx context.Context) (websocket.StatusCode, string) {
	req, err := s.awaitConnect(ctx)
	if err != nil {
		s.fail(ctx, codeBadRequest, err.Error())
		return websocket.StatusPolicyViolation, "bad connect request"
	}
	log := s.log.With("user", req.User, "host", req.Host, "port", req.Port)
	dest := map[string]any{"host": req.Host, "port": req.Port, "user": req.User}

	client, err := s.dial(ctx, req)
	req.Auth = nil // drop the credential as soon as the handshake is over
	if err != nil {
		var ge *gwError
		if !errors.As(err, &ge) {
			ge = &gwError{codeHandshake, err.Error()}
		}
		log.Info("ssh connect failed", "code", ge.code)
		dest["result"] = ge.code
		s.audit(s.userID, "ssh.connect", s.ip, dest)
		s.fail(ctx, ge.code, ge.msg)
		return websocket.StatusNormalClosure, ge.code
	}
	defer client.Close()
	// Closing the client unblocks any stdin write stuck on a full SSH window.
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()

	log.Info("ssh session opened", "fingerprint", s.fingerprint)
	dest["result"], dest["fingerprint"] = "ok", s.fingerprint
	s.audit(s.userID, "ssh.connect", s.ip, dest)
	started := time.Now()
	defer func() {
		log.Info("ssh session closed")
		s.audit(s.userID, "ssh.session", s.ip, map[string]any{
			"host": req.Host, "port": req.Port, "user": req.User,
			"seconds": int(time.Since(started).Seconds()), "bytesIn": s.bytesIn.Load(), "bytesOut": s.bytesOut.Load(),
		})
	}()
	return s.shell(ctx, client, req)
}

func (s *session) awaitConnect(ctx context.Context) (*clientMsg, error) {
	t := time.NewTimer(connectTimeout)
	defer t.Stop()
	select {
	case f, ok := <-s.in:
		if !ok {
			return nil, errors.New("client went away")
		}
		var m clientMsg
		if f.typ != websocket.MessageText || json.Unmarshal(f.data, &m) != nil || m.T != "connect" {
			return nil, errors.New("expected a connect message")
		}
		return &m, m.validate()
	case <-t.C:
		return nil, errors.New("timed out waiting for connect message")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *session) dial(ctx context.Context, req *clientMsg) (*ssh.Client, error) {
	ip, err := resolveTarget(ctx, req.Host, s.cfg.AllowPrivate)
	if err != nil {
		return nil, err
	}
	auth, err := authMethods(req.Auth)
	if err != nil {
		return nil, err
	}

	addr := netip.AddrPortFrom(ip, uint16(req.Port)).String()
	d := net.Dialer{Timeout: s.cfg.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			err = opErr.Err
		}
		return nil, &gwError{codeUnreachable, fmt.Sprintf("Could not reach %s: %v", addr, err)}
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	cfg := &ssh.ClientConfig{
		User:            req.User,
		Auth:            auth,
		HostKeyCallback: s.verifyHostKey(ctx, req.KnownHostKey),
		BannerCallback: func(msg string) error {
			return s.ws.Write(ctx, websocket.MessageBinary, []byte(toCRLF(msg)))
		},
	}
	hostport := net.JoinHostPort(req.Host, strconv.Itoa(req.Port))
	c, chans, reqs, err := ssh.NewClientConn(conn, hostport, cfg)
	if err != nil {
		conn.Close()
		switch {
		case s.hostKeyErr != nil:
			return nil, s.hostKeyErr
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case strings.Contains(err.Error(), "unable to authenticate"):
			what := "password"
			if req.Auth.Type == "key" {
				what = "key"
			}
			return nil, &gwError{codeAuthFailed, "Authentication failed. Check the username and " + what + "."}
		}
		return nil, &gwError{codeHandshake, err.Error()}
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// verifyHostKey implements trust-on-first-use. The browser owns the list of
// trusted keys and sends the one it expects; an unknown key is shown to the
// user, a mismatched key is a hard failure.
func (s *session) verifyHostKey(ctx context.Context, known string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		s.fingerprint, s.hostKeyAlgo = fp, key.Type()

		if known != "" {
			if fp == known {
				return nil
			}
			_ = s.send(ctx, serverMsg{T: "hostkey", Status: "changed", Fingerprint: fp, Algo: key.Type()})
			s.hostKeyErr = &gwError{codeHostKeyChanged, "The server's host key does not match the one you trusted. " +
				"The server may have been reinstalled, or someone may be intercepting the connection."}
			return s.hostKeyErr
		}

		if err := s.send(ctx, serverMsg{T: "hostkey", Status: "new", Fingerprint: fp, Algo: key.Type()}); err != nil {
			return err
		}
		t := time.NewTimer(hostKeyTimeout)
		defer t.Stop()
		for {
			select {
			case f, ok := <-s.in:
				if !ok {
					return errors.New("client went away")
				}
				var m clientMsg
				if f.typ != websocket.MessageText || json.Unmarshal(f.data, &m) != nil {
					continue
				}
				switch m.T {
				case "hostkey-accept":
					return nil
				case "hostkey-reject":
					s.hostKeyErr = &gwError{codeHostKeyRejected, "Host key not trusted. Connection cancelled."}
					return s.hostKeyErr
				}
			case <-t.C:
				s.hostKeyErr = &gwError{codeHostKeyRejected, "Timed out waiting for host key confirmation."}
				return s.hostKeyErr
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func authMethods(a *authInfo) ([]ssh.AuthMethod, error) {
	switch a.Type {
	case "password":
		pw := a.Password
		return []ssh.AuthMethod{
			ssh.Password(pw),
			// Many servers only enable keyboard-interactive; answer hidden prompts with the password.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					if !echos[i] {
						answers[i] = pw
					}
				}
				return answers, nil
			}),
		}, nil
	case "key":
		var signer ssh.Signer
		var err error
		if a.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(a.Key), []byte(a.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(a.Key))
		}
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, &gwError{codeKeyPassphrase, "This private key is encrypted. Enter its passphrase."}
		}
		if err != nil {
			return nil, &gwError{codeKeyInvalid, "Could not read the private key (wrong passphrase or unsupported format)."}
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	return nil, &gwError{codeBadRequest, "unknown auth type"}
}

func (s *session) shell(ctx context.Context, client *ssh.Client, req *clientMsg) (websocket.StatusCode, string) {
	sess, err := client.NewSession()
	if err != nil {
		s.fail(ctx, codeSession, "Could not open a session: "+err.Error())
		return websocket.StatusNormalClosure, codeSession
	}
	defer sess.Close()

	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty(req.Term, req.Rows, req.Cols, modes); err != nil {
		s.fail(ctx, codeSession, "The server refused a terminal: "+err.Error())
		return websocket.StatusNormalClosure, codeSession
	}
	stdin, err1 := sess.StdinPipe()
	stdout, err2 := sess.StdoutPipe()
	stderr, err3 := sess.StderrPipe()
	if err := errors.Join(err1, err2, err3); err != nil {
		s.fail(ctx, codeSession, err.Error())
		return websocket.StatusInternalError, codeSession
	}
	if err := sess.Shell(); err != nil {
		s.fail(ctx, codeSession, "Could not start a shell: "+err.Error())
		return websocket.StatusNormalClosure, codeSession
	}
	if err := s.send(ctx, serverMsg{T: "ready", Fingerprint: s.fingerprint, Algo: s.hostKeyAlgo}); err != nil {
		return websocket.StatusNormalClosure, ""
	}

	var pumps sync.WaitGroup
	for _, r := range []io.Reader{stdout, stderr} {
		pumps.Add(1)
		go func() {
			defer pumps.Done()
			s.pumpOutput(ctx, r)
		}()
	}
	go s.keepalive(ctx, client)

	waitErr := make(chan error, 1)
	go func() { waitErr <- sess.Wait() }()

	for {
		select {
		case f, ok := <-s.in:
			if !ok {
				return websocket.StatusNormalClosure, ""
			}
			if f.typ == websocket.MessageBinary {
				if _, err := stdin.Write(f.data); err != nil {
					return websocket.StatusNormalClosure, "stdin closed"
				}
				s.bytesIn.Add(int64(len(f.data)))
				continue
			}
			var m clientMsg
			if json.Unmarshal(f.data, &m) == nil && m.T == "resize" && validSize(m.Cols, m.Rows) {
				_ = sess.WindowChange(m.Rows, m.Cols)
			}
		case err := <-waitErr:
			// Let the last output reach the browser before the exit notice.
			drained := make(chan struct{})
			go func() { pumps.Wait(); close(drained) }()
			select {
			case <-drained:
			case <-time.After(2 * time.Second):
			}
			code := exitCode(err)
			_ = s.send(ctx, serverMsg{T: "exit", ExitCode: &code})
			return websocket.StatusNormalClosure, "session ended"
		case <-ctx.Done():
			return websocket.StatusNormalClosure, ""
		}
	}
}

// pumpOutput copies PTY output to the socket. A slow browser blocks Write,
// which stops reads from the SSH channel and lets SSH flow control push back
// on the server instead of buffering unbounded output here.
func (s *session) pumpOutput(ctx context.Context, r io.Reader) {
	buf := make([]byte, outputChunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if werr := s.ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				return
			}
			s.bytesOut.Add(int64(n))
		}
		if err != nil {
			return
		}
	}
}

func (s *session) keepalive(ctx context.Context, client *ssh.Client) {
	t := time.NewTicker(keepaliveEvery)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				if misses++; misses >= 3 {
					client.Close()
					return
				}
			} else {
				misses = 0
			}
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = s.ws.Ping(pctx)
			cancel()
		}
	}
}

func exitCode(err error) int {
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		return exitErr.ExitStatus()
	default:
		return -1
	}
}

func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}
