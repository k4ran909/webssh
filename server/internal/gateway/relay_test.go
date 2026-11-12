package gateway

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/k4ran909/webssh/internal/sshcore"
)

func relayURL(wsURL string) string { return strings.TrimSuffix(wsURL, "/ws") + "/relay" }

// openRelay asks the relay for a TCP connection and returns the first reply.
func openRelay(t *testing.T, url, host string, port int) (*client, serverMsg) {
	t.Helper()
	c := dial(t, url)
	c.sendJSON(map[string]any{"t": "dial", "host": host, "port": port})
	return c, c.nextMsg()
}

// An end-to-end session: the SSH client (sshcore, as in the browser) runs on
// this side and the gateway only carries bytes.
func TestRelayCarriesEndToEndSSH(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	url := relayURL(startGateway(t, true))
	c, m := openRelay(t, url, "127.0.0.1", port)
	if m.T != "open" {
		t.Fatalf("got %+v, want open", m)
	}
	conn := websocket.NetConn(c.ctx, c.c, websocket.MessageBinary)
	d := &sshcore.Dialer{
		First: func(context.Context, *sshcore.Hop) (net.Conn, error) { return conn, nil },
		Trust: func(*sshcore.Hop, string, string) error { t.Fatal("known key should not prompt"); return nil },
	}
	hops := []sshcore.Hop{{Host: "127.0.0.1", Port: port, User: "alice", Auth: &sshcore.Auth{Type: "password", Password: "s3cret"}, KnownHostKey: fp}}
	sc, err := d.Dial(c.ctx, hops)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if sc.Fingerprint != fp {
		t.Fatalf("fingerprint %q, want %q", sc.Fingerprint, fp)
	}
	sh, err := sshcore.OpenShell(sc.Client, nil, "xterm", 24, 80, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Stdin.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	buf := make([]byte, 256)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "echo:hi") && time.Now().Before(deadline) {
		n, err := sh.Stdout.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
	}
	if !strings.Contains(out.String(), "echo:hi") {
		t.Fatalf("output %q", out.String())
	}
}

func TestRelayAppliesEgressPolicy(t *testing.T) {
	port, _, _ := testSSHServer(t)
	_, m := openRelay(t, relayURL(startGateway(t, false)), "127.0.0.1", port)
	if m.T != "error" || m.Code != sshcore.CodeBlockedDest {
		t.Fatalf("got %+v, want %s", m, sshcore.CodeBlockedDest)
	}
}

func TestRelayRejectsBadFirstMessage(t *testing.T) {
	c := dial(t, relayURL(startGateway(t, true)))
	c.sendJSON(map[string]any{"t": "connect", "host": "127.0.0.1"})
	if m := c.nextMsg(); m.T != "error" || m.Code != codeBadRequest {
		t.Fatalf("got %+v", m)
	}
}
