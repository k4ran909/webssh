package sshcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	// HandshakeTimeout includes the user reading the host key prompt.
	HandshakeTimeout = 2 * time.Minute
	HostKeyTimeout   = 90 * time.Second
	KeepaliveEvery   = 30 * time.Second
)

// Dialer connects through a chain of hops. Only the first hop is dialed by
// First (where the gateway applies its egress policy, or the browser opens
// its relay); later hops are dialed from the previous hop's network, which
// is the point of a jump host.
type Dialer struct {
	First   func(ctx context.Context, h *Hop) (net.Conn, error)
	Timeout time.Duration // for each onward hop
	// Trust asks the user about a host key they haven't trusted yet; a nil
	// error accepts it.
	Trust func(h *Hop, fingerprint, algo string) error
	// Changed is told about a key that doesn't match the trusted one, just
	// before the connection fails with HOSTKEY_CHANGED.
	Changed func(h *Hop, fingerprint, algo string)
	Banner  func(msg string)
}

// Conn is an authenticated connection to the last hop.
type Conn struct {
	Client      *ssh.Client
	Close       func() // closes every hop, innermost first
	Fingerprint string // of the last hop
	Algo        string
}

func (d *Dialer) Dial(ctx context.Context, hops []Hop) (*Conn, error) {
	var chain []*ssh.Client
	closeAll := func() {
		for i := len(chain) - 1; i >= 0; i-- {
			chain[i].Close()
		}
	}
	out := &Conn{Close: closeAll}
	for i := range hops {
		h := &hops[i]
		final := i == len(hops)-1
		var conn net.Conn
		var err error
		if i == 0 {
			if conn, err = d.First(ctx, h); err != nil {
				return nil, err
			}
		} else {
			dctx, cancel := context.WithTimeout(ctx, d.Timeout)
			conn, err = chain[len(chain)-1].DialContext(dctx, "tcp", net.JoinHostPort(h.Host, strconv.Itoa(h.Port)))
			cancel()
			if err != nil {
				closeAll()
				return nil, &Error{CodeUnreachable, fmt.Sprintf("Jump host %s could not reach %s:%d: %v", hops[i-1].Host, h.Host, h.Port, err)}
			}
		}
		client, fp, algo, err := d.handshake(ctx, conn, h, final)
		if err != nil {
			closeAll()
			return nil, err
		}
		chain = append(chain, client)
		out.Client, out.Fingerprint, out.Algo = client, fp, algo
	}
	return out, nil
}

func (d *Dialer) handshake(ctx context.Context, conn net.Conn, h *Hop, final bool) (*ssh.Client, string, string, error) {
	auth, err := AuthMethods(h.Auth)
	if err != nil {
		conn.Close()
		return nil, "", "", err
	}
	// Channels through a bastion don't support deadlines, so a timer bounds
	// the handshake for both kinds of connection.
	timer := time.AfterFunc(HandshakeTimeout, func() { conn.Close() })
	defer timer.Stop()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	where := ""
	if !final {
		where = " on jump host " + h.Host
	}
	var keyErr *Error
	var fp, algo string
	cfg := &ssh.ClientConfig{
		User: h.User,
		Auth: auth,
		// Trust on first use. The browser owns the list of trusted keys and
		// sends the one it expects: an unknown key is shown to the user, a
		// mismatched one is a hard failure.
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp, algo = ssh.FingerprintSHA256(key), key.Type()
			if h.KnownHostKey != "" {
				if fp == h.KnownHostKey {
					return nil
				}
				if d.Changed != nil {
					d.Changed(h, fp, algo)
				}
				keyErr = &Error{CodeHostKeyChanged, "The host key of " + h.Host + " does not match the one you trusted. " +
					"The server may have been reinstalled, or someone may be intercepting the connection."}
				return keyErr
			}
			if err := d.Trust(h, fp, algo); err != nil {
				keyErr = AsError(err, CodeHostKeyRejected)
				return keyErr
			}
			return nil
		},
		BannerCallback: func(msg string) error {
			if d.Banner != nil {
				d.Banner(ToCRLF(msg))
			}
			return nil
		},
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(h.Host, strconv.Itoa(h.Port)), cfg)
	if err != nil {
		conn.Close()
		switch {
		case keyErr != nil:
			return nil, "", "", keyErr
		case ctx.Err() != nil:
			return nil, "", "", ctx.Err()
		case strings.Contains(err.Error(), "unable to authenticate"):
			what := "password"
			if h.Auth.Type == "key" {
				what = "key"
			}
			return nil, "", "", &Error{CodeAuthFailed, "Authentication failed" + where + ". Check the username and " + what + "."}
		}
		return nil, "", "", &Error{CodeHandshake, err.Error() + where}
	}
	return ssh.NewClient(c, chans, reqs), fp, algo, nil
}

