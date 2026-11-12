package api

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k4ran909/webssh/internal/config"
	"github.com/k4ran909/webssh/internal/store"
)

// Key material is opaque to the server; random bytes of the right size stand in.
func keypair() map[string]any { return map[string]any{"publicKey": rnd(91), "encPrivateKey": rnd(160)} }

func (c *client) signupWith(email string, extra map[string]any) (account, int) {
	acct := account{email, rnd(32), rnd(32)}
	body := map[string]any{
		"email": email, "kdf": defaultKDF, "salt": rnd(16), "authHash": acct.authHash,
		"wrappedKey": rnd(60), "recoveryWrappedKey": rnd(60), "recoveryAuth": acct.recoveryAuth,
	}
	for k, v := range extra {
		body[k] = v
	}
	return acct, c.do("POST", "/api/auth/signup", body, nil)
}

type teamList struct {
	Teams []store.Team
}

func (c *client) teams() []store.Team {
	var l teamList
	c.do("GET", "/api/teams", nil, &l)
	return l.Teams
}

func TestFirstAccountIsAdmin(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OpenSignup = true })
	a, b := e.client(), e.client()
	a.signupWith("admin@example.com", keypair())
	b.signupWith("user@example.com", keypair())
	if !a.session().User.IsAdmin || b.session().User.IsAdmin {
		t.Fatal("only the first account should be admin")
	}
	if code := b.do("GET", "/api/admin/users", nil, nil); code != 403 {
		t.Fatalf("non-admin listing users: %d", code)
	}
	var users struct{ Users []store.UserSummary }
	a.do("GET", "/api/admin/users", nil, &users)
	if len(users.Users) != 2 {
		t.Fatalf("admin users: %+v", users)
	}
}

func TestKeypairIsSetOnce(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.signupWith("k@example.com", nil)
	if code := c.do("POST", "/api/teams", map[string]string{"name": "ops", "wrappedKey": rnd(100)}, nil); code != 409 {
		t.Fatalf("team without a keypair: want 409, got %d", code)
	}
	if code := c.do("PUT", "/api/me/keypair", keypair(), nil); code != 200 {
		t.Fatalf("set keypair: %d", code)
	}
	if code := c.do("PUT", "/api/me/keypair", keypair(), nil); code != 409 {
		t.Fatalf("replacing a keypair must be refused: %d", code)
	}
	if s := c.session(); s.Keypair == nil || s.Keypair.PublicKey == "" {
		t.Fatalf("session should carry the keypair: %+v", s)
	}
}

