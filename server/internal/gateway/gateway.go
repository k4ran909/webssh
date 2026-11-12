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
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh/agent"

	"github.com/k4ran909/webssh/internal/config"
	"github.com/k4ran909/webssh/internal/sshcore"
)

const (
	connectTimeout = 10 * time.Second
	keepaliveEvery = 30 * time.Second
	maxFrameBytes  = 1 << 20
	outputChunk    = 32 << 10
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

	tcpForwards map[string]*tcpForward
}

func New(cfg config.Config, log *slog.Logger, hooks Hooks) *Gateway {
	if hooks.ClientIP == nil {
		hooks.ClientIP = func(r *http.Request) string { return r.RemoteAddr }
	}
	if hooks.Audit == nil {
		hooks.Audit = func(string, string, string, any) {}
	}
	return &Gateway{cfg: cfg, log: log, hooks: hooks, slots: make(chan struct{}, cfg.MaxSessions),
		perUser: map[string]int{}, handles: map[string]*sftpHandle{}, lives: map[string]*live{}, forwards: map[int]*forward{},
		tcpForwards: map[string]*tcpForward{}}
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

	var keyring agent.Agent
	if req.Agent {
		keyring = sshcore.AgentKeyring(req.Hops()) // before credentials are dropped
	}
	conn, err := s.dial(req)
	req.DropCredentials() // as soon as the handshakes are over
	if err != nil {
		ge := sshcore.AsError(err, codeHandshake)
		log.Info("ssh connect failed", "code", ge.Code)
		dest["result"] = ge.Code
		g.hooks.Audit(s.userID, "ssh.connect", s.ip, dest)
		s.fail(ge.Code, ge.Msg)
		return nil
	}

	l, err := newLive(g, s, conn.Client, conn.Close, req, keyring, log)
	if err != nil {
		conn.Close()
		ge := sshcore.AsError(err, codeSession)
		s.fail(ge.Code, ge.Msg)
		return nil
	}
	ok = true
	dest["result"], dest["fingerprint"] = "ok", conn.Fingerprint
	if keyring != nil {
		dest["agent"] = true
	}
	g.hooks.Audit(s.userID, "ssh.connect", s.ip, dest)
	log.Info("ssh session opened", "fingerprint", conn.Fingerprint)

	l.attach(s, 0, false, &serverMsg{T: "ready", Fingerprint: conn.Fingerprint, Algo: conn.Algo, SFTP: l.id, Resume: l.resumeToken})
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
			return &m, m.Validate()
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

// dial connects to the target through req.Jump. The egress policy applies
// to the first hop only: later hops are dialed from the bastion's network.
func (s *session) dial(req *clientMsg) (*sshcore.Conn, error) {
	d := &sshcore.Dialer{
		Timeout: s.cfg.DialTimeout,
		First: func(ctx context.Context, h *hop) (net.Conn, error) {
			return s.gw.dialTarget(ctx, h.Host, h.Port)
		},
		Trust: s.askTrust,
		Changed: func(h *hop, fp, algo string) {
			_ = s.send(serverMsg{T: "hostkey", Status: "changed", Host: h.Host, Port: h.Port, Fingerprint: fp, Algo: algo})
		},
		Banner: func(msg string) { _ = s.ws.Write(s.ctx, websocket.MessageBinary, []byte(msg)) },
	}
	return d.Dial(s.ctx, req.Hops())
}

// askTrust shows an unknown host key to the user and waits for the answer.
func (s *session) askTrust(h *hop, fp, algo string) error {
	if err := s.send(serverMsg{T: "hostkey", Status: "new", Host: h.Host, Port: h.Port, Fingerprint: fp, Algo: algo}); err != nil {
		return err
	}
	t := time.NewTimer(sshcore.HostKeyTimeout)
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
				return &gwError{Code: codeHostKeyRejected, Msg: "Host key not trusted. Connection cancelled."}
			}
		case <-t.C:
			return &gwError{Code: codeHostKeyRejected, Msg: "Timed out waiting for host key confirmation."}
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
}