func ParseSigner(a *Auth) (ssh.Signer, error) {
	var signer ssh.Signer
	var err error
	if a.Passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(a.Key), []byte(a.Passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(a.Key))
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return nil, &Error{CodeKeyPassphrase, "This private key is encrypted. Enter its passphrase."}
	}
	if err != nil {
		return nil, &Error{CodeKeyInvalid, "Could not read the private key (wrong passphrase or unsupported format)."}
	}
	return signer, nil
}

func AuthMethods(a *Auth) ([]ssh.AuthMethod, error) {
	switch a.Type {
	case "password":
		pw := a.Password
		return []ssh.AuthMethod{
			ssh.Password(pw),
			// Many servers only enable keyboard-interactive; answer hidden prompts with the password.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					if !echos[i] {
						answers[i] = pw
					}
				}
				return answers, nil
			}),
		}, nil
	case "key":
		signer, err := ParseSigner(a)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	return nil, &Error{CodeBadRequest, "unknown auth type"}
}

// AgentKeyring builds an in-memory agent from the private keys used for a
// connection (target and jump hosts), for opt-in agent forwarding. Unlike
// other credentials it lives as long as the session: that is what agent
// forwarding means, and why it is off unless the host asks for it.
func AgentKeyring(hops []Hop) agent.Agent {
	kr := agent.NewKeyring()
	n := 0
	for _, h := range hops {
		if h.Auth == nil || h.Auth.Type != "key" {
			continue
		}
		var raw any
		var err error
		if h.Auth.Passphrase != "" {
			raw, err = ssh.ParseRawPrivateKeyWithPassphrase([]byte(h.Auth.Key), []byte(h.Auth.Passphrase))
		} else {
			raw, err = ssh.ParseRawPrivateKey([]byte(h.Auth.Key))
		}
		if err == nil && kr.Add(agent.AddedKey{PrivateKey: raw, Comment: h.User + "@" + h.Host}) == nil {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return kr
}

// Shell is an interactive shell on a PTY.
type Shell struct {
	Sess   *ssh.Session
	Stdin  io.Writer
	Stdout io.Reader
	Stderr io.Reader
}

// OpenShell requests a PTY and a shell. keyring may be nil; refused agent
// forwarding is reported to agentRefused and the shell opens anyway.
func OpenShell(client *ssh.Client, keyring agent.Agent, term string, rows, cols int, agentRefused func(error)) (*Shell, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, &Error{CodeSession, "Could not open a session: " + err.Error()}
	}
	if keyring != nil {
		if err := agent.ForwardToAgent(client, keyring); err == nil {
			if err := agent.RequestAgentForwarding(sess); err != nil && agentRefused != nil {
				agentRefused(err)
			}
		}
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty(term, rows, cols, modes); err != nil {
		sess.Close()
		return nil, &Error{CodeSession, "The server refused a terminal: " + err.Error()}
	}
	stdin, err1 := sess.StdinPipe()
	stdout, err2 := sess.StdoutPipe()
	stderr, err3 := sess.StderrPipe()
	if err := errors.Join(err1, err2, err3); err != nil {
		sess.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		sess.Close()
		return nil, &Error{CodeSession, "Could not start a shell: " + err.Error()}
	}
	return &Shell{Sess: sess, Stdin: stdin, Stdout: stdout, Stderr: stderr}, nil
}

// Keepalive pings the server and closes the client after 3 missed replies.
func Keepalive(ctx context.Context, client *ssh.Client) {
	t := time.NewTicker(KeepaliveEvery)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				if misses++; misses >= 3 {
					client.Close()
					return
				}
			} else {
				misses = 0
			}
		}
	}
}

func ExitCode(err error) int {
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		return exitErr.ExitStatus()
	default:
		return -1
	}
}
