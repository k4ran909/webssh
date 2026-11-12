//go:build js && wasm

// Command webssh-wasm is the SSH client for end-to-end sessions. It runs in
// the browser as WebAssembly, so passwords, keys and terminal contents never
// reach the gateway: the gateway only relays the encrypted TCP stream
// (/ws/relay). It speaks the same terminal protocol as the gateway (see
// internal/sshcore), and serves the same SFTP file API (internal/sftpweb).
//
// JavaScript API, on globalThis.websshE2E:
//
//	open(relayURL, onText(string), onBinary(Uint8Array), onClose()) → {send(string|Uint8Array), close()}
//	sftp(handle, method, url, body?: Uint8Array) → Promise<{status, headers, body: Uint8Array}>
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"syscall/js"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/k4ran909/webssh/internal/sshcore"
)

const (
	dialTimeout = 10 * time.Second
	outputChunk = 32 << 10
)

func main() {
	js.Global().Set("websshE2E", js.ValueOf(map[string]any{
		"open": js.FuncOf(open),
		"sftp": js.FuncOf(sftpCall),
	}))
	select {}
}

type frame struct {
	text bool
	data []byte
}

// term is one terminal: the JS side treats it like a WebSocket to the gateway.
type term struct {
	relayURL                  string
	onText, onBinary, onClose js.Value
	ctx                       context.Context
	cancel                    context.CancelFunc
	funcs                     []js.Func
	closeOnce                 sync.Once

	// Frames from JS. send() runs on the JS event loop and must not block,
	// so it appends to a queue that run() drains.
	mu     sync.Mutex
	queue  []frame
	notify chan struct{}
}

func open(_ js.Value, args []js.Value) any {
	t := &term{relayURL: args[0].String(), onText: args[1], onBinary: args[2], onClose: args[3], notify: make(chan struct{}, 1)}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	send := js.FuncOf(func(_ js.Value, a []js.Value) any {
		var f frame
		if a[0].Type() == js.TypeString {
			f = frame{text: true, data: []byte(a[0].String())}
		} else {
			f.data = make([]byte, a[0].Get("length").Int())
			js.CopyBytesToGo(f.data, a[0])
		}
		t.mu.Lock()
		t.queue = append(t.queue, f)
		t.mu.Unlock()
		select {
		case t.notify <- struct{}{}:
		default:
		}
		return nil
	})
	closeFn := js.FuncOf(func(js.Value, []js.Value) any { t.cancel(); return nil })
	t.funcs = []js.Func{send, closeFn}
	go t.run()
	return js.ValueOf(map[string]any{"send": send, "close": closeFn})
}

// next returns the next frame from JS, or false once the terminal is closed.
func (t *term) next(timeout <-chan time.Time) (frame, bool) {
	for {
		t.mu.Lock()
		if len(t.queue) > 0 {
			f := t.queue[0]
			t.queue = t.queue[1:]
			t.mu.Unlock()
			return f, true
		}
		t.mu.Unlock()
		select {
		case <-t.notify:
		case <-timeout:
			return frame{}, false
		case <-t.ctx.Done():
			return frame{}, false
		}
	}
}

func (t *term) text(m sshcore.ServerMsg) {
	b, _ := json.Marshal(m)
	t.onText.Invoke(string(b))
}

func (t *term) binary(b []byte) {
	u := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u, b)
	t.onBinary.Invoke(u)
}

func (t *term) fail(err error, fallback string) {
	e := sshcore.AsError(err, fallback)
	t.text(sshcore.ServerMsg{T: "error", Code: e.Code, Msg: e.Msg})
}

func (t *term) finish() {
	t.closeOnce.Do(func() {
		t.cancel()
		t.onClose.Invoke()
		for _, f := range t.funcs {
			f.Release()
		}
	})
}