func TestTeamLifecycle(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OpenSignup = true })
	owner, bob, eve, viewer := e.client(), e.client(), e.client(), e.client()
	owner.signupWith("owner@example.com", keypair())
	bob.signupWith("bob@example.com", keypair())
	eve.signupWith("eve@example.com", keypair())
	viewer.signupWith("viewer@example.com", keypair())

	var created struct{ ID string }
	if code := owner.do("POST", "/api/teams", map[string]string{"name": "Ops", "wrappedKey": rnd(100)}, &created); code != 200 {
		t.Fatalf("create team: %d", code)
	}
	tid := created.ID
	if ts := owner.teams(); len(ts) != 1 || ts[0].Role != "owner" || !ts[0].Confirmed {
		t.Fatalf("owner teams: %+v", ts)
	}

	// Add bob (existing account): he joins unconfirmed and can't read the vault.
	var add struct{ Status string }
	owner.do("POST", "/api/teams/"+tid+"/members", map[string]string{"email": "bob@example.com", "role": "member"}, &add)
	owner.do("POST", "/api/teams/"+tid+"/members", map[string]string{"email": "viewer@example.com", "role": "viewer"}, nil)
	if add.Status != "added" {
		t.Fatalf("add bob: %+v", add)
	}
	if ts := bob.teams(); len(ts) != 1 || ts[0].Confirmed || ts[0].WrappedKey != "" {
		t.Fatalf("bob before confirmation: %+v", ts)
	}
	var errResp apiError
	if code := bob.do("GET", "/api/teams/"+tid+"/vault", nil, &errResp); code != 403 || errResp.Code != "NOT_CONFIRMED" {
		t.Fatalf("unconfirmed vault read: %d %+v", code, errResp)
	}

	// The owner sees bob's public key, wraps the team key for him.
	var members struct{ Members []store.Member }
	owner.do("GET", "/api/teams/"+tid+"/members", nil, &members)
	var bobID, viewerID string
	for _, m := range members.Members {
		switch m.Email {
		case "bob@example.com":
			bobID = m.UserID
			if m.PublicKey == "" || m.Confirmed {
				t.Fatalf("bob member row: %+v", m)
			}
		case "viewer@example.com":
			viewerID = m.UserID
		}
	}
	if code := bob.do("POST", "/api/teams/"+tid+"/members/"+bobID+"/confirm", map[string]string{"wrappedKey": rnd(100)}, nil); code != 403 {
		t.Fatalf("a member confirming themselves: %d", code)
	}
	for _, id := range []string{bobID, viewerID} {
		if code := owner.do("POST", "/api/teams/"+tid+"/members/"+id+"/confirm", map[string]string{"wrappedKey": rnd(100)}, nil); code != 200 {
			t.Fatalf("confirm: %d", code)
		}
	}

	// Members write, viewers read, outsiders see nothing.
	if code := bob.do("PUT", "/api/teams/"+tid+"/vault/items/host-0001", map[string]string{"data": rnd(80)}, nil); code != 200 {
		t.Fatalf("member write: %d", code)
	}
	if code := viewer.do("PUT", "/api/teams/"+tid+"/vault/items/host-0002", map[string]string{"data": rnd(80)}, nil); code != 403 {
		t.Fatalf("viewer write: want 403, got %d", code)
	}
	var list struct{ Items []store.Item }
	viewer.do("GET", "/api/teams/"+tid+"/vault", nil, &list)
	if len(list.Items) != 1 {
		t.Fatalf("viewer read: %+v", list)
	}
	if code := eve.do("GET", "/api/teams/"+tid+"/vault", nil, nil); code != 404 {
		t.Fatalf("outsider read: want 404, got %d", code)
	}
	if code := eve.do("GET", "/api/teams/"+tid+"/members", nil, nil); code != 404 {
		t.Fatalf("outsider members: want 404, got %d", code)
	}

	// Role rules.
	if code := owner.do("PATCH", "/api/teams/"+tid+"/members/"+bobID, map[string]string{"role": "admin"}, nil); code != 200 {
		t.Fatalf("owner promotes bob: %d", code)
	}
	if code := bob.do("PATCH", "/api/teams/"+tid+"/members/"+viewerID, map[string]string{"role": "admin"}, nil); code != 403 {
		t.Fatalf("admin making an admin: want 403, got %d", code)
	}
	if code := bob.do("PATCH", "/api/teams/"+tid+"/members/"+viewerID, map[string]string{"role": "member"}, nil); code != 200 {
		t.Fatalf("admin promoting viewer to member: %d", code)
	}
	ownerID := owner.session().User.ID
	if code := bob.do("DELETE", "/api/teams/"+tid+"/members/"+ownerID, nil, nil); code != 403 {
		t.Fatalf("admin removing owner: want 403, got %d", code)
	}
	if code := owner.do("DELETE", "/api/teams/"+tid+"/members/"+ownerID, nil, &errResp); code != 409 || errResp.Code != "LAST_OWNER" {
		t.Fatalf("last owner leaving: %d %+v", code, errResp)
	}
	if code := bob.do("DELETE", "/api/teams/"+tid+"/members/"+viewerID, nil, nil); code != 200 {
		t.Fatalf("admin removing member: %d", code)
	}
	if len(viewer.teams()) != 0 {
		t.Fatal("removed member still sees the team")
	}

	// Team 2FA rule.
	owner.do("PATCH", "/api/teams/"+tid, map[string]bool{"requireTotp": true}, nil)
	if code := bob.do("GET", "/api/teams/"+tid+"/vault", nil, &errResp); code != 403 || errResp.Code != "TEAM_REQUIRES_2FA" {
		t.Fatalf("team 2FA rule: %d %+v", code, errResp)
	}

	if code := bob.do("DELETE", "/api/teams/"+tid, nil, nil); code != 403 {
		t.Fatalf("admin deleting team: want 403, got %d", code)
	}
	if code := owner.do("DELETE", "/api/teams/"+tid, nil, nil); code != 200 {
		t.Fatalf("owner deleting team: %d", code)
	}
	if len(bob.teams()) != 0 {
		t.Fatal("team still listed after delete")
	}
}

