package api

import (
	"net/http"
	"regexp"
	"strconv"
)

const (
	maxItemBytes = 256 << 10
	maxItems     = 5000
)

var itemIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

func (a *API) vaultList(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	items, rev, err := a.db.Items(r.Context(), p.user.ID, since)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "revision": rev})
}

func (a *API) vaultPut(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
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
	n, err := a.db.CountItems(r.Context(), p.user.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	// At the cap, edits to existing items still go through; new ones don't.
	if n >= maxItems {
		if exists, err := a.db.ItemExists(r.Context(), p.user.ID, id); err != nil || !exists {
			writeErr(w, http.StatusInsufficientStorage, "VAULT_FULL", "Vault item limit reached.")
			return
		}
	}
	rev, err := a.db.PutItem(r.Context(), p.user.ID, id, req.Data)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"revision": rev})
}

func (a *API) vaultDelete(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	id := r.PathValue("id")
	if !itemIDRe.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "BAD_ID", "Invalid item id.")
		return
	}
	rev, err := a.db.PutItem(r.Context(), p.user.ID, id, "")
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"revision": rev})
}

func (a *API) auditList(w http.ResponseWriter, r *http.Request) {
	p := current(r.Context())
	events, err := a.db.Audit(r.Context(), p.user.ID, 200)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
