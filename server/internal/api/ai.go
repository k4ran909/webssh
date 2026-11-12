package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k4ran909/webssh/internal/ai"
)

// Explainer is the optional AI helper (internal/ai), behind an interface so
// tests don't call a real model.
type Explainer interface {
	Explain(ctx context.Context, text, where string) (string, error)
	Model() string
}

func (a *API) SetExplainer(e Explainer) { a.ai = e }

func (a *API) aiRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/ai", a.RequireUser(http.HandlerFunc(a.aiStatus)))
	mux.Handle("POST /api/ai/explain", a.RequireUser(http.HandlerFunc(a.aiExplain)))
}

func (a *API) aiStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"enabled": a.ai != nil}
	if a.ai != nil {
		out["model"] = a.ai.Model()
	}
	writeJSON(w, http.StatusOK, out)
}

// aiExplain sends text the user selected, and only that, to the model.
// The audit log records that it happened and how much was sent, never the text.
func (a *API) aiExplain(w http.ResponseWriter, r *http.Request) {
	if a.ai == nil {
		writeErr(w, http.StatusNotFound, "AI_OFF", "The AI helper is off on this gateway.")
		return
	}
	p := current(r.Context())
	var req struct {
		Text  string `json:"text"`
		Where string `json:"where"`
	}
	if !decode(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeErr(w, http.StatusBadRequest, "EMPTY", "Select some terminal output first.")
		return
	}
	if len(text) > ai.MaxInput || !utf8.ValidString(text) {
		writeErr(w, http.StatusRequestEntityTooLarge, "TOO_LONG", "Select less output (up to 16 KB).")
		return
	}
	if len(req.Where) > 300 {
		req.Where = req.Where[:300]
	}
	if !a.limit.allow("ai:"+p.user.ID, 30, time.Hour, a.now()) {
		writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "That's a lot of explanations for one hour. Try again later.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	answer, err := a.ai.Explain(ctx, text, req.Where)
	detail := map[string]any{"bytes": len(text), "model": a.ai.Model()}
	if err != nil {
		detail["result"] = "failed"
		a.Record(p.user.ID, "ai.explain", a.ClientIP(r), detail)
		if errors.Is(err, ai.ErrRefused) {
			writeErr(w, http.StatusUnprocessableEntity, "AI_REFUSED", "The AI declined to explain this output.")
			return
		}
		a.log.Warn("ai explain failed", "err", err)
		writeErr(w, http.StatusBadGateway, "AI_FAILED", "The AI helper couldn't answer right now. Try again in a moment.")
		return
	}
	a.Record(p.user.ID, "ai.explain", a.ClientIP(r), detail)
	writeJSON(w, http.StatusOK, map[string]string{"answer": answer})
}
