package gateway

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// Session monitoring for the gateway admin: list every live session on the
// gateway, then watch or take control of any of them through /ws/watch
// ({"t":"admin-watch", handle, control}). Only sessions the gateway runs can
// be monitored; end-to-end sessions and Telnet aren't visible to it.

func (g *Gateway) canMonitor(userID string) (bool, string) {
	if g.hooks.CanMonitor == nil {
		return false, "Session monitoring isn't available on this gateway."
	}
	return g.hooks.CanMonitor(userID)
}

type liveInfo struct {
	Handle   string    `json:"handle"`
	Email    string    `json:"email"`
	User     string    `json:"user"`
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	Started  time.Time `json:"started"`
	Attached bool      `json:"attached"` // the owner's browser is connected
	Cols     int       `json:"cols"`
	Rows     int       `json:"rows"`
}

func (g *Gateway) monitorRoutes(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/admin/sessions", wrap(http.HandlerFunc(g.listAllSessions)))
}

func (g *Gateway) listAllSessions(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	if ok, why := g.canMonitor(userID); !ok {
		sftpErr(w, http.StatusForbidden, "NOT_ALLOWED", why)
		return
	}
	g.mu.Lock()
	all := make([]*live, 0, len(g.lives))
	for _, l := range g.lives {
		all = append(all, l)
	}
	g.mu.Unlock()
	out := make([]liveInfo, 0, len(all))
	for _, l := range all {
		l.mu.Lock()
		info := liveInfo{Handle: l.id, User: l.user, Host: l.host, Port: l.port, Started: l.started,
			Attached: l.attached != nil, Cols: l.cols, Rows: l.rows}
		l.mu.Unlock()
		if g.hooks.Email != nil {
			info.Email = g.hooks.Email(l.userID)
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sessions": out})
}
