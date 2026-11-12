package api

import (
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k4ran909/webssh/internal/store"
)

// Teams share a vault the server can't read. The browser of whoever creates
// the team generates a random team key and wraps it (ECDH-ES, P-256) to each
// member's public key. A new member stays unconfirmed until an admin's
// browser wraps the key for them, after checking their key fingerprint.

var roleRank = map[string]int{"viewer": 0, "member": 1, "admin": 2, "owner": 3}

const inviteTTL = 7 * 24 * time.Hour

type keypairReq struct {
	PublicKey     string `json:"publicKey,omitempty"`
	EncPrivateKey string `json:"encPrivateKey,omitempty"`
}

// valid checks shapes only; optional allows both fields to be absent.
func (k keypairReq) valid(optional bool) bool {
	if optional && k.PublicKey == "" && k.EncPrivateKey == "" {
		return true
	}
	_, e1 := b64(k.PublicKey, 60, 200)      // P-256 SPKI is 91 bytes
	_, e2 := b64(k.EncPrivateKey, 100, 600) // IV + AES-GCM(PKCS#8)
	return e1 == nil && e2 == nil
}

func validWrapped(s string) bool {
	return len(s) >= 40 && len(s) <= 1024 && !strings.ContainsAny(s, " \t\r\n")
}

func (a *API) teamRoutes(mux *http.ServeMux) {
	h := func(f http.HandlerFunc) http.Handler { return a.RequireUser(f) }
	mux.Handle("PUT /api/me/keypair", h(a.setKeypair))
	mux.HandleFunc("GET /api/invites/{token}", a.inviteInfo)
	mux.Handle("GET /api/teams", h(a.listTeams))
	mux.Handle("POST /api/teams", h(a.createTeam))
	mux.Handle("PATCH /api/teams/{team}", h(a.teamGuard("admin", false, a.updateTeam)))
	mux.Handle("DELETE /api/teams/{team}", h(a.teamGuard("owner", false, a.deleteTeam)))
	mux.Handle("GET /api/teams/{team}/members", h(a.teamGuard("viewer", false, a.listMembers)))
	mux.Handle("POST /api/teams/{team}/members", h(a.teamGuard("admin", true, a.addMember)))
	mux.Handle("POST /api/teams/{team}/members/{user}/confirm", h(a.teamGuard("admin", true, a.confirmMember)))
	mux.Handle("PATCH /api/teams/{team}/members/{user}", h(a.teamGuard("admin", false, a.setRole)))
	mux.Handle("DELETE /api/teams/{team}/members/{user}", h(a.teamGuard("viewer", false, a.removeMember)))
	mux.Handle("DELETE /api/teams/{team}/invites", h(a.teamGuard("admin", false, a.revokeTeamInvite)))
	mux.Handle("GET /api/teams/{team}/vault", h(a.teamGuard("viewer", true, a.teamVaultList)))
	mux.Handle("PUT /api/teams/{team}/vault/items/{id}", h(a.teamGuard("member", true, a.teamVaultPut)))
	mux.Handle("DELETE /api/teams/{team}/vault/items/{id}", h(a.teamGuard("member", true, a.teamVaultDelete)))
}

func (a *API) setKeypair(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	var req keypairReq
	if !decode(w, r, &req) {
		return
	}
	if !req.valid(false) {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "Malformed key material.")
		return
	}
	if err := a.db.SetKeypair(r.Context(), p.user.ID, req.PublicKey, req.EncPrivateKey); err != nil {
		if errors.Is(err, store.ErrExists) {
			writeErr(w, http.StatusConflict, "KEYPAIR_EXISTS", "This account already has a sharing key.")
			return
		}
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) inviteInfo(w http.ResponseWriter, r *http.Request) {
	if a.rateLimited(w, r) {
		return
	}
	h := sha256.Sum256([]byte(r.PathValue("token")))
	inv, err := a.db.LookupInvite(r.Context(), h[:], a.now())
	if err != nil {
		writeErr(w, http.StatusNotFound, "INVITE_INVALID", "This invitation is invalid or has expired.")
		return
	}
	resp := map[string]string{"email": inv.Email}
	if inv.TeamID != "" {
		var name string
		if teams, err := a.db.TeamsForUser(r.Context(), inv.CreatedBy); err == nil {
			for _, t := range teams {
				if t.ID == inv.TeamID {
					name = t.Name
				}
			}
		}
		resp["team"] = name
	}
	writeJSON(w, http.StatusOK, resp)
}

