package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/k4ran909/webssh/internal/config"
)

// testSSHServer is a tiny in-process SSH server: password alice/s3cret or the
// returned client key, echoes input as "echo:<data>", reports window changes,
// and exits with status 3 when it receives "exit".
func testSSHServer(t *testing.T) (port int, hostFP string, clientKeyPEM string) {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)

	_, userPriv, _ := ed25519.GenerateKey(rand.Reader)
	userSigner, _ := ssh.NewSignerFromKey(userPriv)
	block, err := ssh.MarshalPrivateKey(userPriv, "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "alice" && string(pw) == "s3cret" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == "alice" && bytes.Equal(k.Marshal(), userSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveTestConn(conn, cfg)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, ssh.FingerprintSHA256(hostSigner.PublicKey()), string(pem.EncodeToMemory(block))
}

func serveTestConn(conn net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range creqs {
				switch req.Type {
				case "pty-req", "shell":
					req.Reply(true, nil)
					if req.Type == "shell" {
						go echoShell(ch)
					}
				case "window-change":
					var wc struct{ Cols, Rows, W, H uint32 }
					if ssh.Unmarshal(req.Payload, &wc) == nil {
						fmt.Fprintf(ch, "resized %dx%d\r\n", wc.Cols, wc.Rows)
					}
				default:
					req.Reply(false, nil)
				}
			}
		}()
	}
}

func echoShell(ch ssh.Channel) {
	buf := make([]byte, 1024)
	for {
		n, err := ch.Read(buf)
		if err != nil {
			return
		}
		if bytes.Contains(buf[:n], []byte("exit")) {
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{3}))
			ch.Close()
			return
		}
		ch.Write(append([]byte("echo:"), buf[:n]...))
	}
}