func (t *term) run() {
	defer t.finish()
	f, ok := t.next(time.After(dialTimeout))
	var req sshcore.ClientMsg
	if !ok || !f.text || !sshcore.DecodeMsg(f.data, &req) {
		t.fail(errors.New("expected a connect message"), sshcore.CodeBadRequest)
		return
	}
	if req.T == "resume" {
		t.text(sshcore.ServerMsg{T: "error", Code: sshcore.CodeResumeGone, Msg: "End-to-end sessions can't be resumed."})
		return
	}
	if err := req.Validate(); err != nil {
		t.fail(err, sshcore.CodeBadRequest)
		return
	}
	var keyring agent.Agent
	if req.Agent {
		keyring = sshcore.AgentKeyring(req.Hops())
	}
	d := &sshcore.Dialer{
		Timeout: dialTimeout,
		First:   t.dialRelay,
		Trust:   t.askTrust,
		Changed: func(h *sshcore.Hop, fp, algo string) {
			t.text(sshcore.ServerMsg{T: "hostkey", Status: "changed", Host: h.Host, Port: h.Port, Fingerprint: fp, Algo: algo})
		},
		Banner: func(msg string) { t.binary([]byte(msg)) },
	}
	conn, err := d.Dial(t.ctx, req.Hops())
	req.DropCredentials()
	if err != nil {
		t.fail(err, sshcore.CodeHandshake)
		return
	}
	defer conn.Close()
	sh, err := sshcore.OpenShell(conn.Client, keyring, req.Term, req.Rows, req.Cols, nil)
	if err != nil {
		t.fail(err, sshcore.CodeSession)
		return
	}
	defer sh.Sess.Close()
	handle := register(conn.Client, req.Host)
	defer unregister(handle)
	go sshcore.Keepalive(t.ctx, conn.Client)
	t.text(sshcore.ServerMsg{T: "ready", Fingerprint: conn.Fingerprint, Algo: conn.Algo, SFTP: handle})

	var pumps sync.WaitGroup
	for _, r := range []io.Reader{sh.Stdout, sh.Stderr} {
		pumps.Add(1)
		go func() {
			defer pumps.Done()
			buf := make([]byte, outputChunk)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					t.binary(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}()
	}
	exited := make(chan int, 1)
	go func() { exited <- sshcore.ExitCode(sh.Sess.Wait()) }()
	go func() {
		for {
			f, ok := t.next(nil)
			if !ok {
				return
			}
			if !f.text {
				_, _ = sh.Stdin.Write(f.data)
				continue
			}
			var m sshcore.ClientMsg
			if sshcore.DecodeMsg(f.data, &m) && m.T == "resize" && sshcore.ValidSize(m.Cols, m.Rows) {
				_ = sh.Sess.WindowChange(m.Rows, m.Cols)
			}
		}
	}()

	select {
	case code := <-exited:
		done := make(chan struct{})
		go func() { pumps.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		t.text(sshcore.ServerMsg{T: "exit", ExitCode: &code})
	case <-t.ctx.Done():
	}
}

// dialRelay asks the gateway to open a TCP connection to the first hop and
// returns it as a net.Conn carrying raw bytes.
func (t *term) dialRelay(ctx context.Context, h *sshcore.Hop) (net.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, t.relayURL, nil)
	if err != nil {
		return nil, &sshcore.Error{Code: sshcore.CodeUnreachable, Msg: "Could not reach the gateway's relay."}
	}
	ws.SetReadLimit(1 << 20)
	b, _ := json.Marshal(map[string]any{"t": "dial", "host": h.Host, "port": h.Port})
	if err := ws.Write(dctx, websocket.MessageText, b); err != nil {
		ws.CloseNow()
		return nil, &sshcore.Error{Code: sshcore.CodeUnreachable, Msg: "The gateway's relay went away."}
	}
	typ, data, err := ws.Read(dctx)
	var reply sshcore.ServerMsg
	if err != nil || typ != websocket.MessageText || json.Unmarshal(data, &reply) != nil {
		ws.CloseNow()
		return nil, &sshcore.Error{Code: sshcore.CodeUnreachable, Msg: "The gateway's relay didn't answer."}
	}
	if reply.T != "open" {
		ws.CloseNow()
		return nil, &sshcore.Error{Code: reply.Code, Msg: reply.Msg}
	}
	// The relay lives as long as the terminal, not the dial timeout.
	return websocket.NetConn(t.ctx, ws, websocket.MessageBinary), nil
}

func (t *term) askTrust(h *sshcore.Hop, fp, algo string) error {
	t.text(sshcore.ServerMsg{T: "hostkey", Status: "new", Host: h.Host, Port: h.Port, Fingerprint: fp, Algo: algo})
	timeout := time.After(sshcore.HostKeyTimeout)
	for {
		f, ok := t.next(timeout)
		if !ok {
			if t.ctx.Err() != nil {
				return t.ctx.Err()
			}
			return &sshcore.Error{Code: sshcore.CodeHostKeyRejected, Msg: "Timed out waiting for host key confirmation."}
		}
		var m sshcore.ClientMsg
		if !f.text || !sshcore.DecodeMsg(f.data, &m) {
			continue
		}
		switch m.T {
		case "hostkey-accept":
			return nil
		case "hostkey-reject":
			return &sshcore.Error{Code: sshcore.CodeHostKeyRejected, Msg: "Host key not trusted. Connection cancelled."}
		}
	}
}

// SFTP handles, one per open terminal, prefixed so the frontend can tell
// them from the gateway's.
var (
	handlesMu sync.Mutex
	handles   = map[string]*sftpHandle{}
)

func register(c *ssh.Client, host string) string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	id := "e2e-" + base64.RawURLEncoding.EncodeToString(b)
	handlesMu.Lock()
	handles[id] = &sftpHandle{host: host, ssh: c}
	handlesMu.Unlock()
	return id
}

func unregister(id string) {
	handlesMu.Lock()
	h := handles[id]
	delete(handles, id)
	handlesMu.Unlock()
	if h != nil {
		h.close()
	}
}