// teamGuard loads the caller's membership and enforces a minimum role.
// confirmed requires the caller to hold the team key (and pass the team's 2FA rule).
func (a *API) teamGuard(minRole string, confirmed bool, next func(http.ResponseWriter, *http.Request, *store.Team)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := current(r.Context())
		t, err := a.db.Membership(r.Context(), r.PathValue("team"), p.user.ID)
		if err != nil {
			// Not a member looks the same as no such team.
			writeErr(w, http.StatusNotFound, "NO_TEAM", "No such team.")
			return
		}
		if roleRank[t.Role] < roleRank[minRole] {
			writeErr(w, http.StatusForbidden, "ROLE", "Your role in this team doesn't allow that.")
			return
		}
		if confirmed {
			if !t.Confirmed {
				writeErr(w, http.StatusForbidden, "NOT_CONFIRMED", "A team admin still has to confirm you.")
				return
			}
			if t.RequireTOTP && p.user.TOTPSecret == nil {
				writeErr(w, http.StatusForbidden, "TEAM_REQUIRES_2FA", "This team requires two-factor authentication. Turn it on in Account & security.")
				return
			}
		}
		next(w, r, t)
	}
}

func (a *API) listTeams(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	teams, err := a.db.TeamsForUser(r.Context(), p.user.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
}

func (a *API) createTeam(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	var req struct {
		Name       string `json:"name"`
		WrappedKey string `json:"wrappedKey"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 80 || !validWrapped(req.WrappedKey) {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "Give the team a name (up to 80 characters).")
		return
	}
	if p.user.PublicKey == "" {
		writeErr(w, http.StatusConflict, "NO_KEYPAIR", "Unlock your vault once so a sharing key can be created.")
		return
	}
	id := newID()
	if err := a.db.CreateTeam(r.Context(), id, name, p.user.ID, req.WrappedKey); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(p.user.ID, "team.created", a.ClientIP(r), map[string]string{"team": name})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (a *API) updateTeam(w http.ResponseWriter, r *http.Request, t *store.Team) {
	var req struct {
		RequireTOTP *bool `json:"requireTotp"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.RequireTOTP != nil {
		if err := a.db.SetTeamRequireTOTP(r.Context(), t.ID, *req.RequireTOTP); err != nil {
			a.fail(w, err)
			return
		}
		a.Record(current(r.Context()).user.ID, "team.policy", a.ClientIP(r), map[string]any{"team": t.Name, "requireTotp": *req.RequireTOTP})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) deleteTeam(w http.ResponseWriter, r *http.Request, t *store.Team) {
	if err := a.db.DeleteTeam(r.Context(), t.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(current(r.Context()).user.ID, "team.deleted", a.ClientIP(r), map[string]string{"team": t.Name})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) listMembers(w http.ResponseWriter, r *http.Request, t *store.Team) {
	members, err := a.db.Members(r.Context(), t.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	resp := map[string]any{"members": members}
	if roleRank[t.Role] >= roleRank["admin"] {
		if inv, err := a.db.PendingInvites(r.Context(), t.ID, a.now()); err == nil {
			resp["invites"] = inv
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// canAssign: owners can grant anything but owner; admins up to member.
func canAssign(actor, role string) bool {
	if _, ok := roleRank[role]; !ok || role == "owner" {
		return false
	}
	if actor == "owner" {
		return true
	}
	return roleRank[role] < roleRank["admin"]
}

func (a *API) addMember(w http.ResponseWriter, r *http.Request, t *store.Team) {
	p := current(r.Context())
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decode(w, r, &req) || a.rateLimited(w, r, "invite:"+p.user.ID) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !emailRe.MatchString(email) {
		writeErr(w, http.StatusBadRequest, "BAD_EMAIL", "Enter a valid email address.")
		return
	}
	if !canAssign(t.Role, req.Role) {
		writeErr(w, http.StatusForbidden, "ROLE", "You can't give that role.")
		return
	}
	detail := map[string]string{"team": t.Name, "email": email, "role": req.Role}
	if u, err := a.db.UserByEmail(r.Context(), email); err == nil {
		if err := a.db.AddMember(r.Context(), t.ID, u.ID, req.Role, p.user.ID); err != nil {
			if errors.Is(err, store.ErrExists) {
				writeErr(w, http.StatusConflict, "ALREADY_MEMBER", "They're already in this team.")
				return
			}
			a.fail(w, err)
			return
		}
		a.Record(p.user.ID, "team.member.added", a.ClientIP(r), detail)
		writeJSON(w, http.StatusOK, map[string]string{"status": "added"})
		return
	}
	// No account yet: an invitation link that also opens sign-up for this email.
	token := randomToken()
	h := sha256.Sum256([]byte(token))
	_ = a.db.RevokeInvites(r.Context(), email, t.ID)
	if err := a.db.CreateInvite(r.Context(), h[:], &store.Invite{Email: email, TeamID: t.ID, Role: req.Role, CreatedBy: p.user.ID, ExpiresAt: a.now().Add(inviteTTL)}); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(p.user.ID, "team.invited", a.ClientIP(r), detail)
	writeJSON(w, http.StatusOK, map[string]string{"status": "invited", "link": "/?invite=" + token})
}

func (a *API) confirmMember(w http.ResponseWriter, r *http.Request, t *store.Team) {
	var req struct {
		WrappedKey string `json:"wrappedKey"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validWrapped(req.WrappedKey) {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "Malformed key material.")
		return
	}
	uid := r.PathValue("user")
	if err := a.db.ConfirmMember(r.Context(), t.ID, uid, req.WrappedKey); err != nil {
		writeErr(w, http.StatusNotFound, "NO_MEMBER", "That person isn't in this team.")
		return
	}
	email := ""
	if u, err := a.db.UserByID(r.Context(), uid); err == nil {
		email = u.Email
	}
	a.Record(current(r.Context()).user.ID, "team.member.confirmed", a.ClientIP(r), map[string]string{"team": t.Name, "email": email})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// target loads another member and checks the actor outranks them.
func (a *API) target(w http.ResponseWriter, r *http.Request, t *store.Team) (*store.Team, string, bool) {
	uid := r.PathValue("user")
	m, err := a.db.Membership(r.Context(), t.ID, uid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NO_MEMBER", "That person isn't in this team.")
		return nil, "", false
	}
	return m, uid, true
}

func (a *API) setRole(w http.ResponseWriter, r *http.Request, t *store.Team) {
	var req struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	m, uid, ok := a.target(w, r, t)
	if !ok {
		return
	}
	actor := current(r.Context()).user.ID
	// Owners may change anyone (but not into or out of owner by this route);
	// admins only manage members and viewers.
	if uid == actor || m.Role == "owner" || !canAssign(t.Role, req.Role) || (t.Role != "owner" && roleRank[m.Role] >= roleRank["admin"]) {
		writeErr(w, http.StatusForbidden, "ROLE", "You can't change that member's role.")
		return
	}
	if err := a.db.SetMemberRole(r.Context(), t.ID, uid, req.Role); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(actor, "team.member.role", a.ClientIP(r), map[string]string{"team": t.Name, "user": uid, "role": req.Role})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) removeMember(w http.ResponseWriter, r *http.Request, t *store.Team) {
	m, uid, ok := a.target(w, r, t)
	if !ok {
		return
	}
	actor := current(r.Context()).user.ID
	self := uid == actor
	if !self && (roleRank[t.Role] < roleRank["admin"] || roleRank[m.Role] >= roleRank[t.Role]) {
		writeErr(w, http.StatusForbidden, "ROLE", "You can't remove that member.")
		return
	}
	if m.Role == "owner" {
		if n, _ := a.db.CountOwners(r.Context(), t.ID); n <= 1 {
			writeErr(w, http.StatusConflict, "LAST_OWNER", "A team needs an owner. Delete the team instead.")
			return
		}
	}
	if err := a.db.RemoveMember(r.Context(), t.ID, uid); err != nil {
		a.fail(w, err)
		return
	}
	action := "team.member.removed"
	if self {
		action = "team.left"
	}
	a.Record(actor, action, a.ClientIP(r), map[string]string{"team": t.Name, "user": uid})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) revokeTeamInvite(w http.ResponseWriter, r *http.Request, t *store.Team) {
	var req struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &req) {
		return
	}
	_ = a.db.RevokeInvites(r.Context(), strings.ToLower(strings.TrimSpace(req.Email)), t.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) teamVaultList(w http.ResponseWriter, r *http.Request, t *store.Team) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	items, rev, err := a.db.TeamItems(r.Context(), t.ID, since)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "revision": rev, "role": t.Role})
}

func (a *API) teamVaultPut(w http.ResponseWriter, r *http.Request, t *store.Team) {
	id := r.PathValue("id")
	var req struct {
		Data string `json:"data"`
	}
	if !itemIDRe.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "BAD_ID", "Invalid item id.")
		return
	}
	if !decode(w, r, &req) {
		return
	}
	if _, err := b64(req.Data, 28, maxItemBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_ITEM", "Item must be encrypted base64 under 256 KB.")
		return
	}
	n, err := a.db.CountTeamItems(r.Context(), t.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	if n >= maxItems {
		if exists, err := a.db.TeamItemExists(r.Context(), t.ID, id); err != nil || !exists {
			writeErr(w, http.StatusInsufficientStorage, "VAULT_FULL", "Team vault item limit reached.")
			return
		}
	}
	rev, err := a.db.PutTeamItem(r.Context(), t.ID, id, req.Data, current(r.Context()).user.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"revision": rev})
}

func (a *API) teamVaultDelete(w http.ResponseWriter, r *http.Request, t *store.Team) {
	id := r.PathValue("id")
	if !itemIDRe.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "BAD_ID", "Invalid item id.")
		return
	}
	rev, err := a.db.PutTeamItem(r.Context(), t.ID, id, "", current(r.Context()).user.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"revision": rev})
}

// writeAuditCSV streams audit events as CSV.
func writeAuditCSV(w http.ResponseWriter, name string, events []store.AuditEvent) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"time", "email", "action", "ip", "detail"})
	for _, e := range events {
		_ = cw.Write([]string{e.CreatedAt.UTC().Format(time.RFC3339), e.Email, e.Action, e.IP, e.Detail})
	}
	cw.Flush()
}
