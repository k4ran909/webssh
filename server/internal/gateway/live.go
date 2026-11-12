package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/k4ran909/webssh/internal/sshcore"
)

// ringSize is how much recent output is kept for a reconnecting browser.
const ringSize = 512 << 10

// live is an SSH shell that outlives its WebSocket. While a socket is
// attached, output streams to it (and into a replay ring). While detached,
// reading pauses so SSH flow control holds the server back, and the session
// waits up to cfg.ResumeWindow for the browser to come back.
type live struct {
	g           *Gateway
	id          string // SFTP / forwarding handle
	resumeToken string
	userID, ip  string
	host, user  string
	port        int
	log         *slog.Logger
	started     time.Time

	ctx      context.Context
	cancel   context.CancelFunc
	client   *ssh.Client
	closeAll func()
	sess     *ssh.Session
	stdin    io.Writer
	pumps    sync.WaitGroup

	mu          sync.Mutex
	cond        *sync.Cond
	attached    *session
	ring        []byte
	start       int64 // stream offset of ring[0]
	exited      *int
	exitSent    bool
	detachTimer *time.Timer

	// writeMu orders socket writes: the ring and the socket see output in the
	// same order, and a replay finishes before new output follows it.
	writeMu sync.Mutex

	endOnce           sync.Once
	bytesIn, bytesOut atomic.Int64
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newLive(g *Gateway, s *session, client *ssh.Client, closeAll func(), req *clientMsg, keyring agent.Agent, log *slog.Logger) (*live, error) {
	sh, err := sshcore.OpenShell(client, keyring, req.Term, req.Rows, req.Cols, func(err error) {
		log.Info("agent forwarding refused", "err", err)
	})
	if err != nil {
		return nil, err
	}
	sess, stdin, stdout, stderr := sh.Sess, sh.Stdin, sh.Stdout, sh.Stderr

	l := &live{
		g: g, userID: s.userID, ip: s.ip, host: req.Host, user: req.User, port: req.Port, log: log, started: time.Now(),
		client: client, closeAll: closeAll, sess: sess, stdin: stdin,
		ring: make([]byte, 0, ringSize), resumeToken: randomToken(),
	}
	l.cond = sync.NewCond(&l.mu)
	l.ctx, l.cancel = context.WithCancel(context.Background())
	context.AfterFunc(l.ctx, func() {
		l.mu.Lock()
		l.cond.Broadcast()
		l.mu.Unlock()
	})
	l.id = g.register(s.userID, client, req.Host)
	g.mu.Lock()
	g.lives[l.resumeToken] = l
	g.mu.Unlock()

	for _, r := range []io.Reader{stdout, stderr} {
		l.pumps.Add(1)
		go func() {
			defer l.pumps.Done()
			l.pump(r)
		}()
	}
	go l.keepalive()
	go l.wait()
	return l, nil
}

// serve carries one socket until it drops, the session ends, or another socket takes over.
func (l *live) serve(s *session) (websocket.StatusCode, string) {
	defer l.detach(s)
	for {
		select {
		case f, ok := <-s.in:
			if !ok {
				return websocket.StatusNormalClosure, ""
			}
			if f.typ == websocket.MessageBinary {
				if _, err := l.stdin.Write(f.data); err != nil {
					return websocket.StatusNormalClosure, "stdin closed"
				}
				l.bytesIn.Add(int64(len(f.data)))
				continue
			}
			var m clientMsg
			if decodeMsg(f.data, &m) && m.T == "resize" && validSize(m.Cols, m.Rows) {
				_ = l.sess.WindowChange(m.Rows, m.Cols)
			}
		case <-l.ctx.Done():
			return websocket.StatusNormalClosure, "session ended"
		case <-s.kicked:
			return websocket.StatusNormalClosure, "session opened elsewhere"
		}
	}
}

// attach makes s the session's socket. A resuming socket first gets the
// output it missed, from offset (bytes it already received since "ready").
func (l *live) attach(s *session, offset int64, resuming bool, hello *serverMsg) {
	l.writeMu.Lock()
	l.mu.Lock()
	if l.detachTimer != nil {
		l.detachTimer.Stop()
		l.detachTimer = nil
	}
	old := l.attached
	end := l.start + int64(len(l.ring))
	from, gap := end, false
	if resuming {
		from = offset
		if from < l.start {
			from, gap = l.start, true
		}
		if from > end {
			from = end
		}
	}
	replay := append([]byte(nil), l.ring[from-l.start:]...)
	l.attached = s
	exited := l.exited != nil
	l.cond.Broadcast()
	l.mu.Unlock()

	if old != nil && old != s {
		old.kick()
	}
	if hello != nil {
		_ = s.send(*hello)
	}
	if resuming {
		_ = s.send(serverMsg{T: "resumed", Offset: from, Gap: gap, SFTP: l.id})
		for len(replay) > 0 {
			n := min(len(replay), outputChunk)
			if s.ws.Write(s.ctx, websocket.MessageBinary, replay[:n]) != nil {
				break
			}
			replay = replay[n:]
		}
	}
	l.writeMu.Unlock()
	if exited {
		go l.finish()
	}
}

func (l *live) detach(s *session) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attached != s {
		return
	}
	l.attached = nil
	window := l.g.cfg.ResumeWindow
	if window <= 0 {
		go l.end()
		return
	}
	l.detachTimer = time.AfterFunc(window, func() {
		l.mu.Lock()
		idle := l.attached == nil
		l.mu.Unlock()
		if idle {
			l.log.Info("session not resumed in time")
			l.end()
		}
	})
}

