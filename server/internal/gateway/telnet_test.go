package gateway

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeTelnet negotiates like a typical device, then records everything it receives.
func fakeTelnet(t *testing.T) (port int, got func() []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var rx []byte
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte{tnIAC, tnDO, optNAWS, tnIAC, tnWILL, optEcho, tnIAC, tnDO, optTType, tnIAC, tnDO, 39,
			tnIAC, tnSB, optTType, 1, tnIAC, tnSE})
		c.Write([]byte("login: \xff\xff"))
		buf := make([]byte, 512)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			mu.Lock()
			rx = append(rx, buf[:n]...)
			mu.Unlock()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return append([]byte(nil), rx...)
	}
}

func TestTelnet(t *testing.T) {
	port, got := fakeTelnet(t)
	c := dial(t, startGateway(t, true))
	c.sendJSON(map[string]any{"t": "connect", "proto": "telnet", "host": "127.0.0.1", "port": port, "cols": 100, "rows": 30})
	if m := c.nextMsg(); m.T != "ready" || m.Algo != "telnet" {
		t.Fatalf("got %+v", m)
	}
	// Protocol bytes are stripped; an escaped 255 comes through as one byte.
	c.waitOutput("login: \xff")
	if bytes.Contains(c.out.Bytes(), []byte{tnIAC, tnDO}) {
		t.Fatalf("negotiation leaked into the terminal: %q", c.out.String())
	}
	c.sendBytes("admin\r")
	want := [][]byte{
		{tnIAC, tnWILL, optNAWS},
		{tnIAC, tnSB, optNAWS, 0, 100, 0, 30, tnIAC, tnSE},
		{tnIAC, tnDO, optEcho},
		{tnIAC, tnWILL, optTType},
		{tnIAC, tnWONT, 39},
		append(append([]byte{tnIAC, tnSB, optTType, 0}, "XTERM-256COLOR"...), tnIAC, tnSE),
		[]byte("admin\r\n"),
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rx := got()
		missing := 0
		for _, w := range want {
			if !bytes.Contains(rx, w) {
				missing++
			}
		}
		if missing == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("device received %v", rx)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A resize is reported to the device.
	c.sendJSON(map[string]any{"t": "resize", "cols": 120, "rows": 40})
	for !bytes.Contains(got(), []byte{tnIAC, tnSB, optNAWS, 0, 120, 0, 40, tnIAC, tnSE}) {
		if time.Now().After(deadline) {
			t.Fatal("resize not sent")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
