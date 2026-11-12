package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/k4ran909/webssh/internal/sftpweb"
)

// SFTP rides on the SSH connection of an open terminal session, so there is
// no second login. Each session gets an unguessable handle that only its
// owner can use, and that dies with the session.

type sftpHandle struct {
	userID string
	host   string
	ssh    *ssh.Client

	mu     sync.Mutex
	client *sftp.Client
}

func (g *Gateway) register(userID string, c *ssh.Client, host string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	id := base64.RawURLEncoding.EncodeToString(b)
	g.mu.Lock()
	g.handles[id] = &sftpHandle{userID: userID, host: host, ssh: c}
	g.mu.Unlock()
	return id
}

func (g *Gateway) unregister(id string) {
	g.mu.Lock()
	h := g.handles[id]
	delete(g.handles, id)
	g.mu.Unlock()
	if h != nil {
		h.mu.Lock()
		if h.client != nil {
			h.client.Close()
		}
		h.mu.Unlock()
	}
}

// SFTPRoutes mounts the file API; wrap must authenticate the request so
// Hooks.User works.
func (g *Gateway) SFTPRoutes(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/sftp/{handle}/ls", wrap(g.withSFTP(func(w http.ResponseWriter, r *http.Request, c *sftp.Client, _ string, _ sftpweb.Audit) {
		sftpweb.List(w, r, c)
	})))
	mux.Handle("GET /api/sftp/{handle}/file", wrap(g.withSFTP(sftpweb.Read)))
	mux.Handle("PUT /api/sftp/{handle}/file", wrap(g.withSFTP(sftpweb.Write)))
	mux.Handle("POST /api/sftp/{handle}/op", wrap(g.withSFTP(sftpweb.Op)))
}

type sftpFunc func(w http.ResponseWriter, r *http.Request, c *sftp.Client, host string, audit sftpweb.Audit)

func (g *Gateway) withSFTP(f sftpFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := g.hooks.User(r)
		if !ok {
			sftpweb.Err(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in first.")
			return
		}
		g.mu.Lock()
		h := g.handles[r.PathValue("handle")]
		g.mu.Unlock()
		// Someone else's handle looks exactly like a closed one.
		if h == nil || h.userID != userID {
			sftpweb.Err(w, http.StatusNotFound, "NO_SESSION", "That SSH session is closed.")
			return
		}
		h.mu.Lock()
		if h.client == nil {
			c, err := sftp.NewClient(h.ssh)
			if err != nil {
				h.mu.Unlock()
				sftpweb.Err(w, http.StatusBadGateway, "SFTP_UNAVAILABLE", "This server doesn't offer SFTP: "+err.Error())
				return
			}
			h.client = c
		}
		c := h.client
		h.mu.Unlock()
		f(w, r, c, h.host, func(action string, detail map[string]any) {
			g.hooks.Audit(h.userID, action, g.hooks.ClientIP(r), detail)
		})
	})
}

var sftpErr = sftpweb.Err

type entry = sftpweb.Entry
