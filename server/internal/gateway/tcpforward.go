package gateway

import (
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

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// Raw TCP forwards for the companion CLI (cmd/webssh-cli). A browser can't
// listen on the user's machine, so the CLI does: it accepts local
// connections and carries each one over a WebSocket to the gateway, which
// opens a direct-tcpip channel on the session's SSH connection. That reaches
// databases and anything else that isn't HTTP.
//
// Access is a bearer token, shown once to the creator and stored hashed. It
// dies with the forward or with the session.

const (
	maxTCPForwardsPerUser = 10
	maxTCPConnsPerForward = 32
)

type tcpForward struct {
	id, userID, handle, host, target string
	tokenHash                        [32]byte
	client                           *ssh.Client
	conns                            atomic.Int64
}

type tcpForwardInfo struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
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
}

func (g *Gateway) listTCPForwards(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	g.mu.Lock()
	out := []tcpForwardInfo{}
	for _, f := range g.tcpForwards {
		if f.userID == userID {
			out = append(out, tcpForwardInfo{ID: f.id, Handle: f.handle, Target: f.target, Conns: f.conns.Load()})
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
		Host   string `json:"host"`
		Port   int    `json:"port"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil || req.Port < 1 || req.Port > 65535 {
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
	f := &tcpForward{id: randomToken()[:12], userID: userID, handle: req.Handle, target: net.JoinHostPort(host, strconv.Itoa(req.Port)),
		tokenHash: sha256.Sum256([]byte(token))}
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

	g.hooks.Audit(userID, "forward.tcp.open", g.hooks.ClientIP(r), map[string]any{"host": f.host, "target": f.target})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tcpForwardInfo{ID: f.id, Handle: f.handle, Target: f.target, Token: token})
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
	g.hooks.Audit(userID, "forward.tcp.close", g.hooks.ClientIP(r), map[string]any{"target": f.target})
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
	_ = json.NewEncoder(w).Encode(map[string]string{"target": f.target, "host": f.host})
}

// cliForwardConn carries one local connection from the CLI to the target.
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
	// The CLI sends no Origin; a page on another origin would need the token.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: g.cfg.AllowedOrigins})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()
	remote, err := f.client.DialContext(ctx, "tcp", f.target)
	if err != nil {
		ws.Close(websocket.StatusTryAgainLater, "nothing answered at "+f.target+" on the server")
		return
	}
	defer remote.Close()
	local := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	var once sync.Once
	done := make(chan struct{})
	stop := func() { once.Do(func() { close(done) }) }
	go func() { _, _ = io.Copy(remote, local); stop() }()
	go func() { _, _ = io.Copy(local, remote); stop() }()
	<-done
}