func startGateway(t *testing.T, allowPrivate bool) string {
	t.Helper()
	g := New(config.Config{AllowPrivate: allowPrivate, MaxSessions: 4, MaxUserSessions: 4, DialTimeout: 2 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hooks{User: func(r *http.Request) (string, bool) { return "u1", r.Header.Get("X-Test-Anon") == "" }})
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

type client struct {
	t   *testing.T
	ctx context.Context
	c   *websocket.Conn
	out bytes.Buffer
}

func dial(t *testing.T, url string) *client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return &client{t: t, ctx: ctx, c: c}
}

func (c *client) sendJSON(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	if err := c.c.Write(c.ctx, websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) sendBytes(s string) {
	c.t.Helper()
	if err := c.c.Write(c.ctx, websocket.MessageBinary, []byte(s)); err != nil {
		c.t.Fatal(err)
	}
}

// nextMsg returns the next control message, collecting terminal output on the way.
func (c *client) nextMsg() serverMsg {
	c.t.Helper()
	for {
		typ, data, err := c.c.Read(c.ctx)
		if err != nil {
			c.t.Fatalf("read: %v (output so far %q)", err, c.out.String())
		}
		if typ == websocket.MessageBinary {
			c.out.Write(data)
			continue
		}
		var m serverMsg
		if err := json.Unmarshal(data, &m); err != nil {
			c.t.Fatal(err)
		}
		return m
	}
}

// waitOutput reads until the collected terminal output contains want.
func (c *client) waitOutput(want string) {
	c.t.Helper()
	for !strings.Contains(c.out.String(), want) {
		typ, data, err := c.c.Read(c.ctx)
		if err != nil {
			c.t.Fatalf("waiting for %q: %v (got %q)", want, err, c.out.String())
		}
		if typ == websocket.MessageBinary {
			c.out.Write(data)
		}
	}
}

func connectMsg(port int, auth map[string]string, known string) map[string]any {
	return map[string]any{
		"t": "connect", "host": "127.0.0.1", "port": port, "user": "alice",
		"auth": auth, "knownHostKey": known, "cols": 120, "rows": 32,
	}
}

var goodPassword = map[string]string{"type": "password", "password": "s3cret"}

func TestPasswordSessionWithHostKeyPrompt(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	c := dial(t, startGateway(t, true))

	c.sendJSON(connectMsg(port, goodPassword, ""))
	m := c.nextMsg()
	if m.T != "hostkey" || m.Status != "new" || m.Fingerprint != fp || m.Algo != "ssh-ed25519" {
		t.Fatalf("want new hostkey prompt for %s, got %+v", fp, m)
	}
	c.sendJSON(map[string]string{"t": "hostkey-accept"})
	if m := c.nextMsg(); m.T != "ready" || m.Fingerprint != fp {
		t.Fatalf("want ready, got %+v", m)
	}

	c.sendBytes("hello")
	c.waitOutput("echo:hello")

	c.sendJSON(map[string]any{"t": "resize", "cols": 100, "rows": 40})
	c.waitOutput("resized 100x40")

	c.sendBytes("exit")
	m = c.nextMsg()
	if m.T != "exit" || m.ExitCode == nil || *m.ExitCode != 3 {
		t.Fatalf("want exit 3, got %+v", m)
	}
}

func TestKnownHostKeySkipsPrompt(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	c := dial(t, startGateway(t, true))
	c.sendJSON(connectMsg(port, goodPassword, fp))
	if m := c.nextMsg(); m.T != "ready" {
		t.Fatalf("want ready, got %+v", m)
	}
}

func TestPrivateKeyAuth(t *testing.T) {
	port, fp, key := testSSHServer(t)
	c := dial(t, startGateway(t, true))
	c.sendJSON(connectMsg(port, map[string]string{"type": "key", "key": key}, fp))
	if m := c.nextMsg(); m.T != "ready" {
		t.Fatalf("want ready, got %+v", m)
	}
}

func TestChangedHostKeyIsBlocked(t *testing.T) {
	port, _, _ := testSSHServer(t)
	c := dial(t, startGateway(t, true))
	c.sendJSON(connectMsg(port, goodPassword, "SHA256:somethingElse"))
	if m := c.nextMsg(); m.T != "hostkey" || m.Status != "changed" {
		t.Fatalf("want changed hostkey, got %+v", m)
	}
	if m := c.nextMsg(); m.T != "error" || m.Code != codeHostKeyChanged {
		t.Fatalf("want %s, got %+v", codeHostKeyChanged, m)
	}
}

func TestRejectedHostKey(t *testing.T) {
	port, _, _ := testSSHServer(t)
	c := dial(t, startGateway(t, true))
	c.sendJSON(connectMsg(port, goodPassword, ""))
	c.nextMsg()
	c.sendJSON(map[string]string{"t": "hostkey-reject"})
	if m := c.nextMsg(); m.T != "error" || m.Code != codeHostKeyRejected {
		t.Fatalf("want %s, got %+v", codeHostKeyRejected, m)
	}
}

func TestWrongPassword(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	c := dial(t, startGateway(t, true))
	c.sendJSON(connectMsg(port, map[string]string{"type": "password", "password": "nope"}, fp))
	if m := c.nextMsg(); m.T != "error" || m.Code != codeAuthFailed {
		t.Fatalf("want %s, got %+v", codeAuthFailed, m)
	}
}

func TestPrivateTargetsBlockedByDefaultPolicy(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	c := dial(t, startGateway(t, false))
	c.sendJSON(connectMsg(port, goodPassword, fp))
	if m := c.nextMsg(); m.T != "error" || m.Code != codeBlockedDest {
		t.Fatalf("want %s, got %+v", codeBlockedDest, m)
	}
}

func TestBadConnectMessage(t *testing.T) {
	c := dial(t, startGateway(t, true))
	c.sendJSON(map[string]any{"t": "connect", "host": "example.com", "auth": goodPassword})
	if m := c.nextMsg(); m.T != "error" || m.Code != codeBadRequest {
		t.Fatalf("want %s, got %+v", codeBadRequest, m)
	}
}

func TestCrossOriginRejected(t *testing.T) {
	url := startGateway(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		t.Fatal("cross-origin websocket was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %v", resp)
	}
}

func TestAnonymousRejected(t *testing.T) {
	url := startGateway(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"X-Test-Anon": []string{"1"}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for anonymous, got %v %v", resp, err)
	}
}

func TestIsRestricted(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true,
		"169.254.169.254": true, "100.64.0.1": true, "0.0.0.0": true, "::1": true,
		"fe80::1": true, "fd00::1": true, "::ffff:10.0.0.1": true, "64:ff9b::a00:1": true,
		"8.8.8.8": false, "142.93.18.204": false, "2606:4700::1111": false,
	} {
		if got := isRestricted(netip.MustParseAddr(addr)); got != want {
			t.Errorf("isRestricted(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := clientMsg{T: "connect", Host: "[::1]", User: "root", Auth: &authInfo{Type: "password", Password: "x"}}
	if err := ok.validate(); err != nil || ok.Host != "::1" || ok.Port != 22 || ok.Term != "xterm-256color" || ok.Cols != 80 {
		t.Fatalf("defaults not applied: %v %+v", err, ok)
	}
	for name, m := range map[string]clientMsg{
		"no host":   {User: "u", Auth: ok.Auth},
		"bad host":  {Host: "a b", User: "u", Auth: ok.Auth},
		"bad port":  {Host: "h", Port: 70000, User: "u", Auth: ok.Auth},
		"no user":   {Host: "h", Auth: ok.Auth},
		"no auth":   {Host: "h", User: "u"},
		"bad term":  {Host: "h", User: "u", Auth: ok.Auth, Term: "xterm; rm -rf"},
		"empty key": {Host: "h", User: "u", Auth: &authInfo{Type: "key"}},
	} {
		if err := m.validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
