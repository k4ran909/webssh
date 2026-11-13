package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Session sharing: the owner of a live session creates a share link, and
// anyone signed in to this gateway who has the link can watch the terminal
// live ("view") or also type into it ("control"). Watchers join with a
// replay of recent output, so they see the current screen. The owner sees
// who is watching and can stop the share at any time; it ends with the session.
//
//	client → server  text    {"t":"watch", token}            (first message)
//	                 binary  keystrokes (control shares only)
//	server → client  text    {"t":"watching", mode, owner, host, cols, rows}
//	                 text    {"t":"size", cols, rows}         the owner resized
//	                 text    {"t":"exit"} | {"t":"error", code, msg}
//	                 binary  terminal output

const (
	maxSharesPerSession = 3
	maxWatchersPerShare = 10
	watcherBuffer       = 256 // output chunks; a watcher that falls further behind is dropped
)

type share struct {
	id, userID, mode string
	tokenHash        [32]byte
	live             *live
	created          time.Time
}

type watcherOut struct {
	text []byte
	bin  []byte
}

type watcher struct {
	share   *share
	userID  string
	email   string
	since   time.Time
	out     chan watcherOut
	done    chan struct{}
	stopped sync.Once
}

func (w *watcher) stop() { w.stopped.Do(func() { close(w.done) }) }

// push queues output for a watcher without ever blocking the session; a
// watcher that can't keep up is disconnected. Call with l.mu held.
func (l *live) push(o watcherOut) {
	for w := range l.watchers {
		select {
		case w.out <- o:
		default:
			delete(l.watchers, w)
			w.stop()
		}
	}
}

func (l *live) setSize(cols, rows int) {
	l.mu.Lock()
	l.cols, l.rows = cols, rows
	b, _ := json.Marshal(map[string]any{"t": "size", "cols": cols, "rows": rows})
	l.push(watcherOut{text: b})
	l.mu.Unlock()
}

// join adds a watcher and returns the output it should replay first. Holding
// writeMu means no output slips between the replay and the live stream.
func (l *live) join(w *watcher) (replay []byte, cols, rows int, ok bool) {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return nil, 0, 0, false
	}
	n := 0
	for o := range l.watchers {
		if o.share == w.share {
			n++
		}
	}
	if n >= maxWatchersPerShare {
		return nil, 0, 0, false
	}
	l.watchers[w] = struct{}{}
	return append([]byte(nil), l.ring...), l.cols, l.rows, true
}

func (l *live) leave(w *watcher) {
	l.mu.Lock()
	delete(l.watchers, w)
	l.mu.Unlock()
	w.stop()
}

// stopShare disconnects everyone who joined through s.
func (l *live) stopShare(s *share) {
	l.mu.Lock()
	for w := range l.watchers {
		if w.share == s {
			delete(l.watchers, w)
			w.stop()
		}
	}
	l.mu.Unlock()
}

func (g *Gateway) shareRoutes(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/shares", wrap(http.HandlerFunc(g.listShares)))
	mux.Handle("POST /api/shares", wrap(http.HandlerFunc(g.createShare)))
	mux.Handle("DELETE /api/shares/{id}", wrap(http.HandlerFunc(g.deleteShare)))
}

type watcherInfo struct {
	Email string    `json:"email"`
	Since time.Time `json:"since"`
}

type shareInfo struct {
	ID       string        `json:"id"`
	Handle   string        `json:"handle"`
	Mode     string        `json:"mode"`
	Watchers []watcherInfo `json:"watchers"`
	Token    string        `json:"token,omitempty"` // only to the creator, once
}

func (g *Gateway) listShares(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	g.mu.Lock()
	var mine []*share
	for _, s := range g.shares {
		if s.userID == userID {
			mine = append(mine, s)
		}
	}
	g.mu.Unlock()
	out := []shareInfo{}
	for _, s := range mine {
		info := shareInfo{ID: s.id, Handle: s.live.id, Mode: s.mode, Watchers: []watcherInfo{}}
		s.live.mu.Lock()
		for wt := range s.live.watchers {
			if wt.share == s {
				info.Watchers = append(info.Watchers, watcherInfo{Email: wt.email, Since: wt.since})
			}
		}
		s.live.mu.Unlock()
		out = append(out, info)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"shares": out})
}

