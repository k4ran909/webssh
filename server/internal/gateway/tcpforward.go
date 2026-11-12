package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// Raw TCP forwards for the companion CLI (cmd/webssh-cli). A browser can't
// listen on the user's machine, so the CLI does. Three kinds, like ssh's
// -L, -D and -R:
//
//   - local:  the CLI accepts local connections and carries each one over a
//     WebSocket; the gateway opens a direct-tcpip channel to the fixed target.
//   - socks:  the same, but the CLI is a SOCKS5 proxy and each connection
//     names its own destination (?to=host:port).
//   - remote: the gateway asks the SSH server to listen (tcpip-forward) and
//     announces each incoming connection on the CLI's control socket; the CLI
//     fetches it (?accept=id) and connects it to a port on the user's machine.
//
// Access is a bearer token, shown once to the creator and stored hashed. It
// dies with the forward or with the session.

const (
	maxTCPForwardsPerUser = 10
	maxTCPConnsPerForward = 32
)

type tcpForward struct {
	id, userID, handle, host, target string
	kind                             string // local | socks | remote
	tokenHash                        [32]byte
	client                           *ssh.Client
	conns                            atomic.Int64

	// remote only
	mu      sync.Mutex
	control bool                // a CLI is attached
	pending map[string]net.Conn // incoming connections waiting for the CLI
}

type tcpForwardInfo struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Conns  int64  `json:"conns"`
	// Only returned to the creator, once.
	Token string `json:"token,omitempty"`
}

func (g *Gateway) tcpRoutes(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/tcp-forwards", wrap(http.HandlerFunc(g.listTCPForwards)))
	mux.Handle("POST /api/tcp-forwards", wrap(http.HandlerFunc(g.createTCPForward)))
	mux.Handle("DELETE /api/tcp-forwards/{id}", wrap(http.HandlerFunc(g.deleteTCPForward)))
}

// CLIRoutes mounts what the companion CLI calls. They authenticate with the
// forward's bearer token, not a browser session, so don't wrap them.
func (g *Gateway) CLIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cli/forward", g.cliForwardInfo)
	mux.HandleFunc("GET /ws/cli/forward", g.cliForwardConn)
	mux.HandleFunc("GET /ws/cli/control", g.cliForwardControl)
}

func (g *Gateway) listTCPForwards(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	g.mu.Lock()
	out := []tcpForwardInfo{}
	for _, f := range g.tcpForwards {
		if f.userID == userID {
			out = append(out, tcpForwardInfo{ID: f.id, Handle: f.handle, Kind: f.kind, Target: f.target, Conns: f.conns.Load()})
		}
	}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"forwards": out})
}

