package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func dialAs(t *testing.T, url, user string) *client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"X-Test-User": {user}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return &client{t: t, ctx: ctx, c: c}
}

func TestAdminMonitoring(t *testing.T) {
	port, fp, _ := testSSHServer(t)
	url := startGateway(t, true)
	base := httpBase(url)
	owner, ready := connectReady(t, url, port, fp, nil)

	list := func(user string) (int, []liveInfo) {
		req, _ := http.NewRequest("GET", base+"/api/admin/sessions", nil)
		req.Header.Set("X-Test-User", user)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var out struct{ Sessions []liveInfo }
		_ = json.NewDecoder(r.Body).Decode(&out)
		return r.StatusCode, out.Sessions
	}
	if code, _ := list("u2"); code != http.StatusForbidden {
		t.Fatalf("non-admin listing: %d", code)
	}
	code, sessions := list("admin")
	if code != 200 || len(sessions) != 1 || sessions[0].Handle != ready.SFTP || !sessions[0].Attached {
		t.Fatalf("admin listing: %d %+v", code, sessions)
	}

	// A non-admin can't join by handle.
	nope := dialAs(t, strings.TrimSuffix(url, "/ws")+"/watch", "u2")
	nope.sendJSON(map[string]any{"t": "admin-watch", "handle": ready.SFTP})
	if m := nope.nextMsg(); m.T != "error" || m.Code != "NOT_ALLOWED" {
		t.Fatalf("non-admin watch: %+v", m)
	}

	// The admin takes control: sees output and types into the session.
	owner.sendBytes("before")
	owner.waitOutput("echo:before")
	adm := dialAs(t, strings.TrimSuffix(url, "/ws")+"/watch", "admin")
	adm.sendJSON(map[string]any{"t": "admin-watch", "handle": ready.SFTP, "control": true})
	if _, data, err := adm.c.Read(adm.ctx); err != nil || !strings.Contains(string(data), `"mode":"control"`) {
		t.Fatalf("admin hello: %s %v", data, err)
	}
	adm.waitOutput("echo:before")
	adm.sendBytes("fromadmin")
	owner.waitOutput("echo:fromadmin")

	// Silent: the owner's share list doesn't show the admin.
	req, _ := http.NewRequest("GET", base+"/api/shares", nil)
	r, _ := http.DefaultClient.Do(req)
	var shares struct{ Shares []shareInfo }
	_ = json.NewDecoder(r.Body).Decode(&shares)
	r.Body.Close()
	if len(shares.Shares) != 0 {
		t.Fatalf("owner sees the admin: %+v", shares.Shares)
	}
}