func (g *Gateway) createShare(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	var req struct {
		Handle string `json:"handle"`
		Mode   string `json:"mode"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil || (req.Mode != "view" && req.Mode != "control") {
		sftpErr(w, http.StatusBadRequest, "BAD_REQUEST", "Mode must be view or control.")
		return
	}
	token := randomToken()
	s := &share{id: randomToken()[:12], userID: userID, mode: req.Mode, tokenHash: sha256.Sum256([]byte(token)), created: time.Now()}
	g.mu.Lock()
	for _, l := range g.lives {
		if l.id == req.Handle && l.userID == userID {
			s.live = l
		}
	}
	if s.live == nil {
		g.mu.Unlock()
		sftpErr(w, http.StatusNotFound, "NO_SESSION", "That SSH session is closed.")
		return
	}
	n := 0
	for _, o := range g.shares {
		if o.live == s.live {
			n++
		}
	}
	if n >= maxSharesPerSession {
		g.mu.Unlock()
		sftpErr(w, http.StatusConflict, "TOO_MANY", "This session already has several share links. Stop one first.")
		return
	}
	g.shares[s.id] = s
	g.mu.Unlock()
	g.hooks.Audit(userID, "share.open", g.hooks.ClientIP(r), map[string]any{"host": s.live.host, "mode": s.mode})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(shareInfo{ID: s.id, Handle: req.Handle, Mode: s.mode, Watchers: []watcherInfo{}, Token: token})
}

func (g *Gateway) deleteShare(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	g.mu.Lock()
	s := g.shares[r.PathValue("id")]
	if s != nil && s.userID == userID {
		delete(g.shares, s.id)
	} else {
		s = nil
	}
	g.mu.Unlock()
	if s == nil {
		sftpErr(w, http.StatusNotFound, "NOT_FOUND", "No such share.")
		return
	}
	s.live.stopShare(s)
	g.hooks.Audit(userID, "share.close", g.hooks.ClientIP(r), map[string]any{"host": s.live.host, "mode": s.mode})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// ServeWatch carries one watcher's socket.
func (g *Gateway) ServeWatch(w http.ResponseWriter, r *http.Request) {
	userID, ok := g.hooks.User(r)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: g.cfg.AllowedOrigins})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxFrameBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	send := func(v any) error {
		b, _ := json.Marshal(v)
		return ws.Write(ctx, websocket.MessageText, b)
	}
	fail := func(code, msg string) {
		_ = send(serverMsg{T: "error", Code: code, Msg: msg})
		ws.Close(websocket.StatusNormalClosure, "")
	}

	fctx, fcancel := context.WithTimeout(ctx, connectTimeout)
	typ, data, err := ws.Read(fctx)
	fcancel()
	var first struct {
		T     string `json:"t"`
		Token string `json:"token"`
		// admin-watch: the gateway admin joins any session by its handle.
		Handle  string `json:"handle"`
		Control bool   `json:"control"`
	}
	if err != nil || typ != websocket.MessageText || json.Unmarshal(data, &first) != nil || (first.T != "watch" && first.T != "admin-watch") {
		fail(codeBadRequest, "expected a watch message")
		return
	}
	var s *share
	monitoring := first.T == "admin-watch"
	if monitoring {
		if ok, why := g.canMonitor(userID); !ok {
			fail("NOT_ALLOWED", why)
			return
		}
		g.mu.Lock()
		for _, l := range g.lives {
			if l.id == first.Handle {
				mode := "view"
				if first.Control {
					mode = "control"
				}
				// A private share: never listed to the session's owner.
				s = &share{id: "admin", userID: l.userID, mode: mode, live: l, created: time.Now()}
			}
		}
		g.mu.Unlock()
		if s == nil {
			fail("SHARE_GONE", "That session has ended.")
			return
		}
	} else {
		sum := sha256.Sum256([]byte(first.Token))
		g.mu.Lock()
		for _, o := range g.shares {
			if subtle.ConstantTimeCompare(sum[:], o.tokenHash[:]) == 1 {
				s = o
			}
		}
		g.mu.Unlock()
		if s == nil {
			fail("SHARE_GONE", "This share link has ended (the session closed, or its owner stopped sharing).")
			return
		}
	}
	l := s.live
	wt := &watcher{share: s, userID: userID, since: time.Now(), out: make(chan watcherOut, watcherBuffer), done: make(chan struct{})}
	if g.hooks.Email != nil {
		wt.email = g.hooks.Email(userID)
	}
	replay, cols, rows, ok := l.join(wt)
	if !ok {
		fail("SHARE_FULL", "This session has ended, or too many people are watching it.")
		return
	}
	defer l.leave(wt)
	owner := ""
	if g.hooks.Email != nil {
		owner = g.hooks.Email(s.userID)
	}
	if monitoring {
		// Recorded for the admin only; the session's owner isn't told.
		g.hooks.Audit(userID, "admin.session.watch", g.hooks.ClientIP(r), map[string]any{"host": l.host, "user": l.user, "mode": s.mode, "owner": owner})
	} else {
		g.hooks.Audit(userID, "share.join", g.hooks.ClientIP(r), map[string]any{"host": l.host, "mode": s.mode, "owner": owner})
		g.hooks.Audit(s.userID, "share.joined", g.hooks.ClientIP(r), map[string]any{"host": l.host, "mode": s.mode, "watcher": wt.email})
	}

	if send(map[string]any{"t": "watching", "mode": s.mode, "owner": owner, "host": l.user + "@" + l.host, "cols": cols, "rows": rows}) != nil {
		return
	}
	for len(replay) > 0 {
		n := min(len(replay), outputChunk)
		if ws.Write(ctx, websocket.MessageBinary, replay[:n]) != nil {
			return
		}
		replay = replay[n:]
	}

	// Input from a control share goes to the shell; view shares ignore it.
	go func() {
		defer wt.stop()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary && s.mode == "control" {
				if _, err := l.stdin.Write(data); err != nil {
					return
				}
				l.bytesIn.Add(int64(len(data)))
			}
		}
	}()
	for {
		select {
		case o := <-wt.out:
			var err error
			if o.text != nil {
				err = ws.Write(ctx, websocket.MessageText, o.text)
			} else {
				err = ws.Write(ctx, websocket.MessageBinary, o.bin)
			}
			if err != nil {
				return
			}
		case <-wt.done:
			select {
			case <-l.ctx.Done():
				_ = send(map[string]string{"t": "exit"})
			default:
				fail("SHARE_GONE", "The owner stopped sharing.")
			}
			ws.Close(websocket.StatusNormalClosure, "")
			return
		case <-ctx.Done():
			return
		}
	}
}
