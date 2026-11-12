package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// echoServer accepts TCP connections and writes back what it reads.
func echoServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func cliGet(t *testing.T, url, token string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	return r.StatusCode
}

func TestTCPForwardForCLI(t *testing.T) {
	port, fp, _ := testSSHServerWith(t, serverOpts{forward: true})
	echo := echoServer(t)
	url := startGateway(t, true)
	base := httpBase(url)
	_, ready := connectReady(t, url, port, fp, nil)

	body, _ := json.Marshal(map[string]any{"handle": ready.SFTP, "host": "127.0.0.1", "port": echo})
	resp, err := http.Post(base+"/api/tcp-forwards", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var f tcpForwardInfo
	_ = json.NewDecoder(resp.Body).Decode(&f)
	resp.Body.Close()
	if f.Token == "" || f.ID == "" {
		t.Fatalf("create: %d %+v", resp.StatusCode, f)
	}

	if s := cliGet(t, base+"/api/cli/forward", f.Token); s != 200 {
		t.Fatalf("info with token: %d", s)
	}
	if s := cliGet(t, base+"/api/cli/forward", "wrong"); s != http.StatusUnauthorized {
		t.Fatalf("info with a wrong token: %d", s)
	}

	// One local connection, as the CLI carries it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/ws/cli/forward"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + f.Token}}})
	if err != nil {
		t.Fatal(err)
	}
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	if _, err := conn.Write([]byte("ping over tcp")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 13)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping over tcp" {
		t.Fatalf("echo: %q %v", buf, err)
	}
	conn.Close()

	// Another account can't stop it; its owner can, and the token dies.
	req, _ := http.NewRequest("DELETE", base+"/api/tcp-forwards/"+f.ID, nil)
	req.Header.Set("X-Test-User", "u2")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusNotFound {
		t.Fatalf("delete by someone else: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("DELETE", base+"/api/tcp-forwards/"+f.ID, nil)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 200 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if s := cliGet(t, base+"/api/cli/forward", f.Token); s != http.StatusUnauthorized {
		t.Fatalf("after delete: %d", s)
	}
}

func createTCP(t *testing.T, base string, body map[string]any) tcpForwardInfo {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(base+"/api/tcp-forwards", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var f tcpForwardInfo
	_ = json.NewDecoder(resp.Body).Decode(&f)
	if f.Token == "" {
		t.Fatalf("create %v: %d", body, resp.StatusCode)
	}
	return f
}

func cliDial(t *testing.T, ctx context.Context, url, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
}

func TestSocksAndRemoteForwards(t *testing.T) {
	port, fp, _ := testSSHServerWith(t, serverOpts{forward: true})
	echo := echoServer(t)
	url := startGateway(t, true)
	base := httpBase(url)
	wsBase := "ws" + strings.TrimPrefix(base, "http")
	_, ready := connectReady(t, url, port, fp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// SOCKS: each connection names its destination.
	s := createTCP(t, base, map[string]any{"handle": ready.SFTP, "kind": "socks"})
	ws, _, err := cliDial(t, ctx, wsBase+"/ws/cli/forward?to=127.0.0.1:"+strconv.Itoa(echo), s.Token)
	if err != nil {
		t.Fatal(err)
	}
	c := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	c.Write([]byte("via socks"))
	buf := make([]byte, 9)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "via socks" {
		t.Fatalf("socks echo: %q %v", buf, err)
	}
	c.Close()
	// Nothing listening there: the CLI gets an HTTP error, not a dead socket.
	if _, res, err := cliDial(t, ctx, wsBase+"/ws/cli/forward?to=127.0.0.1:1", s.Token); err == nil || res == nil || res.StatusCode != http.StatusBadGateway {
		t.Fatalf("closed port: %v %v", res, err)
	}

	// Remote: the server listens; its connections reach the CLI.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	rport := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	r := createTCP(t, base, map[string]any{"handle": ready.SFTP, "kind": "remote", "host": "127.0.0.1", "port": rport})
	ctl, _, err := cliDial(t, ctx, wsBase+"/ws/cli/control", r.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, data, err := ctl.Read(ctx); err != nil || !strings.Contains(string(data), "listening") {
		t.Fatalf("control: %s %v", data, err)
	}
	if _, res, _ := cliDial(t, ctx, wsBase+"/ws/cli/control", r.Token); res == nil || res.StatusCode != http.StatusConflict {
		t.Fatal("a second CLI on the same remote forward should be refused")
	}
	incoming, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(rport))
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Close()
	incoming.Write([]byte("hello"))
	_, data, err := ctl.Read(ctx)
	var m struct{ T, ID string }
	if err != nil || json.Unmarshal(data, &m) != nil || m.T != "conn" {
		t.Fatalf("announce: %s %v", data, err)
	}
	dws, _, err := cliDial(t, ctx, wsBase+"/ws/cli/forward?accept="+m.ID, r.Token)
	if err != nil {
		t.Fatal(err)
	}
	dc := websocket.NetConn(ctx, dws, websocket.MessageBinary)
	got := make([]byte, 5)
	if _, err := io.ReadFull(dc, got); err != nil || string(got) != "hello" {
		t.Fatalf("remote → cli: %q %v", got, err)
	}
	dc.Write([]byte("back"))
	back := make([]byte, 4)
	if _, err := io.ReadFull(incoming, back); err != nil || string(back) != "back" {
		t.Fatalf("cli → remote: %q %v", back, err)
	}
}
