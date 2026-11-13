package gateway

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/k4ran909/webssh/internal/sshcore"
)

// Telnet, for old switches, routers and lab gear that don't speak SSH. It is
// unencrypted between the gateway and the device, and the user logs in at
// the device's own prompt, so webssh never handles its credentials. No
// resume, files, forwarding or sharing.

const (
	tnIAC  = 255
	tnDONT = 254
	tnDO   = 253
	tnWONT = 252
	tnWILL = 251
	tnSB   = 250
	tnSE   = 240

	optEcho  = 1
	optSGA   = 3
	optTType = 24
	optNAWS  = 31
)

type telnet struct {
	conn net.Conn
	term string

	mu         sync.Mutex // serialises writes to conn
	naws       bool       // the server asked for window sizes
	cols, rows int
	answered   map[[2]byte]bool // replies already sent, so negotiation can't loop
}

func (t *telnet) write(b []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := t.conn.Write(b)
	return err
}

func (t *telnet) reply(cmd, opt byte) {
	k := [2]byte{cmd, opt}
	t.mu.Lock()
	seen := t.answered[k]
	t.answered[k] = true
	t.mu.Unlock()
	if !seen {
		_ = t.write([]byte{tnIAC, cmd, opt})
	}
}

func (t *telnet) sendSize() {
	t.mu.Lock()
	on, c, r := t.naws, t.cols, t.rows
	t.mu.Unlock()
	if !on {
		return
	}
	b := []byte{tnIAC, tnSB, optNAWS}
	for _, v := range []int{c, r} {
		hi, lo := byte(v>>8), byte(v)
		b = append(b, hi)
		if hi == tnIAC {
			b = append(b, tnIAC)
		}
		b = append(b, lo)
		if lo == tnIAC {
			b = append(b, tnIAC)
		}
	}
	_ = t.write(append(b, tnIAC, tnSE))
}

// negotiate answers one option request. We offer window size and terminal
// type, and let the server echo and suppress go-ahead; everything else is refused.
func (t *telnet) negotiate(cmd, opt byte) {
	switch cmd {
	case tnDO:
		switch opt {
		case optNAWS:
			t.reply(tnWILL, opt)
			t.mu.Lock()
			t.naws = true
			t.mu.Unlock()
			t.sendSize()
		case optTType:
			t.reply(tnWILL, opt)
		default:
			t.reply(tnWONT, opt)
		}
	case tnWILL:
		if opt == optEcho || opt == optSGA {
			t.reply(tnDO, opt)
		} else {
			t.reply(tnDONT, opt)
		}
	}
}

func (t *telnet) subnegotiation(b []byte) {
	if len(b) >= 2 && b[0] == optTType && b[1] == 1 { // SEND
		_ = t.write(append(append([]byte{tnIAC, tnSB, optTType, 0}, bytes.ToUpper([]byte(t.term))...), tnIAC, tnSE))
	}
}

// pump reads from the device, strips telnet commands and passes the text on.
func (t *telnet) pump(out func([]byte) error) error {
	r := bufio.NewReaderSize(t.conn, outputChunk)
	data := make([]byte, 0, outputChunk)
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		err := out(append([]byte(nil), data...))
		data = data[:0]
		return err
	}
	for {
		// Pass on what we have before waiting for more.
		if r.Buffered() == 0 || len(data) == cap(data) {
			if err := flush(); err != nil {
				return err
			}
		}
		c, err := r.ReadByte()
		if err != nil {
			return err
		}
		if c != tnIAC {
			data = append(data, c)
			continue
		}
		cmd, err := r.ReadByte()
		if err != nil {
			return err
		}
		switch cmd {
		case tnIAC: // escaped 255
			data = append(data, tnIAC)
		case tnDO, tnDONT, tnWILL, tnWONT:
			opt, err := r.ReadByte()
			if err != nil {
				return err
			}
			t.negotiate(cmd, opt)
		case tnSB:
			var sb []byte
			for {
				b, err := r.ReadByte()
				if err != nil {
					return err
				}
				if b == tnIAC {
					n, err := r.ReadByte()
					if err != nil {
						return err
					}
					if n == tnSE {
						break
					}
					b = n
				}
				if len(sb) < 256 {
					sb = append(sb, b)
				}
			}
			t.subnegotiation(sb)
		}
	}
}

// startTelnet runs a telnet session on s until either side closes.
func (g *Gateway) startTelnet(s *session, req *clientMsg) {
	select {
	case g.slots <- struct{}{}:
	default:
		s.fail(codeBusy, "Too many open sessions on this gateway. Try again later.")
		return
	}
	defer func() { <-g.slots }()
	if !g.claim(s.userID) {
		s.fail(codeBusy, fmt.Sprintf("You already have %d open sessions. Close one first.", g.cfg.MaxUserSessions))
		return
	}
	defer g.release(s.userID)

	dest := map[string]any{"host": req.Host, "port": req.Port, "proto": "telnet"}
	conn, err := g.dialTarget(s.ctx, req.Host, req.Port)
	if err != nil {
		ge := sshcore.AsError(err, codeUnreachable)
		dest["result"] = ge.Code
		g.hooks.Audit(s.userID, "ssh.connect", s.ip, dest)
		s.fail(ge.Code, ge.Msg)
		return
	}
	defer conn.Close()
	dest["result"] = "ok"
	g.hooks.Audit(s.userID, "ssh.connect", s.ip, dest)
	started := time.Now()
	var in, out atomic.Int64

	t := &telnet{conn: conn, term: req.Term, cols: req.Cols, rows: req.Rows, answered: map[[2]byte]bool{}}
	rec := g.record(s.userID, req.Host, "", "telnet", req.Port, req.Cols, req.Rows)
	defer rec.Close()
	if s.send(serverMsg{T: "ready", Algo: "telnet"}) != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = t.pump(func(b []byte) error {
			out.Add(int64(len(b)))
			rec.Output(b)
			return s.ws.Write(s.ctx, websocket.MessageBinary, b)
		})
	}()
	defer func() {
		g.hooks.Audit(s.userID, "ssh.session", s.ip, map[string]any{
			"host": req.Host, "port": req.Port, "proto": "telnet",
			"seconds": int(time.Since(started).Seconds()), "bytesIn": in.Load(), "bytesOut": out.Load(),
		})
	}()
	for {
		select {
		case f, ok := <-s.in:
			if !ok {
				return
			}
			if f.typ == websocket.MessageBinary {
				// Escape 255 and send Enter as CR LF, as telnet expects.
				b := bytes.ReplaceAll(f.data, []byte{tnIAC}, []byte{tnIAC, tnIAC})
				b = bytes.ReplaceAll(b, []byte("\r"), []byte("\r\n"))
				b = bytes.ReplaceAll(b, []byte("\r\n\n"), []byte("\r\n"))
				if t.write(b) != nil {
					return
				}
				in.Add(int64(len(f.data)))
				continue
			}
			var m clientMsg
			if decodeMsg(f.data, &m) && m.T == "resize" && validSize(m.Cols, m.Rows) {
				t.mu.Lock()
				t.cols, t.rows = m.Cols, m.Rows
				t.mu.Unlock()
				t.sendSize()
				rec.Resize(m.Cols, m.Rows)
			}
		case <-done:
			code := -1
			_ = s.send(serverMsg{T: "exit", ExitCode: &code})
			return
		case <-s.ctx.Done():
			return
		}
	}
}
