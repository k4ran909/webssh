package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k4ran909/webssh/internal/config"
	"github.com/k4ran909/webssh/internal/gateway"
	"github.com/k4ran909/webssh/internal/store"
)

func TestRecordings(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OpenSignup = true; c.DataDir = t.TempDir(); c.RecordingDays = 30 })
	admin, user := e.client(), e.client()
	admin.signupWith("admin@example.com", keypair())
	user.signupWith("user@example.com", keypair())
	var me struct{ User struct{ ID string } }
	user.do("GET", "/api/session", nil, &me)

	// The gateway records a session (as it would through the Record hook).
	w := e.api.StartRecording(gateway.RecordedSession{UserID: me.User.ID, Host: "web-01", Login: "deploy", Proto: "ssh", Port: 22, Cols: 80, Rows: 24})
	if w == nil {
		t.Fatal("recording should be on by default")
	}
	w.Output([]byte("$ uptime\r\n 10:00 up 3 days\r\n"))
	w.Close()

	// Admin-with-2FA only.
	var er apiError
	if code := user.do("GET", "/api/admin/recordings", nil, &er); code != 403 {
		t.Fatalf("user: %d", code)
	}
	if code := admin.do("GET", "/api/admin/recordings", nil, &er); code != 403 || er.Code != "NOT_ALLOWED" {
		t.Fatalf("admin without 2FA: %d %+v", code, er)
	}
	var setup struct{ Secret string }
	admin.do("POST", "/api/auth/totp/setup", map[string]string{}, &setup)
	secret, _ := b32.DecodeString(setup.Secret)
	admin.do("POST", "/api/auth/totp/enable", map[string]string{"code": totpCode(secret, time.Now().Unix()/totpPeriod)}, nil)

	var list struct{ Recordings []store.Recording }
	if code := admin.do("GET", "/api/admin/recordings", nil, &list); code != 200 || len(list.Recordings) != 1 {
		t.Fatalf("list: %d %+v", code, list)
	}
	rec := list.Recordings[0]
	if rec.Email != "user@example.com" || rec.Login != "deploy" || rec.Ended.IsZero() || rec.Bytes == 0 {
		t.Fatalf("recording: %+v", rec)
	}

	// Playback returns the asciicast and is audited.
	resp, err := admin.http.Get(e.srv.URL + "/api/admin/recordings/" + rec.ID + "/cast")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"version":2`) || !strings.Contains(string(body), "up 3 days") {
		t.Fatalf("play: %d %s", resp.StatusCode, body)
	}
	var audit struct{ Events []store.AuditEvent }
	admin.do("GET", "/api/admin/audit", nil, &audit)
	if len(audit.Events) == 0 || audit.Events[0].Action != "admin.recording.view" {
		t.Fatalf("audit: %+v", audit.Events)
	}

	// Delete, then the policy switch turns recording off.
	if code := admin.do("DELETE", "/api/admin/recordings/"+rec.ID, nil, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code := admin.do("GET", "/api/admin/recordings/"+rec.ID+"/cast", nil, nil); code != 404 {
		t.Fatalf("after delete: %d", code)
	}
	if code := admin.do("PUT", "/api/admin/settings", map[string]bool{"recordSessions": false}, nil); code != 200 {
		t.Fatalf("policy: %d", code)
	}
	if e.api.StartRecording(gateway.RecordedSession{UserID: me.User.ID, Host: "h", Cols: 80, Rows: 24}) != nil {
		t.Fatal("recording should be off")
	}
}
