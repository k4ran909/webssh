// Command webssh-cli is the companion to the webssh gateway for things a
// browser can't do. For now: raw TCP port forwarding (databases, Redis, RDP…)
// through an SSH session you have open in webssh.
//
//	webssh-cli forward -gateway https://webssh.example.com [-listen 127.0.0.1:5432]
//
// Create the forward in webssh first (a window's ⋯ menu → Forward a port →
// Local port); it shows the token, which goes in WEBSSH_TOKEN (or -token).
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
		fmt.Fprintln(os.Stderr, "usage: webssh-cli forward -gateway URL [-listen ADDR] [-token TOKEN]")
		fmt.Fprintln(os.Stderr, "\nCreate the forward in webssh (window menu → Forward a port → Local port), then run the command it shows.")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("forward", flag.ExitOnError)
	gateway := fs.String("gateway", os.Getenv("WEBSSH_GATEWAY"), "webssh URL, e.g. https://webssh.example.com (or WEBSSH_GATEWAY)")
	listen := fs.String("listen", "", "local address to listen on (default 127.0.0.1:<remote port>)")
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
	if err := forward(ctx, strings.TrimSuffix(*gateway, "/"), *token, *listen); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "webssh-cli:", err)
		os.Exit(1)
	}
}

type info struct {
	Target string `json:"target"`
	Host   string `json:"host"`
	Code   string `json:"code"`
	Msg    string `json:"msg"`
}

func forward(ctx context.Context, gateway, token, listen string) error {
	base, err := url.Parse(gateway)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return fmt.Errorf("-gateway must be an http(s) URL, got %q", gateway)
	}
	if base.Scheme == "http" && !isLoopback(base.Hostname()) {
		fmt.Fprintln(os.Stderr, "webssh-cli: warning: plain http sends the token and your traffic unencrypted")
	}
	in, err := lookup(ctx, gateway, token)
	if err != nil {
		return err
	}
	if listen == "" {
		_, port, _ := net.SplitHostPort(in.Target)
		listen = net.JoinHostPort("127.0.0.1", port)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w (pick another with -listen)", listen, err)
	}
	defer ln.Close()
	fmt.Printf("Forwarding %s → %s on %s via %s. Ctrl+C to stop.\n", ln.Addr(), in.Target, in.Host, base.Host)

	wsURL := *base
	wsURL.Scheme = map[string]string{"https": "wss", "http": "ws"}[base.Scheme]
	wsURL.Path = strings.TrimSuffix(base.Path, "/") + "/ws/cli/forward"

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
			if err := carry(ctx, wsURL.String(), token, c); err != nil {
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

var errGone = errors.New("the forward has ended in webssh (stopped, or its session closed)")

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

// carry pipes one local connection through its own WebSocket.
func carry(ctx context.Context, wsURL, token string, local net.Conn) error {
	defer local.Close()
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, res, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	cancel()
	if err != nil {
		if res != nil && res.StatusCode == http.StatusUnauthorized {
			return errGone
		}
		return err
	}
	defer ws.CloseNow()
	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()
	remote := websocket.NetConn(cctx, ws, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
