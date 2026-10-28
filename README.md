# webssh

A browser-based SSH client: a Go gateway bridges WebSocket ↔ SSH, and a React + xterm.js frontend renders the terminal. "webssh" is a placeholder name.

**Status: Phase 0 + multi-session workspace.** See [docs/PLAN.md](docs/PLAN.md) for the roadmap and [design/THEME.md](design/THEME.md) for the visual system.

## Features

- **Window manager.** Each session is a window you can drag, resize from any edge, minimize to the dock, maximize (or double-click the title), and close. Drag a window to the top edge to maximize it, or to the left/right edge to snap it to half the screen. Use Tile or Cascade to arrange everything.
- **Tabs layout.** Switch between floating windows and classic tabs without dropping any sessions. Phones always use tabs.
- **Saved hosts.** The sidebar keeps hosts with a label and color tag but no credentials, so you enter them when you connect. Within one page load, reopening a host or using Duplicate reuses the credentials you already typed.
- **Command palette** (`Ctrl+Shift+K`, or `Ctrl+K` outside a terminal). Connect, switch sessions, run snippets, change layout, theme and font size.
- **Broadcast input.** Keystrokes from the focused terminal go to every connected session. Windows get an amber outline while it's on.
- **Snippets.** Saved commands that run in the focused session, or in all of them while broadcasting.
- **Find in terminal** (`Ctrl+Shift+F`) with match count and highlights.
- **Host keys.** Trust on first use. A changed key is blocked and shows the old and new fingerprints side by side.
- **Other:** `Alt+1…9` switches sessions. Selecting text with the mouse copies it. You can change the terminal font size, and the light/dark theme can follow the system.

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
- **Credentials** are sent once per connection and used only for the handshake. The gateway never logs or stores them. The browser keeps them in memory (never in storage) until the page is reloaded, so Reconnect and Duplicate work. Only saved hosts (without credentials), snippets, preferences and trusted fingerprints go into `localStorage`, until the encrypted vault arrives in Phase 1.
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
