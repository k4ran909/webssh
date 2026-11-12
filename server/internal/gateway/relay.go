package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/k4ran909/webssh/internal/sshcore"
)

// End-to-end sessions: the browser runs the SSH client itself (in
// WebAssembly) and the gateway only relays the TCP stream to the first hop.
// That stream is SSH, encrypted end to end, so the gateway sees the
// destination and byte counts but never credentials or terminal contents.
// The egress policy applies exactly as for gateway sessions.
//
//	client → server  text    {"t":"dial", host, port}   (first message)
//	server → client  text    {"t":"open"} | {"t":"error", code, msg}
//	then             binary  raw TCP bytes, both ways
type relayMsg struct {
	T    string `json:"t"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

func relayTarget(m *relayMsg) error {
	m.Host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(m.Host), "["), "]")
	if m.T != "dial" || m.Host == "" || len(m.Host) > 253 || strings.ContainsAny(m.Host, " \t\r\n/\\@") {
		return errors.New("expected a dial message")
	}
	if m.Port == 0 {
		m.Port = 22
	}
	if m.Port < 1 || m.Port > 65535 {
		return errors.New("invalid port")
	}
	return nil
}

func (g *Gateway) ServeRelay(w http.ResponseWriter, r *http.Request) {
	userID, ok := g.hooks.User(r)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
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
	fail := func(code, msg string) {
		b, _ := json.Marshal(serverMsg{T: "error", Code: code, Msg: msg})
		_ = ws.Write(ctx, websocket.MessageText, b)
		ws.Close(websocket.StatusNormalClosure, "")
	}

	fctx, fcancel := context.WithTimeout(ctx, connectTimeout)
	typ, data, err := ws.Read(fctx)
	fcancel()
	var m relayMsg
	if err != nil || typ != websocket.MessageText || json.Unmarshal(data, &m) != nil || relayTarget(&m) != nil {
		fail(codeBadRequest, "expected a dial message")
		return
	}

	select {
	case g.slots <- struct{}{}:
	default:
		fail(codeBusy, "Too many open sessions on this gateway. Try again later.")
		return
	}
	defer func() { <-g.slots }()
	if !g.claim(userID) {
		fail(codeBusy, fmt.Sprintf("You already have %d open sessions. Close one first.", g.cfg.MaxUserSessions))
		return
	}
	defer g.release(userID)

	dest := map[string]any{"host": m.Host, "port": m.Port, "e2e": true}
	conn, err := g.dialTarget(ctx, m.Host, m.Port)
	if err != nil {
		ge := sshcore.AsError(err, codeUnreachable)
		dest["result"] = ge.Code
		g.hooks.Audit(userID, "ssh.connect", ip, dest)
		fail(ge.Code, ge.Msg)
		return
	}
	defer conn.Close()
	b, _ := json.Marshal(serverMsg{T: "open"})
	if ws.Write(ctx, websocket.MessageText, b) != nil {
		return
	}
	dest["result"] = "relayed"
	g.hooks.Audit(userID, "ssh.connect", ip, dest)
	started := time.Now()

	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	var in, out atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // browser → server
		defer wg.Done()
		n, _ := io.Copy(conn, nc)
		in.Add(n)
		cancel()
	}()
	go func() { // server → browser
		defer wg.Done()
		n, _ := io.Copy(nc, conn)
		out.Add(n)
		cancel()
	}()
	<-ctx.Done()
	conn.Close()
	nc.Close()
	wg.Wait()
	g.hooks.Audit(userID, "ssh.session", ip, map[string]any{
		"host": m.Host, "port": m.Port, "e2e": true,
		"seconds": int(time.Since(started).Seconds()), "bytesIn": in.Load(), "bytesOut": out.Load(),
	})
}

// dialTarget opens a TCP connection to host:port under the egress policy:
// the name is resolved once and that exact IP is dialed.
func (g *Gateway) dialTarget(ctx context.Context, host string, port int) (net.Conn, error) {
	ip, err := resolveTarget(ctx, host, g.cfg.AllowPrivate)
	if err != nil {
		return nil, err
	}
	addr := netip.AddrPortFrom(ip, uint16(port)).String()
	nd := net.Dialer{Timeout: g.cfg.DialTimeout}
	conn, err := nd.DialContext(ctx, "tcp", addr)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			err = opErr.Err
		}
		return nil, &gwError{Code: codeUnreachable, Msg: fmt.Sprintf("Could not reach %s: %v", addr, err)}
	}
	return conn, nil
}