func (l *live) pump(r io.Reader) {
	buf := make([]byte, outputChunk)
	for {
		n, err := r.Read(buf)
		if n > 0 && !l.emit(buf[:n]) {
			return
		}
		if err != nil {
			return
		}
	}
}

// emit waits for a socket, records b in the ring and writes it. A slow
// browser blocks Write, which stops reads from the SSH channel and lets SSH
// flow control push back on the server.
func (l *live) emit(b []byte) bool {
	for {
		l.mu.Lock()
		for l.attached == nil && l.ctx.Err() == nil {
			l.cond.Wait()
		}
		l.mu.Unlock()
		if l.ctx.Err() != nil {
			return false
		}
		l.writeMu.Lock()
		l.mu.Lock()
		c := l.attached
		if c == nil {
			l.mu.Unlock()
			l.writeMu.Unlock()
			continue
		}
		l.ring = append(l.ring, b...)
		if len(l.ring) > 2*ringSize { // compact rarely; the ring holds 512 KB–1 MB
			drop := len(l.ring) - ringSize
			copy(l.ring, l.ring[drop:])
			l.ring = l.ring[:ringSize]
			l.start += int64(drop)
		}
		l.mu.Unlock()
		err := c.ws.Write(c.ctx, websocket.MessageBinary, b)
		l.writeMu.Unlock()
		l.bytesOut.Add(int64(len(b)))
		if err != nil {
			l.detach(c) // the bytes are in the ring for the resume
		}
		return true
	}
}

func (l *live) wait() {
	code := sshcore.ExitCode(l.sess.Wait())
	l.mu.Lock()
	l.exited = &code
	attached := l.attached != nil
	l.mu.Unlock()
	if attached {
		l.finish()
	}
	// Detached: the exit is delivered on resume, or the timer ends the session.
}

// finish flushes the last output to the attached socket, reports the exit and ends.
func (l *live) finish() {
	done := make(chan struct{})
	go func() { l.pumps.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	l.mu.Lock()
	c := l.attached
	if c == nil || l.exitSent {
		l.mu.Unlock()
		return
	}
	l.exitSent = true
	code := *l.exited
	l.mu.Unlock()
	l.writeMu.Lock()
	_ = c.send(serverMsg{T: "exit", ExitCode: &code})
	l.writeMu.Unlock()
	l.end()
}

func (l *live) end() {
	l.endOnce.Do(func() {
		l.cancel()
		l.sess.Close()
		l.closeAll()
		g := l.g
		g.unregister(l.id)
		g.mu.Lock()
		delete(g.lives, l.resumeToken)
		var fwds []*forward
		for p, f := range g.forwards {
			if f.handle == l.id {
				delete(g.forwards, p)
				fwds = append(fwds, f)
			}
		}
		for id, f := range g.tcpForwards {
			if f.handle == l.id {
				delete(g.tcpForwards, id)
			}
		}
		g.mu.Unlock()
		for _, f := range fwds {
			f.close()
		}
		g.release(l.userID)
		<-g.slots
		l.log.Info("ssh session closed")
		g.hooks.Audit(l.userID, "ssh.session", l.ip, map[string]any{
			"host": l.host, "port": l.port, "user": l.user,
			"seconds": int(time.Since(l.started).Seconds()), "bytesIn": l.bytesIn.Load(), "bytesOut": l.bytesOut.Load(),
		})
	})
}

func (l *live) keepalive() { sshcore.Keepalive(l.ctx, l.client) }

// Shutdown ends every SSH session (server shutdown).
func (g *Gateway) Shutdown() {
	g.mu.Lock()
	all := make([]*live, 0, len(g.lives))
	for _, l := range g.lives {
		all = append(all, l)
	}
	g.mu.Unlock()
	for _, l := range all {
		l.end()
	}
}
