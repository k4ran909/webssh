package api

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"

	"github.com/k4ran909/webssh/internal/store"
)

// Gateway administration: the first account manages users, invitations,
// gateway-wide policy and the full audit log. Admins can't read anyone's
// vault; that stays end-to-end encrypted.

const settingRequire2FA = "require_2fa"

func (a *API) adminRoutes(mux *http.ServeMux) {
	h := func(f http.HandlerFunc) http.Handler { return a.RequireUser(a.requireAdmin(f)) }
	mux.Handle("GET /api/admin/users", h(a.adminUsers))
	mux.Handle("DELETE /api/admin/users/{user}", h(a.adminDeleteUser))
	mux.Handle("GET /api/admin/invites", h(a.adminInvites))
	mux.Handle("POST /api/admin/invites", h(a.adminInvite))
	mux.Handle("DELETE /api/admin/invites", h(a.adminRevokeInvite))
	mux.Handle("GET /api/admin/settings", h(a.adminSettings))
	mux.Handle("PUT /api/admin/settings", h(a.adminSetSettings))
	mux.Handle("GET /api/admin/audit", h(a.adminAudit))
}

func (a *API) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !current(r.Context()).user.IsAdmin {
			writeErr(w, http.StatusForbidden, "NOT_ADMIN", "Only the gateway admin can do that.")
			return
		}
		next(w, r)
	}
}

func (a *API) require2FA(ctx context.Context) bool {
	v, _ := a.db.Setting(ctx, settingRequire2FA)
	return v == "1"
}

// twoFactorExempt are the calls a user needs to satisfy the 2FA policy.
func twoFactorExempt(path string) bool {
	return strings.HasPrefix(path, "/api/auth/totp/") || path == "/api/auth/logout" || path == "/api/me/keypair"
}

func (a *API) adminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.db.ListUsers(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (a *API) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	uid := r.PathValue("user")
	if uid == p.user.ID {
		writeErr(w, http.StatusConflict, "SELF", "You can't delete your own account here.")
		return
	}
	u, err := a.db.UserByID(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NO_USER", "No such user.")
		return
	}
	// Teams they solely own would be left without an owner: refuse.
	if teams, err := a.db.TeamsForUser(r.Context(), uid); err == nil {
		for _, t := range teams {
			if t.Role == "owner" {
				if n, _ := a.db.CountOwners(r.Context(), t.ID); n <= 1 {
					writeErr(w, http.StatusConflict, "SOLE_OWNER", "They're the only owner of team "+t.Name+". Make someone else owner first.")
					return
				}
			}
		}
	}
	if err := a.db.DeleteUser(r.Context(), uid); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(p.user.ID, "admin.user.deleted", a.ClientIP(r), map[string]string{"email": u.Email})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) adminInvites(w http.ResponseWriter, r *http.Request) {
	inv, err := a.db.PendingInvites(r.Context(), "", a.now())
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": inv})
}

func (a *API) adminInvite(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	var req struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !emailRe.MatchString(email) {
		writeErr(w, http.StatusBadRequest, "BAD_EMAIL", "Enter a valid email address.")
		return
	}
	if _, err := a.db.UserByEmail(r.Context(), email); err == nil {
		writeErr(w, http.StatusConflict, "EMAIL_TAKEN", "That person already has an account.")
		return
	}
	token := randomToken()
	h := sha256.Sum256([]byte(token))
	_ = a.db.RevokeInvites(r.Context(), email, "")
	if err := a.db.CreateInvite(r.Context(), h[:], &store.Invite{Email: email, CreatedBy: p.user.ID, ExpiresAt: a.now().Add(inviteTTL)}); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(p.user.ID, "admin.invited", a.ClientIP(r), map[string]string{"email": email})
	writeJSON(w, http.StatusOK, map[string]string{"link": "/?invite=" + token})
}

func (a *API) adminRevokeInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email  string `json:"email"`
		TeamID string `json:"teamId"`
	}
	if !decode(w, r, &req) {
		return
	}
	_ = a.db.RevokeInvites(r.Context(), strings.ToLower(strings.TrimSpace(req.Email)), req.TeamID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) adminSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"require2fa": a.require2FA(r.Context()), "openSignup": a.cfg.OpenSignup})
}

func (a *API) adminSetSettings(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	var req struct {
		Require2FA *bool `json:"require2fa"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Require2FA != nil {
		if *req.Require2FA && p.user.TOTPSecret == nil {
			writeErr(w, http.StatusConflict, "ADMIN_NO_2FA", "Turn on two-factor for your own account first.")
			return
		}
		v := "0"
		if *req.Require2FA {
			v = "1"
		}
		if err := a.db.SetSetting(r.Context(), settingRequire2FA, v); err != nil {
			a.fail(w, err)
			return
		}
		a.Record(p.user.ID, "admin.policy", a.ClientIP(r), map[string]bool{"require2fa": *req.Require2FA})
	}
	a.adminSettings(w, r)
}

func (a *API) adminAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 500
	if q.Get("format") == "csv" {
		limit = 100_000
	}
	events, err := a.db.AllAudit(r.Context(), q.Get("user"), limit)
	if err != nil {
		a.fail(w, err)
		return
	}
	if q.Get("format") == "csv" {
		writeAuditCSV(w, "webssh-audit.csv", events)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
