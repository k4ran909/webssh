package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestJumpHost(t *testing.T) {
	bastion, bastionFP, _ := testSSHServerWith(t, serverOpts{forward: true})
	target, targetFP, _ := testSSHServer(t)
	c := dial(t, startGateway(t, true))

	msg := connectMsg(target, goodPassword, "")
	msg["jump"] = []map[string]any{{"host": "127.0.0.1", "port": bastion, "user": "alice", "auth": goodPassword}}
	c.sendJSON(msg)

	// One prompt per hop, each naming the host it belongs to.
	for _, want := range []struct {
		port int
		fp   string
	}{{bastion, bastionFP}, {target, targetFP}} {
		m := c.nextMsg()
		if m.T != "hostkey" || m.Status != "new" || m.Port != want.port || m.Fingerprint != want.fp {
			t.Fatalf("want host key prompt for port %d, got %+v", want.port, m)
		}
		c.sendJSON(map[string]string{"t": "hostkey-accept"})
	}
	if m := c.nextMsg(); m.T != "ready" || m.Fingerprint != targetFP {
		t.Fatalf("want ready with the target's fingerprint, got %+v", m)
	}
	c.sendBytes("through-the-bastion")
	c.waitOutput("echo:through-the-bastion")
}

func TestJumpHostAuthFailureNamesTheHop(t *testing.T) {
	bastion, bastionFP, _ := testSSHServerWith(t, serverOpts{forward: true})
	target, targetFP, _ := testSSHServer(t)
	c := dial(t, startGateway(t, true))
	msg := connectMsg(target, goodPassword, targetFP)
	msg["jump"] = []map[string]any{{"host": "127.0.0.1", "port": bastion, "user": "alice",
		"auth": map[string]string{"type": "password", "password": "wrong"}, "knownHostKey": bastionFP}}
	c.sendJSON(msg)
	m := c.nextMsg()
	if m.T != "error" || m.Code != codeAuthFailed || !strings.Contains(m.Msg, "jump host") {
		t.Fatalf("want auth failure on the jump host, got %+v", m)
	}
}

func TestJumpHostThatCannotForward(t *testing.T) {
	bastion, bastionFP, _ := testSSHServer(t) // forwarding disabled
	target, targetFP, _ := testSSHServer(t)
	c := dial(t, startGateway(t, true))
	msg := connectMsg(target, goodPassword, targetFP)
	msg["jump"] = []map[string]any{{"host": "127.0.0.1", "port": bastion, "user": "alice", "auth": goodPassword, "knownHostKey": bastionFP}}
	c.sendJSON(msg)
	if m := c.nextMsg(); m.T != "error" || m.Code != codeUnreachable {
		t.Fatalf("want UNREACHABLE, got %+v", m)
	}
}

type httpc struct {
	t    *testing.T
	base string
	user string
}

