// Package gateway bridges browser WebSockets to SSH sessions.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
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

	mu       sync.Mutex
	perUser  map[string]int
	handles  map[string]*sftpHandle
	lives    map[string]*live
	forwards map[int]*forward
}

func New(cfg config.Config, log *slog.Logger, hooks Hooks) *Gateway {
	if hooks.ClientIP == nil {
		hooks.ClientIP = func(r *http.Request) string { return r.RemoteAddr }
	}
	if hooks.Audit == nil {
		hooks.Audit = func(string, string, string, any) {}
	}
	return &Gateway{cfg: cfg, log: log, hooks: hooks, slots: make(chan struct{}, cfg.MaxSessions),
		perUser: map[string]int{}, handles: map[string]*sftpHandle{}, lives: map[string]*live{}, forwards: map[int]*forward{}}
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

// ServeHTTP handles one WebSocket. The first message either starts a new SSH
// session ("connect") or reattaches to one that lost its socket ("resume").
// SSH sessions outlive their sockets for cfg.ResumeWindow.
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
		ctx:    ctx,
		cfg:    g.cfg,
		log:    g.log.With("remote", ip, "account", userID),
		in:     make(chan frame, 64),
		kicked: make(chan struct{}),
		gw:     g,
		userID: userID,
		ip:     ip,
	}
	go s.readLoop(ctx, cancel)
	go s.pingLoop(ctx)

	first, err := s.awaitFirst(ctx)
	if err != nil {
		s.fail(codeBadRequest, err.Error())
		ws.Close(websocket.StatusPolicyViolation, "bad first message")
		return
	}
	var l *live
	if first.T == "resume" {
		l = g.resume(s, first)
	} else {
		l = g.start(s, first)
	}
	if l == nil {
		ws.Close(websocket.StatusNormalClosure, "")
		return
	}
	status, reason := l.serve(s)
	ws.Close(status, reason)
}

// start authenticates and opens a new shell; it returns nil after reporting any failure.
func (g *Gateway) start(s *session, req *clientMsg) *live {
	select {
	case g.slots <- struct{}{}:
	default:
		s.fail(codeBusy, "Too many open sessions on this gateway. Try again later.")
		return nil
	}
	if !g.claim(s.userID) {
		<-g.slots
		s.fail(codeBusy, fmt.Sprintf("You already have %d open sessions. Close one first.", g.cfg.MaxUserSessions))
		return nil
	}
	// From here the slot and claim belong to the session; release them on any failure.
	ok := false
	defer func() {
		if !ok {
			g.release(s.userID)
			<-g.slots
		}
	}()

	dest := map[string]any{"host": req.Host, "port": req.Port, "user": req.User}
	if len(req.Jump) > 0 {
		via := make([]string, len(req.Jump))
		for i, h := range req.Jump {
			via[i] = h.User + "@" + net.JoinHostPort(h.Host, strconv.Itoa(h.Port))
		}
		dest["via"] = via
	}
	log := s.log.With("user", req.User, "host", req.Host, "port", req.Port)

	keyring := agentKeyring(req) // before credentials are dropped
	client, closeAll, err := s.dial(req)
	// drop credentials as soon as the handshakes are over
	req.Auth = nil
	for i := range req.Jump {
		req.Jump[i].Auth = nil
	}
	if err != nil {
		var ge *gwError
		if !errors.As(err, &ge) {
			ge = &gwError{codeHandshake, err.Error()}
		}
		log.Info("ssh connect failed", "code", ge.code)
		dest["result"] = ge.code
		g.hooks.Audit(s.userID, "ssh.connect", s.ip, dest)
		s.fail(ge.code, ge.msg)
		return nil
	}

	l, err := newLive(g, s, client, closeAll, req, keyring, log)
	if err != nil {
		closeAll()
		var ge *gwError
		if !errors.As(err, &ge) {
			ge = &gwError{codeSession, err.Error()}
		}
		s.fail(ge.code, ge.msg)
		return nil
	}
	ok = true
	dest["result"], dest["fingerprint"] = "ok", s.fingerprint
	if keyring != nil {
		dest["agent"] = true
	}
	g.hooks.Audit(s.userID, "ssh.connect", s.ip, dest)
	log.Info("ssh session opened", "fingerprint", s.fingerprint)

	l.attach(s, 0, false, &serverMsg{T: "ready", Fingerprint: s.fingerprint, Algo: s.hostKeyAlgo, SFTP: l.id, Resume: l.resumeToken})
	return l
}

