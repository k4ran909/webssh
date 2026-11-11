package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/k4ran909/webssh/internal/config"
)

// readyWithOutput connects and returns the client plus the ready message.
func connectReady(t *testing.T, url string, port int, fp string, extra map[string]any) (*client, serverMsg) {
	t.Helper()
	c := dial(t, url)
	msg := connectMsg(port, goodPassword, fp)
	for k, v := range extra {
		msg[k] = v
	}
	c.sendJSON(msg)
	m := c.nextMsg()
	if m.T != "ready" {
		t.Fatalf("want ready, got %+v", m)
	}
	c.out.Reset() // offsets count output after "ready"
	return c, m
}

func TestResumeReplaysMissedOutput(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	url := startGatewayWith(t, config.Config{AllowPrivate: true, ResumeWindow: time.Minute})
	c, ready := connectReady(t, url, port, fp, nil)
	if ready.Resume == "" {
		t.Fatal("ready should carry a resume token")
	}

	c.sendBytes("first")
	c.waitOutput("echo:first")
	received := int64(c.out.Len())
	// Output produced after the client stopped reading, then the socket drops.
	c.sendBytes("second")
	time.Sleep(200 * time.Millisecond)
	c.c.CloseNow()

	r := dial(t, url)
	r.sendJSON(map[string]any{"t": "resume", "token": ready.Resume, "offset": received, "cols": 100, "rows": 30})
	m := r.nextMsg()
	if m.T != "resumed" || m.Offset != received || m.Gap {
		t.Fatalf("want resumed at %d, got %+v", received, m)
	}
	r.waitOutput("echo:second") // replayed, not lost
	if strings.Contains(r.out.String(), "echo:first") {
		t.Fatalf("replay should start at the offset, got %q", r.out.String())
	}
	r.sendBytes("third")
	r.waitOutput("echo:third")
	r.sendBytes("exit")
	if m := r.nextMsg(); m.T != "exit" || *m.ExitCode != 3 {
		t.Fatalf("want exit 3, got %+v", m)
	}
}