func (h httpc) do(method, path string, body io.Reader) (*http.Response, []byte) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.base+path, body)
	if h.user != "" {
		req.Header.Set("X-Test-User", h.user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (h httpc) op(handle string, body map[string]string) int {
	b, _ := json.Marshal(body)
	resp, _ := h.do("POST", "/api/sftp/"+handle+"/op", bytes.NewReader(b))
	return resp.StatusCode
}

type listing struct {
	Path    string
	Entries []entry
}

func (h httpc) ls(handle, p string) listing {
	h.t.Helper()
	resp, b := h.do("GET", "/api/sftp/"+handle+"/ls?path="+url.QueryEscape(p), nil)
	if resp.StatusCode != 200 {
		h.t.Fatalf("ls %q: %d %s", p, resp.StatusCode, b)
	}
	var l listing
	_ = json.Unmarshal(b, &l)
	return l
}

func find(l listing, name string) *entry {
	for i := range l.Entries {
		if l.Entries[i].Name == name {
			return &l.Entries[i]
		}
	}
	return nil
}

func TestSFTP(t *testing.T) {
	root := t.TempDir()
	port, fp, _ := testSSHServerWith(t, serverOpts{sftpRoot: root})
	wsURL := startGateway(t, true)
	c := dial(t, wsURL)
	c.sendJSON(connectMsg(port, goodPassword, fp))
	ready := c.nextMsg()
	if ready.T != "ready" || ready.SFTP == "" {
		t.Fatalf("want ready with an sftp handle, got %+v", ready)
	}
	h := httpc{t: t, base: httpBase(wsURL)}
	hd := ready.SFTP

	// upload, refuse to clobber, overwrite on request
	if resp, b := h.do("PUT", "/api/sftp/"+hd+"/file?path=hello.txt", strings.NewReader("hi there")); resp.StatusCode != 200 {
		t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	if resp, _ := h.do("PUT", "/api/sftp/"+hd+"/file?path=hello.txt", strings.NewReader("x")); resp.StatusCode != 409 {
		t.Fatalf("upload over existing file without overwrite: %d", resp.StatusCode)
	}
	if resp, _ := h.do("PUT", "/api/sftp/"+hd+"/file?path=hello.txt&overwrite=1", strings.NewReader("hello, world")); resp.StatusCode != 200 {
		t.Fatalf("overwrite: %d", resp.StatusCode)
	}

	home := h.ls(hd, "")
	if e := find(home, "hello.txt"); e == nil || e.Size != 12 || e.IsDir {
		t.Fatalf("listing: %+v", home)
	}

	// download is always an opaque attachment
	resp, body := h.do("GET", "/api/sftp/"+hd+"/file?path=hello.txt&download=1", nil)
	if resp.StatusCode != 200 || string(body) != "hello, world" ||
		resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %q %v", resp.StatusCode, body, resp.Header)
	}

	// mkdir, rename into it, chmod, recursive delete
	if code := h.op(hd, map[string]string{"op": "mkdir", "path": "sub"}); code != 200 {
		t.Fatalf("mkdir: %d", code)
	}
	if code := h.op(hd, map[string]string{"op": "rename", "path": "hello.txt", "to": "sub/moved.txt"}); code != 200 {
		t.Fatalf("rename: %d", code)
	}
	if code := h.op(hd, map[string]string{"op": "chmod", "path": "sub/moved.txt", "mode": "0600"}); code != 200 {
		t.Fatalf("chmod: %d", code)
	}
	if code := h.op(hd, map[string]string{"op": "chmod", "path": "sub/moved.txt", "mode": "rwx"}); code != 400 {
		t.Fatalf("bad chmod mode accepted: %d", code)
	}
	sub := h.ls(hd, home.Path+"/sub")
	e := find(sub, "moved.txt")
	if e == nil {
		t.Fatalf("renamed file missing: %+v", sub)
	}
	if runtime.GOOS != "windows" && e.Perm != "0600" { // Windows only has a read-only bit
		t.Fatalf("chmod not applied: %s", e.Perm)
	}
	if find(h.ls(hd, ""), "sub").IsDir != true {
		t.Fatal("sub should be a folder")
	}
	if code := h.op(hd, map[string]string{"op": "remove", "path": "sub"}); code != 200 {
		t.Fatalf("recursive remove: %d", code)
	}
	if find(h.ls(hd, ""), "sub") != nil {
		t.Fatal("folder still there after remove")
	}
	if resp, _ := h.do("GET", "/api/sftp/"+hd+"/file?path=nope.txt", nil); resp.StatusCode != 404 {
		t.Fatalf("missing file: %d", resp.StatusCode)
	}

	// another account can't use the handle
	other := httpc{t: t, base: h.base, user: "u2"}
	if resp, _ := other.do("GET", "/api/sftp/"+hd+"/ls", nil); resp.StatusCode != 404 {
		t.Fatalf("other user's request: want 404, got %d", resp.StatusCode)
	}

	// the handle dies with the session
	c.sendBytes("exit")
	if m := c.nextMsg(); m.T != "exit" {
		t.Fatalf("want exit, got %+v", m)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, _ := h.do("GET", "/api/sftp/"+hd+"/ls", nil)
		if resp.StatusCode == 404 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("handle still usable after the session ended: %d", resp.StatusCode)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSFTPUnavailable(t *testing.T) {
	port, fp, _ := testSSHServer(t) // no sftp subsystem
	wsURL := startGateway(t, true)
	c := dial(t, wsURL)
	c.sendJSON(connectMsg(port, goodPassword, fp))
	ready := c.nextMsg()
	resp, body := httpc{t: t, base: httpBase(wsURL)}.do("GET", "/api/sftp/"+ready.SFTP+"/ls", nil)
	if resp.StatusCode != http.StatusBadGateway || !bytes.Contains(body, []byte("SFTP_UNAVAILABLE")) {
		t.Fatalf("want SFTP_UNAVAILABLE, got %d %s", resp.StatusCode, body)
	}
}
