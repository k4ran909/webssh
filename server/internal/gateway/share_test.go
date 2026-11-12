package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func createShare(t *testing.T, base, handle, mode string) shareInfo {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"handle": handle, "mode": mode})
	r, err := http.Post(base+"/api/shares", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var s shareInfo
	_ = json.NewDecoder(r.Body).Decode(&s)
	if r.StatusCode != 200 || s.Token == "" {
		t.Fatalf("create share: %d %+v", r.StatusCode, s)
	}
	return s
}

func joinShare(t *testing.T, url, token string) (*client, map[string]any) {
	t.Helper()
	w := dial(t, strings.TrimSuffix(url, "/ws")+"/watch")
	w.sendJSON(map[string]string{"t": "watch", "token": token})
	_, data, err := w.c.Read(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return w, m
}

func TestSessionSharing(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	url := startGateway(t, true)
	base := httpBase(url)
	owner, ready := connectReady(t, url, port, fp, nil)
	owner.sendBytes("before")
	owner.waitOutput("echo:before")

	// A control share: the watcher sees what happened before it joined,
	// what happens next, and can type.
	ctl := createShare(t, base, ready.SFTP, "control")
	w, hello := joinShare(t, url, ctl.Token)
	if hello["t"] != "watching" || hello["mode"] != "control" || hello["cols"] == nil {
		t.Fatalf("hello: %+v", hello)
	}
	w.waitOutput("echo:before")
	owner.sendBytes("after")
	w.waitOutput("echo:after")
	w.sendBytes("fromwatcher")
	owner.waitOutput("echo:fromwatcher")

	// The owner sees who is watching.
	r, _ := http.Get(base + "/api/shares")
	var list struct{ Shares []shareInfo }
	_ = json.NewDecoder(r.Body).Decode(&list)
	r.Body.Close()
	if len(list.Shares) != 1 || len(list.Shares[0].Watchers) != 1 || list.Shares[0].Token != "" {
		t.Fatalf("list: %+v", list)
	}

	// A view share ignores input.
	view := createShare(t, base, ready.SFTP, "view")
	v, _ := joinShare(t, url, view.Token)
	v.sendBytes("sneaky")
	owner.sendBytes("marker")
	owner.waitOutput("echo:marker")
	if strings.Contains(owner.out.String(), "sneaky") {
		t.Fatal("a view-only watcher typed into the session")
	}

	// Stopping the share disconnects its watchers; a bad token is refused.
	req, _ := http.NewRequest("DELETE", base+"/api/shares/"+ctl.ID, nil)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 200 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if m := w.nextMsg(); m.T != "error" || m.Code != "SHARE_GONE" {
		t.Fatalf("after stop: %+v", m)
	}
	if _, m := joinShare(t, url, "nope"); m["code"] != "SHARE_GONE" {
		t.Fatalf("bad token: %+v", m)
	}
}