func (g *Gateway) resume(s *session, req *clientMsg) *live {
	g.mu.Lock()
	l := g.lives[req.Token]
	g.mu.Unlock()
	// Someone else's token looks exactly like an expired one.
	if l == nil || l.userID != s.userID || req.Token == "" {
		s.fail(codeResumeGone, "That session has ended.")
		return nil
	}
	if validSize(req.Cols, req.Rows) {
		_ = l.sess.WindowChange(req.Rows, req.Cols)
	}
	g.hooks.Audit(s.userID, "ssh.resume", s.ip, map[string]any{"host": l.host, "user": l.user})
	l.attach(s, req.Offset, true, nil)
	return l
}

type frame struct {
	typ  websocket.MessageType
	data []byte
}

// session is one WebSocket: it runs the handshake prompts for a new SSH
// connection and then carries a live session's terminal until it drops.
type session struct {
	ws     *websocket.Conn
	ctx    context.Context
	cfg    config.Config
	log    *slog.Logger
	in     chan frame
	gw     *Gateway
	userID string
	ip     string

	kicked   chan struct{} // closed when another socket takes over the session
	kickOnce sync.Once

	// Set by the host key callback during the handshake.
	fingerprint string
	hostKeyAlgo string
	hostKeyErr  *gwError
}

func (s *session) kick() { s.kickOnce.Do(func() { close(s.kicked) }) }

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

// pingLoop notices dead sockets (sleeping laptops, dropped Wi-Fi) so the
// session can be detached and kept for resuming.
func (s *session) pingLoop(ctx context.Context) {
	t := time.NewTicker(keepaliveEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := s.ws.Ping(pctx)
			cancel()
			if err != nil {
				s.ws.CloseNow()
				return
			}
		}
	}
}

func (s *session) send(m serverMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	return s.ws.Write(wctx, websocket.MessageText, b)
}

func (s *session) fail(code, msg string) {
	_ = s.send(serverMsg{T: "error", Code: code, Msg: msg})
}

