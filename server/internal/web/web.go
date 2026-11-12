// Package web serves the embedded frontend build.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// dist is filled by `npm run build` in apps/web.
//
//go:embed all:dist
var dist embed.FS

func Handler() http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			http.Error(w, "Frontend not built. Run `npm run build` in apps/web, or use the Vite dev server on :5173.",
				http.StatusServiceUnavailable)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if serveGzipped(w, r, sub, p) {
			return
		}
		if _, err := fs.Stat(sub, p); p != "" && err != nil {
			// SPA fallback: unknown paths get the app shell.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

// serveGzipped answers with the precompressed copy (p + ".gz", written at
// build time) of the ~10 MB WebAssembly SSH client, which compresses to about
// a quarter. Only the .gz is embedded: every browser that runs WebAssembly
// accepts gzip.
func serveGzipped(w http.ResponseWriter, r *http.Request, sub fs.FS, p string) bool {
	if !strings.HasSuffix(p, ".wasm") {
		return false
	}
	b, err := fs.ReadFile(sub, p+".gz")
	if err != nil {
		return false
	}
	h := w.Header()
	h.Set("Content-Type", "application/wasm")
	h.Set("Content-Encoding", "gzip")
	h.Set("Vary", "Accept-Encoding")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
	return true
}

// SecurityHeaders sets a strict CSP; any XSS on this origin could drive SSH
// sessions, so no third-party scripts, styles or fonts are allowed.
// 'wasm-unsafe-eval' lets the Argon2id key derivation run as WebAssembly; it
// does not allow eval() of JavaScript.
func SecurityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
		"object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
