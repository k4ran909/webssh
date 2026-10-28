# webssh

A browser-based SSH client: a Go gateway bridges WebSocket ↔ SSH, and a React + xterm.js frontend renders the terminal. "webssh" is a placeholder name.

**Status: Phase 0 (spike).** One terminal session with password or private-key auth, trust-on-first-use host keys, and live resize. See [docs/PLAN.md](docs/PLAN.md) for the roadmap and [design/THEME.md](design/THEME.md) for the visual system.

> ⚠️ Phase 0 has **no login**. Anyone who can reach the gateway can use it to open SSH sessions. It only listens on `127.0.0.1` and refuses any other address unless you force it.

## Requirements

- Go 1.26+
- Node 20+

## Run

```bash
npm --prefix apps/web install
```

Build the frontend into the Go binary and run it:

```bash
npm run build
```

```bash
./bin/webssh
```

Open http://127.0.0.1:7022.

### Develop with hot reload

Run the gateway and the Vite dev server in two terminals. Vite proxies `/ws` to the gateway.

```bash
npm run dev:server
```

```bash
npm run dev:web
```

Open http://localhost:5173.

### Test

```bash
npm test
```

Runs `go vet`, the gateway integration tests (against an in-process SSH server) and the frontend typecheck.

## Configuration

| Variable | Default | |
|---|---|---|
| `WEBSSH_ADDR` | `127.0.0.1:7022` | Listen address. Non-loopback is refused while there's no login. |
| `WEBSSH_ALLOW_PRIVATE` | `true` on loopback | Allow targets on private / loopback / link-local IPs. Defaults to `false` on any other bind. |
| `WEBSSH_MAX_SESSIONS` | `20` | Concurrent SSH sessions. |
| `WEBSSH_ALLOWED_ORIGINS` | — | Extra WebSocket origins (`host:port`, comma-separated). Same-origin is always allowed. |
| `WEBSSH_ALLOW_PUBLIC_NO_AUTH` | — | Set to `1` to bind a non-loopback address anyway. Don't. |

## How it works

```
Browser (xterm.js) ──WebSocket──▶ Gateway (Go, x/crypto/ssh) ──SSH──▶ your server
```

- One WebSocket per session. Binary frames carry terminal bytes, JSON text frames carry control messages. The protocol is documented in [server/internal/gateway/protocol.go](server/internal/gateway/protocol.go).
- **Host keys:** the browser keeps trusted fingerprints and sends the one it expects. An unknown key is shown for confirmation. A changed key is blocked.
- **Credentials** are sent once per connection, used for the handshake, then dropped. They are never logged or stored. Only the last host, port and user, plus trusted fingerprints, are kept in `localStorage`, until the encrypted vault arrives in Phase 1.
- **Egress:** the gateway resolves the hostname once and dials that exact IP, so DNS rebinding can't slip in a private address. Private ranges are blocked unless allowed.
- The app is served with a strict CSP: no third-party scripts, styles or fonts, and fonts are self-hosted.

## Layout

```
apps/web/          React + Vite + xterm.js frontend
server/            Go gateway (single binary, embeds the built frontend)
  cmd/webssh/
  internal/gateway/   WebSocket ↔ SSH bridge, egress policy, tests
  internal/config/
  internal/web/       embedded static files + security headers
design/            logo, theme tokens, clickable prototype
docs/PLAN.md       architecture and roadmap
```
