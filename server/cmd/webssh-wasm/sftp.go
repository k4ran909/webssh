//go:build js && wasm

package main

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"syscall/js"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/k4ran909/webssh/internal/sftpweb"
)

type sftpHandle struct {
	host string
	ssh  *ssh.Client

	mu     sync.Mutex
	client *sftp.Client
}

func (h *sftpHandle) get() (*sftp.Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == nil {
		c, err := sftp.NewClient(h.ssh)
		if err != nil {
			return nil, err
		}
		h.client = c
	}
	return h.client, nil
}

func (h *sftpHandle) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client != nil {
		h.client.Close()
	}
}

// recorder is a minimal http.ResponseWriter that buffers the response.
type recorder struct {
	hdr    http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.hdr }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}
func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

// sftpCall runs one request of the gateway's file API (/api/sftp/{handle}/…)
// against an end-to-end session, inside the browser. File actions in these
// sessions never reach the gateway, so they aren't in the audit log.
func sftpCall(_ js.Value, args []js.Value) any {
	handle, method, url := args[0].String(), args[1].String(), args[2].String()
	var body []byte
	if len(args) > 3 && args[3].Truthy() {
		body = make([]byte, args[3].Get("length").Int())
		js.CopyBytesToGo(body, args[3])
	}
	var executor js.Func
	executor = js.FuncOf(func(_ js.Value, p []js.Value) any {
		resolve := p[0]
		go func() {
			defer executor.Release()
			rec := serveSFTP(handle, method, url, body)
			headers := map[string]any{}
			for k := range rec.hdr {
				headers[k] = rec.hdr.Get(k)
			}
			u := js.Global().Get("Uint8Array").New(rec.body.Len())
			js.CopyBytesToJS(u, rec.body.Bytes())
			resolve.Invoke(js.ValueOf(map[string]any{"status": rec.status, "headers": headers, "body": u}))
		}()
		return nil
	})
	return js.Global().Get("Promise").New(executor)
}

func serveSFTP(handle, method, url string, body []byte) *recorder {
	rec := &recorder{hdr: http.Header{}}
	handlesMu.Lock()
	h := handles[handle]
	handlesMu.Unlock()
	if h == nil {
		sftpweb.Err(rec, http.StatusNotFound, "NO_SESSION", "That SSH session is closed.")
		return rec
	}
	c, err := h.get()
	if err != nil {
		sftpweb.Err(rec, http.StatusBadGateway, "SFTP_UNAVAILABLE", "This server doesn't offer SFTP: "+err.Error())
		return rec
	}
	r, err := http.NewRequest(method, "http://e2e"+url, bytes.NewReader(body))
	if err != nil {
		sftpweb.Err(rec, http.StatusBadRequest, "BAD_REQUEST", "Malformed request.")
		return rec
	}
	noAudit := func(string, map[string]any) {}
	p := r.URL.Path
	switch {
	case method == "GET" && strings.HasSuffix(p, "/ls"):
		sftpweb.List(rec, r, c)
	case method == "GET" && strings.HasSuffix(p, "/file"):
		sftpweb.Read(rec, r, c, h.host, noAudit)
	case method == "PUT" && strings.HasSuffix(p, "/file"):
		sftpweb.Write(rec, r, c, h.host, noAudit)
	case method == "POST" && strings.HasSuffix(p, "/op"):
		sftpweb.Op(rec, r, c, h.host, noAudit)
	default:
		sftpweb.Err(rec, http.StatusNotFound, "NOT_FOUND", "Unknown file API call.")
	}
	return rec
}