func (g *Gateway) createTCPForward(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	var req struct {
		Handle string `json:"handle"`
		Kind   string `json:"kind"`
		Host   string `json:"host"`
		Port   int    `json:"port"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
		sftpErr(w, http.StatusBadRequest, "BAD_REQUEST", "Malformed request.")
		return
	}
	switch req.Kind {
	case "":
		req.Kind = "local"
	case "local", "remote", "socks":
	default:
		sftpErr(w, http.StatusBadRequest, "BAD_REQUEST", "Unknown forward kind.")
		return
	}
	if req.Kind == "socks" {
		req.Port = 1 // not used; each connection names its destination
	}
	if req.Port < 1 || req.Port > 65535 {
		sftpErr(w, http.StatusBadRequest, "BAD_REQUEST", "Give a port between 1 and 65535.")
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if len(host) > 253 || strings.ContainsAny(host, " /\\@?#") {
		sftpErr(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid host.")
		return
	}
	token := randomToken()
	f := &tcpForward{id: randomToken()[:12], userID: userID, handle: req.Handle, kind: req.Kind,
		target: net.JoinHostPort(host, strconv.Itoa(req.Port)), tokenHash: sha256.Sum256([]byte(token)), pending: map[string]net.Conn{}}
	if req.Kind == "socks" {
		f.target = "SOCKS proxy"
	}
	g.mu.Lock()
	h := g.handles[req.Handle]
	if h == nil || h.userID != userID {
		g.mu.Unlock()
		sftpErr(w, http.StatusNotFound, "NO_SESSION", "That SSH session is closed.")
		return
	}
	n := 0
	for _, o := range g.tcpForwards {
		if o.userID == userID {
			n++
		}
	}
	if n >= maxTCPForwardsPerUser {
		g.mu.Unlock()
		sftpErr(w, http.StatusConflict, "TOO_MANY", "You have too many TCP forwards. Stop one first.")
		return
	}
	f.client, f.host = h.ssh, h.host
	g.tcpForwards[f.id] = f
	g.mu.Unlock()

	g.hooks.Audit(userID, "forward.tcp.open", g.hooks.ClientIP(r), map[string]any{"host": f.host, "target": f.target, "kind": f.kind})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tcpForwardInfo{ID: f.id, Handle: f.handle, Kind: f.kind, Target: f.target, Token: token})
}

func (g *Gateway) deleteTCPForward(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	g.mu.Lock()
	f := g.tcpForwards[r.PathValue("id")]
	if f != nil && f.userID == userID {
		delete(g.tcpForwards, f.id)
	} else {
		f = nil
	}
	g.mu.Unlock()
	if f == nil {
		sftpErr(w, http.StatusNotFound, "NOT_FOUND", "No such forward.")
		return
	}
	f.closePending()
	g.hooks.Audit(userID, "forward.tcp.close", g.hooks.ClientIP(r), map[string]any{"target": f.target, "kind": f.kind})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// tcpForwardFor finds the forward a bearer token belongs to.
func (g *Gateway) tcpForwardFor(r *http.Request) *tcpForward {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(token))
	g.mu.Lock()
	defer g.mu.Unlock()
	var found *tcpForward
	for _, f := range g.tcpForwards {
		if subtle.ConstantTimeCompare(sum[:], f.tokenHash[:]) == 1 {
			found = f
		}
	}
	return found
}

func (g *Gateway) cliForwardInfo(w http.ResponseWriter, r *http.Request) {
	f := g.tcpForwardFor(r)
	if f == nil {
		sftpErr(w, http.StatusUnauthorized, "FORWARD_GONE", "That forward has ended (or the token is wrong). Create a new one in webssh.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"target": f.target, "host": f.host, "kind": f.kind})
}

// cliForwardConn carries one connection between the CLI and the server. The
// remote end is reached before the WebSocket is accepted, so a failure is an
// HTTP error the CLI can report (and turn into a SOCKS failure reply).
func (g *Gateway) cliForwardConn(w http.ResponseWriter, r *http.Request) {
	f := g.tcpForwardFor(r)
	if f == nil {
		http.Error(w, "forward gone", http.StatusUnauthorized)
		return
	}
	if f.conns.Add(1) > maxTCPConnsPerForward {
		f.conns.Add(-1)
		http.Error(w, "too many connections on this forward", http.StatusTooManyRequests)
		return
	}
	defer f.conns.Add(-1)
	ctx := r.Context()
	var remote net.Conn
	var err error
	switch f.kind {
	case "local":
		remote, err = f.client.DialContext(ctx, "tcp", f.target)
	case "socks":
		to := r.URL.Query().Get("to")
		if _, port, perr := net.SplitHostPort(to); perr != nil || port == "" || len(to) > 300 || strings.ContainsAny(to, " /\\@?#") {
			http.Error(w, "bad destination", http.StatusBadRequest)
			return
		}
		remote, err = f.client.DialContext(ctx, "tcp", to)
	case "remote":
		f.mu.Lock()
		remote = f.pending[r.URL.Query().Get("accept")]
		delete(f.pending, r.URL.Query().Get("accept"))
		f.mu.Unlock()
		if remote == nil {
			http.Error(w, "no such incoming connection", http.StatusNotFound)
			return
		}
	}
	if err != nil {
		http.Error(w, "nothing answered on the server: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer remote.Close()
	// The CLI sends no Origin; a page on another origin would need the token.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: g.cfg.AllowedOrigins})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	local := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	var once sync.Once
	done := make(chan struct{})
	stop := func() { once.Do(func() { close(done) }) }
	go func() { _, _ = io.Copy(remote, local); stop() }()
	go func() { _, _ = io.Copy(local, remote); stop() }()
	<-done
}

func (f *tcpForward) closePending() {
	f.mu.Lock()
	for id, c := range f.pending {
		c.Close()
		delete(f.pending, id)
	}
	f.mu.Unlock()
}

// cliForwardControl is the remote kind's control socket: while it is open
// the SSH server listens on the forward's address, and each incoming
// connection is announced as {"t":"conn","id"} for the CLI to fetch.
func (g *Gateway) cliForwardControl(w http.ResponseWriter, r *http.Request) {
	f := g.tcpForwardFor(r)
	if f == nil || f.kind != "remote" {
		http.Error(w, "forward gone", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	busy := f.control
	f.control = true
	f.mu.Unlock()
	if busy {
		http.Error(w, "another webssh-cli is already serving this forward", http.StatusConflict)
		return
	}
	defer func() {
		f.mu.Lock()
		f.control = false
		f.mu.Unlock()
		f.closePending()
	}()
	ln, err := f.client.Listen("tcp", f.target)
	if err != nil {
		http.Error(w, "the server refused to listen on "+f.target+": "+err.Error(), http.StatusBadGateway)
		return
	}
	defer ln.Close()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: g.cfg.AllowedOrigins})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() { // the CLI never sends; reading notices when it goes away
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				cancel()
				ln.Close()
				return
			}
		}
	}()
	b, _ := json.Marshal(map[string]string{"t": "listening", "addr": f.target})
	if ws.Write(ctx, websocket.MessageText, b) != nil {
		return
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		id := randomToken()[:16]
		f.mu.Lock()
		if len(f.pending) >= maxTCPConnsPerForward {
			f.mu.Unlock()
			c.Close()
			continue
		}
		f.pending[id] = c
		f.mu.Unlock()
		// Not fetched in time: drop it.
		time.AfterFunc(15*time.Second, func() {
			f.mu.Lock()
			if p := f.pending[id]; p != nil {
				p.Close()
				delete(f.pending, id)
			}
			f.mu.Unlock()
		})
		b, _ := json.Marshal(map[string]string{"t": "conn", "id": id})
		if ws.Write(ctx, websocket.MessageText, b) != nil {
			return
		}
	}
}
