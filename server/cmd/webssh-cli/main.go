// Command webssh-cli is the companion to the webssh gateway for things a
// browser can't do: TCP forwarding through an SSH session you have open in
// webssh, like ssh's -L, -D and -R.
//
//	webssh-cli forward -gateway https://webssh.example.com [-listen ADDR] [-to ADDR]
//
// Create the forward in webssh first (a window's ⋯ menu → Forward a port);
// it shows the token, which goes in WEBSSH_TOKEN (or -token). The token says
// which kind of forward it is:
//
//   - local:  listens on -listen (default 127.0.0.1:<remote port>) and
//     carries connections to the forward's target on the server's side.
//   - socks:  a SOCKS5 proxy on -listen (default 127.0.0.1:1080); each
//     connection goes wherever the app asks, from the server's network.
//   - remote: the server listens, and its incoming connections are carried
//     to -to on this computer (default 127.0.0.1:<remote port>).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "forward" {
		fmt.Fprintln(os.Stderr, "usage: webssh-cli forward -gateway URL [-listen ADDR] [-to ADDR] [-token TOKEN]")
		fmt.Fprintln(os.Stderr, "\nCreate the forward in webssh (window menu → Forward a port), then run the command it shows.")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("forward", flag.ExitOnError)
	gateway := fs.String("gateway", os.Getenv("WEBSSH_GATEWAY"), "webssh URL, e.g. https://webssh.example.com (or WEBSSH_GATEWAY)")
	listen := fs.String("listen", "", "local and SOCKS forwards: address to listen on")
	to := fs.String("to", "", "remote forwards: where on this computer to send incoming connections")
	token := fs.String("token", "", "forward token (prefer the WEBSSH_TOKEN environment variable, which stays out of shell history)")
	_ = fs.Parse(os.Args[2:])
	if *token == "" {
		*token = os.Getenv("WEBSSH_TOKEN")
	}
	if *gateway == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "webssh-cli: -gateway and a token (WEBSSH_TOKEN or -token) are required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, strings.TrimSuffix(*gateway, "/"), *token, *listen, *to); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "webssh-cli:", err)
		os.Exit(1)
	}
}

type info struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Host   string `json:"host"`
	Code   string `json:"code"`
	Msg    string `json:"msg"`
}

type gw struct {
	base  *url.URL
	token string
}

func (g gw) ws(path string, q url.Values) string {
	u := *g.base
	u.Scheme = map[string]string{"https": "wss", "http": "ws"}[g.base.Scheme]
	u.Path = strings.TrimSuffix(g.base.Path, "/") + path
	u.RawQuery = q.Encode()
	return u.String()
}

func (g gw) dial(ctx context.Context, path string, q url.Values) (*websocket.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ws, res, err := websocket.Dial(dctx, g.ws(path, q), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + g.token}}})
	if err != nil {
		if res != nil {
			if res.StatusCode == http.StatusUnauthorized {
				return nil, errGone
			}
			b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
			if msg := strings.TrimSpace(string(b)); msg != "" {
				return nil, errors.New(msg)
			}
		}
		return nil, err
	}
	return ws, nil
}

var errGone = errors.New("the forward has ended in webssh (stopped, or its session closed)")

func run(ctx context.Context, gateway, token, listen, to string) error {
	base, err := url.Parse(gateway)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return fmt.Errorf("-gateway must be an http(s) URL, got %q", gateway)
	}
	if base.Scheme == "http" && !isLoopback(base.Hostname()) {
		fmt.Fprintln(os.Stderr, "webssh-cli: warning: plain http sends the token and your traffic unencrypted")
	}
	g := gw{base, token}
	in, err := lookup(ctx, gateway, token)
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(in.Target)
	switch in.Kind {
	case "remote":
		if to == "" {
			to = net.JoinHostPort("127.0.0.1", port)
		}
		return remote(ctx, g, in, to)
	case "socks":
		if listen == "" {
			listen = "127.0.0.1:1080"
		}
		return serve(ctx, g, listen, fmt.Sprintf("SOCKS5 proxy on %%s → the network of %s", in.Host), socks)
	default:
		if listen == "" {
			listen = net.JoinHostPort("127.0.0.1", port)
		}
		return serve(ctx, g, listen, fmt.Sprintf("Forwarding %%s → %s on %s", in.Target, in.Host), func(ctx context.Context, g gw, c net.Conn) error {
			ws, err := g.dial(ctx, "/ws/cli/forward", nil)
			if err != nil {
				c.Close()
				return err
			}
			pipe(ctx, ws, c)
			return nil
		})
	}
}

