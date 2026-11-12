package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
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