func (s *session) awaitFirst(ctx context.Context) (*clientMsg, error) {
	t := time.NewTimer(connectTimeout)
	defer t.Stop()
	select {
	case f, ok := <-s.in:
		if !ok {
			return nil, errors.New("client went away")
		}
		var m clientMsg
		if f.typ != websocket.MessageText || json.Unmarshal(f.data, &m) != nil {
			return nil, errors.New("expected a connect message")
		}
		switch m.T {
		case "connect":
			return &m, m.validate()
		case "resume":
			return &m, nil
		}
		return nil, errors.New("expected a connect message")
	case <-t.C:
		return nil, errors.New("timed out waiting for connect message")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// dial connects to the target, hopping through req.Jump in order. Only the
// first hop is subject to the egress policy: later hops are dialed from the
// bastion's own network, which is the point of a jump host.
func (s *session) dial(req *clientMsg) (*ssh.Client, func(), error) {
	ctx := s.ctx
	hops := append(append([]hop{}, req.Jump...), *req.target())
	var chain []*ssh.Client
	closeAll := func() {
		for i := len(chain) - 1; i >= 0; i-- {
			chain[i].Close()
		}
	}

	for i := range hops {
		h := &hops[i]
		final := i == len(hops)-1
		var conn net.Conn
		if i == 0 {
			ip, err := resolveTarget(ctx, h.Host, s.cfg.AllowPrivate)
			if err != nil {
				return nil, nil, err
			}
			addr := netip.AddrPortFrom(ip, uint16(h.Port)).String()
			d := net.Dialer{Timeout: s.cfg.DialTimeout}
			if conn, err = d.DialContext(ctx, "tcp", addr); err != nil {
				var opErr *net.OpError
				if errors.As(err, &opErr) {
					err = opErr.Err
				}
				return nil, nil, &gwError{codeUnreachable, fmt.Sprintf("Could not reach %s: %v", addr, err)}
			}
		} else {
			dctx, cancel := context.WithTimeout(ctx, s.cfg.DialTimeout)
			c, err := chain[len(chain)-1].DialContext(dctx, "tcp", net.JoinHostPort(h.Host, strconv.Itoa(h.Port)))
			cancel()
			if err != nil {
				closeAll()
				return nil, nil, &gwError{codeUnreachable, fmt.Sprintf("Jump host %s could not reach %s:%d: %v", hops[i-1].Host, h.Host, h.Port, err)}
			}
			conn = c
		}
		client, err := s.handshake(conn, h, final)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		chain = append(chain, client)
	}
	return chain[len(chain)-1], closeAll, nil
}

func (s *session) handshake(conn net.Conn, h *hop, final bool) (*ssh.Client, error) {
	ctx := s.ctx
	auth, err := authMethods(h.Auth)
	if err != nil {
		conn.Close()
		return nil, err
	}
	// Channels through a bastion don't support deadlines, so a timer bounds
	// the handshake for both kinds of connection (it includes the user
	// reading the host key prompt).
	timer := time.AfterFunc(handshakeTimeout, func() { conn.Close() })
	defer timer.Stop()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	where := ""
	if !final {
		where = " on jump host " + h.Host
	}
	s.hostKeyErr = nil
	cfg := &ssh.ClientConfig{
		User:            h.User,
		Auth:            auth,
		HostKeyCallback: s.verifyHostKey(h, final),
		BannerCallback: func(msg string) error {
			return s.ws.Write(ctx, websocket.MessageBinary, []byte(toCRLF(msg)))
		},
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(h.Host, strconv.Itoa(h.Port)), cfg)
	if err != nil {
		conn.Close()
		switch {
		case s.hostKeyErr != nil:
			return nil, s.hostKeyErr
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case strings.Contains(err.Error(), "unable to authenticate"):
			what := "password"
			if h.Auth.Type == "key" {
				what = "key"
			}
			return nil, &gwError{codeAuthFailed, "Authentication failed" + where + ". Check the username and " + what + "."}
		}
		return nil, &gwError{codeHandshake, err.Error() + where}
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// verifyHostKey implements trust-on-first-use per hop. The browser owns the
// list of trusted keys and sends the one it expects; an unknown key is shown
// to the user, a mismatched key is a hard failure.
func (s *session) verifyHostKey(h *hop, final bool) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		if final {
			s.fingerprint, s.hostKeyAlgo = fp, key.Type()
		}
		msg := serverMsg{T: "hostkey", Host: h.Host, Port: h.Port, Fingerprint: fp, Algo: key.Type()}

		if h.KnownHostKey != "" {
			if fp == h.KnownHostKey {
				return nil
			}
			msg.Status = "changed"
			_ = s.send(msg)
			s.hostKeyErr = &gwError{codeHostKeyChanged, "The host key of " + h.Host + " does not match the one you trusted. " +
				"The server may have been reinstalled, or someone may be intercepting the connection."}
			return s.hostKeyErr
		}

		msg.Status = "new"
		if err := s.send(msg); err != nil {
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
			case <-s.ctx.Done():
				return s.ctx.Err()
			}
		}
	}
}

func parseSigner(a *authInfo) (ssh.Signer, error) {
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
	return signer, nil
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
		signer, err := parseSigner(a)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	return nil, &gwError{codeBadRequest, "unknown auth type"}
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