// serve accepts local connections and hands each to handle.
func serve(ctx context.Context, g gw, listen, banner string, handle func(context.Context, gw, net.Conn) error) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w (pick another with -listen)", listen, err)
	}
	defer ln.Close()
	fmt.Printf(banner+" via %s. Ctrl+C to stop.\n", ln.Addr(), g.base.Host)
	go func() { <-ctx.Done(); ln.Close() }()
	ended := make(chan error, 1)
	var wg sync.WaitGroup
	for {
		c, err := ln.Accept()
		if err != nil {
			wg.Wait()
			select {
			case e := <-ended:
				return e
			default:
				return ctx.Err()
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := handle(ctx, g, c); err != nil {
				if errors.Is(err, errGone) {
					select {
					case ended <- err:
					default:
					}
					ln.Close()
					return
				}
				fmt.Fprintln(os.Stderr, "connection:", err)
			}
		}()
	}
}

// pipe copies both ways until either side closes.
func pipe(ctx context.Context, ws *websocket.Conn, c net.Conn) {
	defer c.Close()
	defer ws.CloseNow()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	remote := websocket.NetConn(cctx, ws, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, remote); done <- struct{}{} }()
	<-done
}

// socks answers one SOCKS5 CONNECT (no authentication; it only listens on
// loopback by default) and carries the connection from the server's side.
func socks(ctx context.Context, g gw, c net.Conn) error {
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	fail := func(code byte, err error) error {
		_, _ = c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
		c.Close()
		return err
	}
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
		c.Close()
		return nil // not SOCKS5; ignore quietly
	}
	if _, err := io.ReadFull(c, make([]byte, hdr[1])); err != nil {
		c.Close()
		return nil
	}
	if _, err := c.Write([]byte{5, 0}); err != nil { // no authentication
		c.Close()
		return nil
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		c.Close()
		return nil
	}
	if req[1] != 1 { // only CONNECT
		return fail(7, nil)
	}
	var host string
	switch req[3] {
	case 1, 4:
		b := make([]byte, map[byte]int{1: 4, 4: 16}[req[3]])
		if _, err := io.ReadFull(c, b); err != nil {
			c.Close()
			return nil
		}
		host = net.IP(b).String()
	case 3:
		n := make([]byte, 1)
		if _, err := io.ReadFull(c, n); err != nil {
			c.Close()
			return nil
		}
		b := make([]byte, n[0])
		if _, err := io.ReadFull(c, b); err != nil {
			c.Close()
			return nil
		}
		host = string(b)
	default:
		return fail(8, nil)
	}
	p := make([]byte, 2)
	if _, err := io.ReadFull(c, p); err != nil {
		c.Close()
		return nil
	}
	dest := net.JoinHostPort(host, fmt.Sprint(int(p[0])<<8|int(p[1])))
	ws, err := g.dial(ctx, "/ws/cli/forward", url.Values{"to": {dest}})
	if err != nil {
		if errors.Is(err, errGone) {
			return fail(1, err)
		}
		return fail(5, fmt.Errorf("%s: %w", dest, err)) // connection refused
	}
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		ws.CloseNow()
		c.Close()
		return nil
	}
	_ = c.SetDeadline(time.Time{})
	pipe(ctx, ws, c)
	return nil
}

// remote keeps the control socket open; for each connection the server
// receives, it connects to `to` here and fetches the connection.
func remote(ctx context.Context, g gw, in *info, to string) error {
	ctl, err := g.dial(ctx, "/ws/cli/control", nil)
	if err != nil {
		return err
	}
	defer ctl.CloseNow()
	fmt.Printf("Forwarding %s on %s → %s on this computer via %s. Ctrl+C to stop.\n", in.Target, in.Host, to, g.base.Host)
	for {
		typ, data, err := ctl.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errGone
		}
		var m struct{ T, ID string }
		if typ != websocket.MessageText || json.Unmarshal(data, &m) != nil || m.T != "conn" {
			continue
		}
		go func() {
			local, err := net.DialTimeout("tcp", to, 10*time.Second)
			if err != nil {
				fmt.Fprintln(os.Stderr, "connection:", err)
				return
			}
			ws, err := g.dial(ctx, "/ws/cli/forward", url.Values{"accept": {m.ID}})
			if err != nil {
				local.Close()
				fmt.Fprintln(os.Stderr, "connection:", err)
				return
			}
			pipe(ctx, ws, local)
		}()
	}
}

func lookup(ctx context.Context, gateway, token string) (*info, error) {
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, "GET", gateway+"/api/cli/forward", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach %s: %w", gateway, err)
	}
	defer res.Body.Close()
	var in info
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&in)
	if res.StatusCode != http.StatusOK {
		if in.Msg == "" {
			in.Msg = res.Status
		}
		return nil, errors.New(in.Msg)
	}
	return &in, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
