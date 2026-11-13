package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/k4ran909/webssh/internal/gateway"
	"github.com/k4ran909/webssh/internal/recording"
	"github.com/k4ran909/webssh/internal/store"
)

// Server-side session recordings. The gateway records the output of every
// session it runs (not end-to-end ones, which it can't see) while the admin's
// "record sessions" policy is on (the default). Only the gateway admin, with
// 2FA, can list, play or delete them; each playback is in the audit log.

const settingRecord = "record_sessions"

var recordingIDRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (a *API) recordingEnabled(ctx context.Context) bool {
	v, _ := a.db.Setting(ctx, settingRecord)
	return v != "0"
}

func (a *API) recordingPath(id string) string {
	return filepath.Join(a.cfg.DataDir, "recordings", id+".cast.gz")
}

// StartRecording is the gateway's Record hook.
func (a *API) StartRecording(s gateway.RecordedSession) *recording.Writer {
	ctx := context.Background()
	if !a.recordingEnabled(ctx) {
		return nil
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(a.recordingPath(id)), 0o700); err != nil {
		a.log.Warn("recording: no directory", "err", err)
		return nil
	}
	title := s.Host
	if s.Login != "" {
		title = s.Login + "@" + s.Host
	}
	w, err := recording.Create(a.recordingPath(id), s.Cols, s.Rows, title)
	if err != nil {
		a.log.Warn("recording: can't create file", "err", err)
		return nil
	}
	if err := a.db.CreateRecording(ctx, store.Recording{ID: id, UserID: s.UserID, Host: s.Host, Login: s.Login, Port: s.Port, Proto: s.Proto, Started: time.Now()}); err != nil {
		a.log.Warn("recording: can't save metadata", "err", err)
	}
	w.OnClose = func(n int64) {
		if err := a.db.FinishRecording(context.Background(), id, n); err != nil {
			a.log.Warn("recording: can't finish", "err", err)
		}
	}
	return w
}

// pruneRecordings deletes recordings older than the retention period.
func (a *API) pruneRecordings(ctx context.Context) {
	if a.cfg.RecordingDays <= 0 {
		return
	}
	ids, err := a.db.RecordingsBefore(ctx, a.now().Add(-time.Duration(a.cfg.RecordingDays)*24*time.Hour))
	if err != nil {
		return
	}
	for _, id := range ids {
		_ = os.Remove(a.recordingPath(id))
		_ = a.db.DeleteRecording(ctx, id)
	}
}

// requireMonitor: recordings are as sensitive as watching live, so they need
// the same admin-with-2FA check.
func (a *API) requireMonitor(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, why := a.CanMonitor(current(r.Context()).user.ID); !ok {
			writeErr(w, http.StatusForbidden, "NOT_ALLOWED", why)
			return
		}
		next(w, r)
	}
}

func (a *API) recordingRoutes(mux *http.ServeMux) {
	h := func(f http.HandlerFunc) http.Handler { return a.RequireUser(a.requireMonitor(f)) }
	mux.Handle("GET /api/admin/recordings", h(a.listRecordings))
	mux.Handle("GET /api/admin/recordings/{id}/cast", h(a.playRecording))
	mux.Handle("DELETE /api/admin/recordings/{id}", h(a.deleteRecording))
}

func (a *API) listRecordings(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	list, err := a.db.ListRecordings(r.Context(), r.URL.Query().Get("user"), limit)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recordings": list, "enabled": a.recordingEnabled(r.Context()), "days": a.cfg.RecordingDays})
}

func (a *API) recordingFor(w http.ResponseWriter, r *http.Request) *store.Recording {
	id := r.PathValue("id")
	if !recordingIDRe.MatchString(id) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "No such recording.")
		return nil
	}
	rec, err := a.db.RecordingByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "No such recording.")
		return nil
	}
	if err != nil {
		a.fail(w, err)
		return nil
	}
	return rec
}

func (a *API) playRecording(w http.ResponseWriter, r *http.Request) {
	rec := a.recordingFor(w, r)
	if rec == nil {
		return
	}
	f, err := recording.Open(a.recordingPath(rec.ID))
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "The recording file is missing.")
		return
	}
	defer f.Close()
	p := current(r.Context())
	a.Record(p.user.ID, "admin.recording.view", a.ClientIP(r), map[string]any{"owner": rec.Email, "host": rec.Host, "started": rec.Started.Unix()})
	w.Header().Set("Content-Type", "application/x-asciicast")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, f)
}

func (a *API) deleteRecording(w http.ResponseWriter, r *http.Request) {
	rec := a.recordingFor(w, r)
	if rec == nil {
		return
	}
	if rec.Ended.IsZero() {
		writeErr(w, http.StatusConflict, "STILL_RECORDING", "That session is still running.")
		return
	}
	_ = os.Remove(a.recordingPath(rec.ID))
	if err := a.db.DeleteRecording(r.Context(), rec.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.Record(current(r.Context()).user.ID, "admin.recording.delete", a.ClientIP(r), map[string]any{"owner": rec.Email, "host": rec.Host, "started": rec.Started.Unix()})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
