package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// HTTP port forwarding. A browser can't listen on the user's machine, so a
// forwarded service is published on one of the gateway's preview ports
// instead. Each forward gets its own port, which makes it its own browser
// origin: the forwarded app can't read or drive the webssh app.
//
// Access is a capability: the creator gets a one-time link that sets a
// per-port cookie. The webssh session cookie is stripped before proxying, and
// the forwarded app can't set cookies named webssh*.

type forward struct {
	port   int
	userID string
	handle string // the SSH session it rides on
	token  string
	target string // host:port as seen from the SSH server
	client *ssh.Client
	proxy  *httputil.ReverseProxy
	tr     *http.Transport
}

func (f *forward) close() { f.tr.CloseIdleConnections() }

const authPath = "/__webssh/auth"

func previewCookie(port int) string { return "webssh_fwd_" + strconv.Itoa(port) }

// ForwardRoutes mounts the forwarding API; wrap must authenticate the request.
func (g *Gateway) ForwardRoutes(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/forwards", wrap(http.HandlerFunc(g.listForwards)))
	mux.Handle("POST /api/forwards", wrap(http.HandlerFunc(g.createForward)))
	mux.Handle("DELETE /api/forwards/{port}", wrap(http.HandlerFunc(g.deleteForward)))
	g.tcpRoutes(mux, wrap)
	g.shareRoutes(mux, wrap)
	g.monitorRoutes(mux, wrap)
}

type forwardInfo struct {
	Port   int    `json:"port"`
	Handle string `json:"handle"`
	Target string `json:"target"`
	// Path that sets the access cookie; only returned to the creator, once.
	Auth string `json:"auth,omitempty"`
}

func (g *Gateway) listForwards(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	g.mu.Lock()
	out := []forwardInfo{}
	for _, f := range g.forwards {
		if f.userID == userID {
			out = append(out, forwardInfo{Port: f.port, Handle: f.handle, Target: f.target})
		}
	}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"forwards": out, "available": len(g.cfg.ForwardPorts) > 0})
}

func (g *Gateway) createForward(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	var req struct {
		Handle string `json:"handle"`
		Host   string `json:"host"`
		Port   int    `json:"port"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil || req.Port < 1 || req.Port > 65535 {
		sftpErr(w, http.StatusBadRequest, "BAD_REQUEST", "Give a port between 1 and 65535.")
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if len(host) > 253 || strings.ContainsAny(host, " /\\@?#") {
		sftpErr(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid host.")
		return
	}
	g.mu.Lock()
	h := g.handles[req.Handle]
	if h == nil || h.userID != userID {
		g.mu.Unlock()
		sftpErr(w, http.StatusNotFound, "NO_SESSION", "That SSH session is closed.")
		return
	}
	port := 0
	for _, p := range g.cfg.ForwardPorts {
		if g.forwards[p] == nil {
			port = p
			break
		}
	}
	if port == 0 {
		g.mu.Unlock()
		msg := "All preview ports are in use. Stop a forward first."
		if len(g.cfg.ForwardPorts) == 0 {
			msg = "Port forwarding is off on this gateway (WEBSSH_FORWARD_PORTS)."
		}
		sftpErr(w, http.StatusConflict, "NO_PORT", msg)
		return
	}
	f := newForward(port, userID, req.Handle, h.ssh, net.JoinHostPort(host, strconv.Itoa(req.Port)))
	g.forwards[port] = f
	g.mu.Unlock()

	g.hooks.Audit(userID, "forward.open", g.hooks.ClientIP(r), map[string]any{"host": h.host, "target": f.target, "port": port})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(forwardInfo{Port: port, Handle: f.handle, Target: f.target, Auth: authPath + "?t=" + f.token})
}

func (g *Gateway) deleteForward(w http.ResponseWriter, r *http.Request) {
	userID, _ := g.hooks.User(r)
	port, _ := strconv.Atoi(r.PathValue("port"))
	g.mu.Lock()
	f := g.forwards[port]
	if f != nil && f.userID == userID {
		delete(g.forwards, port)
	} else {
		f = nil
	}
	g.mu.Unlock()
	if f == nil {
		sftpErr(w, http.StatusNotFound, "NOT_FOUND", "No such forward.")
		return
	}
	f.close()
	g.hooks.Audit(userID, "forward.close", g.hooks.ClientIP(r), map[string]any{"target": f.target, "port": port})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func newForward(port int, userID, handle string, client *ssh.Client, target string) *forward {
	f := &forward{port: port, userID: userID, handle: handle, token: randomToken(), target: target, client: client}
	f.tr = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return client.DialContext(ctx, "tcp", target)
		},
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	f.proxy = &httputil.ReverseProxy{
		Transport: f.tr,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: target})
			pr.Out.Host = target // dev servers often check Host
			pr.Out.Header.Del("Cookie")
			for _, c := range pr.In.Cookies() {
				if !strings.HasPrefix(c.Name, "webssh") {
					pr.Out.AddCookie(c)
				}
			}
			pr.SetXForwarded()
		},
		ModifyResponse: func(resp *http.Response) error {
			// Keep the forwarded app away from webssh's cookies and storage.
			kept := resp.Header.Values("Set-Cookie")[:0:0]
			for _, sc := range resp.Header.Values("Set-Cookie") {
				name, _, _ := strings.Cut(strings.TrimSpace(sc), "=")
				if !strings.HasPrefix(strings.ToLower(name), "webssh") {
					kept = append(kept, sc)
				}
			}
			resp.Header.Del("Set-Cookie")
			for _, sc := range kept {
				resp.Header.Add("Set-Cookie", sc)
			}
			resp.Header.Del("Clear-Site-Data")
			// Absolute redirects to the remote address should stay on the preview.
			if loc, err := url.Parse(resp.Header.Get("Location")); err == nil && loc.Host == target {
				loc.Scheme, loc.Host = "", ""
				resp.Header.Set("Location", loc.String())
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "webssh: nothing answered at %s on the server (%v).\n", target, err)
		},
	}
	return f
}

func (g *Gateway) previewHandler(port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		f := g.forwards[port]
		g.mu.Unlock()
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path == authPath {
			t := r.URL.Query().Get("t")
			if f == nil || subtle.ConstantTimeCompare([]byte(t), []byte(f.token)) != 1 {
				http.Error(w, "webssh: this preview has ended.", http.StatusNotFound)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: previewCookie(port), Value: f.token, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(previewCookie(port))
		if f == nil || err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(f.token)) != 1 {
			http.Error(w, "webssh: open this preview from the Forwarding panel.", http.StatusForbidden)
			return
		}
		f.proxy.ServeHTTP(w, r)
	})
}

// ServePreviews listens on every preview port of host until ctx ends.
func (g *Gateway) ServePreviews(ctx context.Context, host string) error {
	var servers []*http.Server
	for _, p := range g.cfg.ForwardPorts {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err != nil {
			for _, s := range servers {
				s.Close()
			}
			g.mu.Lock()
			g.cfg.ForwardPorts = nil // createForward reports forwarding as off
			g.mu.Unlock()
			return fmt.Errorf("preview port %d: %w", p, err)
		}
		srv := &http.Server{Handler: g.previewHandler(p), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				g.log.Error("preview server", "port", p, "err", err)
			}
		}()
	}
	go func() {
		<-ctx.Done()
		for _, s := range servers {
			s.Close()
		}
	}()
	return nil
}
