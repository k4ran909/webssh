# webssh

A self-hosted, browser-based SSH client: a Go gateway bridges WebSocket ↔ SSH, and a React + xterm.js frontend renders the terminal. Hosts, saved credentials, snippets and trusted host keys live in an end-to-end encrypted vault. "webssh" is a placeholder name.

**Status: Phase 1 (accounts + encrypted vault) on top of the multi-session workspace.** See [docs/PLAN.md](docs/PLAN.md) for the roadmap and [design/THEME.md](design/THEME.md) for the visual system.

## Features

**Accounts and vault**
- **Zero-knowledge vault.** Your master password never leaves the browser. Hosts (with optional saved passwords or keys), snippets and trusted host keys are encrypted on your device with AES-256-GCM before being synced.
- **The first account owns the gateway.** Sign-up closes after the first account, unless you set `WEBSSH_OPEN_SIGNUP=true`.
- **Two-factor sign-in (TOTP)** with any authenticator app, with replay protection.
- **Recovery code.** Shown once at sign-up, it's the only way back in if you forget the master password. Using it issues a new one.
- **Auto-lock** after inactivity (default 15 minutes) and a manual lock. Open SSH sessions keep running behind the lock screen.
- **Activity log** of sign-ins, security changes and SSH connections (destination, duration, bytes). It never records terminal contents.
- **Change master password** re-wraps the vault key and signs out your other devices.

**Workspace**
- **Window manager.** Each session is a window you can drag, resize from any edge, minimize to the dock, maximize (or double-click the title), and close. Drag a window to the top edge to maximize it, or to the left/right edge to snap it to half the screen. Use Tile or Cascade to arrange everything.
- **Tabs layout.** Switch between floating windows and tabs without dropping any sessions. Phones always use tabs.
- **Command palette** (`Ctrl+Shift+K`, or `Ctrl+K` outside a terminal). Connect, switch sessions, run snippets, change layout, theme and font size, lock the vault.
- **Broadcast input.** Keystrokes from the focused terminal go to every connected session.
- **Snippets**, **find in terminal** (`Ctrl+Shift+F`), `Alt+1…9` to switch sessions, copy on select, adjustable font size, light/dark theme.
- **Host keys.** Trust on first use, synced through the vault. A changed key is blocked and shows the old and new fingerprints side by side.

## Requirements

- Go 1.26+
- Node 20+

## Run locally

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

Open http://127.0.0.1:7022 and create your account. Data goes to `./data` (SQLite + `server.key`); back up both.

### Develop with hot reload

Run the gateway and the Vite dev server in two terminals. Vite proxies `/api` and `/ws` to the gateway.

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

Runs `go vet`, the API tests (sign-up, login, TOTP, recovery, vault sync, CSRF, rate limits), the gateway integration tests (against an in-process SSH server) and the frontend typecheck.

## Deploy (Docker + Caddy)

Point a DNS record at your server, then:

```bash
WEBSSH_DOMAIN=ssh.example.com docker compose -f deploy/docker-compose.yml up -d --build
```

Caddy gets a TLS certificate automatically. Open `https://ssh.example.com` right away and create the owner account. Until then, anyone who finds the URL can claim it.

## Configuration

| Variable | Default | |
|---|---|---|
| `WEBSSH_ADDR` | `127.0.0.1:7022` | Listen address. Docker image: `0.0.0.0:7022`. |
| `WEBSSH_DATA_DIR` | `data` | SQLite database and `server.key` (encrypts TOTP seeds). |
| `WEBSSH_OPEN_SIGNUP` | `false` | Allow sign-ups after the first (owner) account. |
| `WEBSSH_TRUST_PROXY` | `false` | Trust `X-Forwarded-For`/`-Proto` from a reverse proxy (secure cookies, real client IPs). Image: `1`. |
| `WEBSSH_ALLOW_PRIVATE` | `true` on loopback | Allow SSH targets on private / loopback / link-local IPs. Defaults to `false` on any other bind. Compose sets `true` for homelab use. |
| `WEBSSH_MAX_SESSIONS` | `50` | Concurrent SSH sessions on the gateway. |
| `WEBSSH_MAX_USER_SESSIONS` | `20` | Concurrent SSH sessions per account. |
| `WEBSSH_ALLOWED_ORIGINS` | — | Extra origins (`host:port`, comma-separated) for the API and WebSocket. Same-origin is always allowed. |

## Security model

```
master password ──Argon2id (64 MiB)──▶ master key ──HKDF──┬─▶ auth key  → server stores sha256(salt‖key); used to sign in
                                                           └─▶ wrap key  → stays in the browser
vault key (random 256-bit) ── wrapped by wrap key ──▶ server
vault items ── AES-256-GCM(vault key, aad = item id) ──▶ server (opaque base64)
```

- **The server never sees** your master password, vault key, saved hosts, saved credentials or snippets. A copy of the database alone doesn't decrypt anything. An attacker would have to guess your password through 64 MiB of Argon2id per guess.
- **The gateway does see** SSH credentials and terminal traffic while a session is open, because it is the SSH client. Run your own gateway (that's the point of self-hosting). The destination host, port and username are recorded in your activity log.
- **Sign-in:** sessions use an `HttpOnly`, `SameSite=Strict` cookie (`Secure` behind TLS). State-changing requests from other origins are refused. Login, recovery and 2FA attempts are rate-limited per IP and per account. Unknown emails get a decoy salt, so the login form doesn't reveal which accounts exist.
- **Host keys:** trust on first use. The trusted fingerprint is synced in the vault, and a mismatch is a hard block.
- **Egress:** the gateway resolves the hostname once and dials that exact IP (no DNS rebinding). Private ranges are blocked unless allowed.
- **Browser:** strict CSP (`'wasm-unsafe-eval'` only so Argon2 can run as WebAssembly), self-hosted fonts, no third-party code. The in-memory vault key is a non-extractable WebCrypto key.

## Layout

```
apps/web/              React + Vite + xterm.js frontend
  src/lib/crypto.ts    Argon2id / HKDF / AES-GCM vault crypto
  src/lib/vault.ts     encrypted vault store + sync
server/                Go gateway (single binary, embeds the built frontend)
  internal/api/        accounts, sessions, TOTP, recovery, vault, audit (+ tests)
  internal/gateway/    WebSocket ↔ SSH bridge, egress policy (+ tests)
  internal/store/      SQLite (pure Go)
deploy/                Dockerfile, docker-compose (with Caddy), Caddyfile
design/                logo, theme tokens, clickable prototype
docs/PLAN.md           architecture and roadmap
```
