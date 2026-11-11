package gateway

import (
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// agentKeyring builds an in-memory agent from the private keys used for this
// connection (target and jump hosts), for opt-in agent forwarding. Unlike
// other credentials it lives as long as the session: that is what agent
// forwarding means, and why it is off unless the host asks for it.
func agentKeyring(req *clientMsg) agent.Agent {
	if !req.Agent {
		return nil
	}
	kr := agent.NewKeyring()
	n := 0
	for _, h := range append(append([]hop{}, req.Jump...), *req.target()) {
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