func TestTeamInviteSignup(t *testing.T) {
	e := newEnv(t) // sign-up closed after the first account
	owner := e.client()
	owner.signupWith("owner@example.com", keypair())
	var created struct{ ID string }
	owner.do("POST", "/api/teams", map[string]string{"name": "Ops", "wrappedKey": rnd(100)}, &created)

	var inv struct{ Status, Link string }
	owner.do("POST", "/api/teams/"+created.ID+"/members", map[string]string{"email": "new@example.com", "role": "member"}, &inv)
	if inv.Status != "invited" || !strings.HasPrefix(inv.Link, "/?invite=") {
		t.Fatalf("invite: %+v", inv)
	}
	token := strings.TrimPrefix(inv.Link, "/?invite=")

	var info map[string]string
	if code := e.client().do("GET", "/api/invites/"+token, nil, &info); code != 200 || info["email"] != "new@example.com" || info["team"] != "Ops" {
		t.Fatalf("invite info: %d %+v", code, info)
	}
	if _, code := e.client().signupWith("someone-else@example.com", map[string]any{"invite": token}); code != 403 {
		t.Fatalf("invite used for another email: want 403, got %d", code)
	}
	if _, code := e.client().signupWith("stranger@example.com", nil); code != 403 {
		t.Fatalf("sign-up without invite: want 403, got %d", code)
	}
	nu := e.client()
	if _, code := nu.signupWith("NEW@example.com", map[string]any{"invite": token, "publicKey": rnd(91), "encPrivateKey": rnd(160)}); code != 200 {
		t.Fatalf("sign-up with invite: %d", code)
	}
	if ts := nu.teams(); len(ts) != 1 || ts[0].Role != "member" || ts[0].Confirmed {
		t.Fatalf("invitee joins unconfirmed: %+v", ts)
	}
	if nu.session().User.IsAdmin {
		t.Fatal("invitee must not be admin")
	}
	if _, code := e.client().signupWith("new2@example.com", map[string]any{"invite": token}); code != 403 {
		t.Fatalf("invite reused: want 403, got %d", code)
	}
}

func TestGatewayRequires2FA(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OpenSignup = true })
	admin, user := e.client(), e.client()
	admin.signupWith("admin@example.com", keypair())
	user.signupWith("user@example.com", keypair())

	var errResp apiError
	if code := admin.do("PUT", "/api/admin/settings", map[string]bool{"require2fa": true}, &errResp); code != 409 || errResp.Code != "ADMIN_NO_2FA" {
		t.Fatalf("policy without admin 2FA: %d %+v", code, errResp)
	}
	// Turn on the admin's own 2FA, then the policy.
	var setup struct{ Secret string }
	admin.do("POST", "/api/auth/totp/setup", map[string]string{}, &setup)
	secret, _ := b32.DecodeString(setup.Secret)
	admin.do("POST", "/api/auth/totp/enable", map[string]string{"code": totpCode(secret, time.Now().Unix()/totpPeriod)}, nil)
	if code := admin.do("PUT", "/api/admin/settings", map[string]bool{"require2fa": true}, nil); code != 200 {
		t.Fatalf("enable policy: %d", code)
	}

	if code := user.do("GET", "/api/vault", nil, &errResp); code != 403 || errResp.Code != "TOTP_SETUP_REQUIRED" {
		t.Fatalf("user without 2FA: %d %+v", code, errResp)
	}
	if code := user.do("POST", "/api/auth/totp/setup", map[string]string{}, nil); code != 200 {
		t.Fatalf("2FA setup must stay reachable: %d", code)
	}
	if s := user.session(); s.Policy == nil || !s.Policy.Require2FA {
		t.Fatalf("session should report the policy: %+v", s.Policy)
	}
	if code := admin.do("GET", "/api/vault", nil, nil); code != 200 {
		t.Fatalf("admin with 2FA: %d", code)
	}
}