func TestResumeRejectsOtherUsersAndBadTokens(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	url := startGatewayWith(t, config.Config{AllowPrivate: true, ResumeWindow: time.Minute})
	c, ready := connectReady(t, url, port, fp, nil)
	defer c.c.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	other, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"X-Test-User": []string{"u2"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer other.CloseNow()
	o := &client{t: t, ctx: ctx, c: other}
	o.sendJSON(map[string]any{"t": "resume", "token": ready.Resume})
	if m := o.nextMsg(); m.T != "error" || m.Code != codeResumeGone {
		t.Fatalf("another user's resume: want %s, got %+v", codeResumeGone, m)
	}

	b := dial(t, url)
	b.sendJSON(map[string]any{"t": "resume", "token": "nope"})
	if m := b.nextMsg(); m.T != "error" || m.Code != codeResumeGone {
		t.Fatalf("bad token: want %s, got %+v", codeResumeGone, m)
	}
}

func TestResumeTakesOverAndExpires(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	url := startGatewayWith(t, config.Config{AllowPrivate: true, ResumeWindow: 300 * time.Millisecond})
	c, ready := connectReady(t, url, port, fp, nil)

	// A second socket takes over; the first is closed by the gateway.
	r := dial(t, url)
	r.sendJSON(map[string]any{"t": "resume", "token": ready.Resume, "offset": 0})
	if m := r.nextMsg(); m.T != "resumed" {
		t.Fatalf("want resumed, got %+v", m)
	}
	if _, _, err := c.c.Read(c.ctx); err == nil {
		t.Fatal("the old socket should be closed after a takeover")
	}

	// Drop it and wait past the window: the session is gone.
	r.c.CloseNow()
	time.Sleep(700 * time.Millisecond)
	x := dial(t, url)
	x.sendJSON(map[string]any{"t": "resume", "token": ready.Resume})
	if m := x.nextMsg(); m.T != "error" || m.Code != codeResumeGone {
		t.Fatalf("after the window: want %s, got %+v", codeResumeGone, m)
	}
}

func TestExitWhileDetachedIsDeliveredOnResume(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	url := startGatewayWith(t, config.Config{AllowPrivate: true, ResumeWindow: time.Minute})
	c, ready := connectReady(t, url, port, fp, nil)
	c.sendBytes("bye")
	c.waitOutput("echo:bye")
	off := int64(c.out.Len())
	c.sendBytes("exit")
	c.c.CloseNow()
	time.Sleep(200 * time.Millisecond)

	r := dial(t, url)
	r.sendJSON(map[string]any{"t": "resume", "token": ready.Resume, "offset": off})
	if m := r.nextMsg(); m.T != "resumed" {
		t.Fatalf("want resumed, got %+v", m)
	}
	if m := r.nextMsg(); m.T != "exit" || *m.ExitCode != 3 {
		t.Fatalf("want the exit that happened while away, got %+v", m)
	}
}

func TestAgentForwarding(t *testing.T) {
	port, fp, key := testSSHServer(t)
	url := startGateway(t, true)

	c, _ := connectReady(t, url, port, fp, map[string]any{"auth": map[string]string{"type": "key", "key": key}, "agent": true})
	c.waitOutput("agent-keys:1 err:<nil>")

	// Off unless asked for.
	d, _ := connectReady(t, url, port, fp, map[string]any{"auth": map[string]string{"type": "key", "key": key}})
	d.sendBytes("ping")
	d.waitOutput("echo:ping")
	if strings.Contains(d.out.String(), "agent-keys") {
		t.Fatal("agent forwarded without being asked")
	}
}

func TestPortForwardPreview(t *testing.T) {
	// The "remote" web app, reachable from the SSH server's side.
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "webssh_session", Value: "evil"})
		http.SetCookie(w, &http.Cookie{Name: "app", Value: "ok"})
		fmt.Fprintf(w, "path=%s host=%s cookies=%s", r.URL.Path, r.Host, r.Header.Get("Cookie"))
	}))
	defer app.Close()
	appPort := app.Listener.Addr().(*net.TCPAddr).Port

	sshPort, fp, _ := testSSHServerWith(t, serverOpts{forward: true})
	url := startGatewayWith(t, config.Config{AllowPrivate: true, ForwardPorts: []int{9001}})
	base := httpBase(url)
	_, ready := connectReady(t, url, sshPort, fp, nil)

	body, _ := json.Marshal(map[string]any{"handle": ready.SFTP, "port": appPort})
	resp, err := http.Post(base+"/api/forwards", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var f forwardInfo
	_ = json.NewDecoder(resp.Body).Decode(&f)
	resp.Body.Close()
	if f.Port != 9001 || !strings.HasPrefix(f.Auth, authPath+"?t=") {
		t.Fatalf("create forward: %d %+v", resp.StatusCode, f)
	}
	preview := base + "/preview/9001"

	// Without the access link: refused.
	if r, _ := http.Get(preview + "/"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("no cookie: want 403, got %d", r.StatusCode)
	}
	if r, _ := http.Get(preview + authPath + "?t=wrong"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong token: want 404, got %d", r.StatusCode)
	}

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// the test mux serves the preview under a path prefix
		if req.URL.Path == "/" {
			req.URL.Path = "/preview/9001/"
		}
		return nil
	}}
	req, _ := http.NewRequest("GET", preview+f.Auth, nil)
	req.AddCookie(&http.Cookie{Name: "webssh_session", Value: "secret-session"})
	r, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r.Body)
	r.Body.Close()
	s := string(got)
	if r.StatusCode != 200 || !strings.Contains(s, "path=/") || !strings.Contains(s, "host=127.0.0.1:"+strconv.Itoa(appPort)) {
		t.Fatalf("proxied response: %d %q", r.StatusCode, s)
	}
	if strings.Contains(s, "webssh") {
		t.Fatalf("webssh cookies leaked to the forwarded app: %q", s)
	}
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, "webssh") {
			t.Fatalf("forwarded app set a webssh cookie: %v", c)
		}
	}

	// Delete: the preview stops working.
	dreq, _ := http.NewRequest("DELETE", base+"/api/forwards/9001", nil)
	if r, _ := http.DefaultClient.Do(dreq); r.StatusCode != 200 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if r, _ := hc.Get(preview + "/"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("after delete: want 403, got %d", r.StatusCode)
	}
}

func TestPortForwardOwnership(t *testing.T) {
	sshPort, fp, _ := testSSHServerWith(t, serverOpts{forward: true})
	url := startGatewayWith(t, config.Config{AllowPrivate: true, ForwardPorts: []int{9002}})
	_, ready := connectReady(t, url, sshPort, fp, nil)
	body, _ := json.Marshal(map[string]any{"handle": ready.SFTP, "port": 80})
	req, _ := http.NewRequest("POST", httpBase(url)+"/api/forwards", bytes.NewReader(body))
	req.Header.Set("X-Test-User", "u2")
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("forward on someone else's session: want 404, got %d", r.StatusCode)
	}
}