func TestAuditCSV(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.signupWith("csv@example.com", keypair())
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/admin/audit?format=csv", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(string(body), "csv@example.com,account.created") {
		t.Fatalf("admin audit csv: %s %q", resp.Header.Get("Content-Type"), body)
	}
}

func TestMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 2; i++ {
		db, err := store.Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		db.Close()
	}
}

func TestTeamKeyRotation(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OpenSignup = true })
	owner, bob, carol := e.client(), e.client(), e.client()
	owner.signupWith("owner@example.com", keypair())
	bob.signupWith("bob@example.com", keypair())
	carol.signupWith("carol@example.com", keypair())
	var created struct{ ID string }
	owner.do("POST", "/api/teams", map[string]string{"name": "Ops", "wrappedKey": rnd(100)}, &created)
	tid := created.ID
	for _, email := range []string{"bob@example.com", "carol@example.com"} {
		owner.do("POST", "/api/teams/"+tid+"/members", map[string]string{"email": email, "role": "member"}, nil)
	}
	var ms struct{ Members []store.Member }
	owner.do("GET", "/api/teams/"+tid+"/members", nil, &ms)
	ids := map[string]string{}
	for _, m := range ms.Members {
		ids[m.Email] = m.UserID
		if m.Email != "owner@example.com" {
			owner.do("POST", "/api/teams/"+tid+"/members/"+m.UserID+"/confirm", map[string]string{"wrappedKey": rnd(100)}, nil)
		}
	}
	item := "/api/teams/" + tid + "/vault/items/11111111-1111-4111-8111-111111111111"
	if code := owner.do("PUT", item, map[string]any{"data": rnd(64), "epoch": 0}, nil); code != 200 {
		t.Fatalf("put: %d", code)
	}

	// Remove carol, then rotate. Leaving out a member, or keeping carol in, is refused.
	owner.do("DELETE", "/api/teams/"+tid+"/members/"+ids["carol@example.com"], nil, nil)
	var v struct{ Revision int64 }
	owner.do("GET", "/api/teams/"+tid+"/vault", nil, &v)
	items := []map[string]string{{"id": "11111111-1111-4111-8111-111111111111", "data": rnd(64)}}
	rotate := func(c *client, keys map[string]string, rev int64) (int, apiError) {
		var er apiError
		code := c.do("POST", "/api/teams/"+tid+"/rotate", map[string]any{"epoch": 0, "revision": rev, "items": items, "keys": keys}, &er)
		return code, er
	}
	full := map[string]string{ids["owner@example.com"]: rnd(100), ids["bob@example.com"]: rnd(100)}
	if code, er := rotate(owner, map[string]string{ids["owner@example.com"]: rnd(100)}, v.Revision); code != 409 || er.Code != "ROTATE_CONFLICT" {
		t.Fatalf("missing member: %d %+v", code, er)
	}
	withCarol := map[string]string{ids["carol@example.com"]: rnd(100)}
	for k, val := range full {
		withCarol[k] = val
	}
	if code, _ := rotate(owner, withCarol, v.Revision); code != 409 {
		t.Fatalf("removed member included: %d", code)
	}
	if code, _ := rotate(owner, full, v.Revision-1); code != 409 {
		t.Fatalf("stale revision: %d", code)
	}
	if code, _ := rotate(bob, full, v.Revision); code != 403 {
		t.Fatalf("a member rotating: %d", code)
	}
	if code, er := rotate(owner, full, v.Revision); code != 200 {
		t.Fatalf("rotate: %d %+v", code, er)
	}

	// Writes with the old key are refused; the new epoch is visible to members.
	var er apiError
	if code := bob.do("PUT", item, map[string]any{"data": rnd(64), "epoch": 0}, &er); code != 409 || er.Code != "KEY_ROTATED" {
		t.Fatalf("stale write: %d %+v", code, er)
	}
	if ts := bob.teams(); len(ts) != 1 || ts[0].KeyEpoch != 1 || ts[0].WrappedKey != full[ids["bob@example.com"]] {
		t.Fatalf("bob after rotation: %+v", ts)
	}
	if code := bob.do("PUT", item, map[string]any{"data": rnd(64), "epoch": 1}, nil); code != 200 {
		t.Fatalf("write with new epoch: %d", code)
	}
}
