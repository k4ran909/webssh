# Theme — "Ink"

Minimal, monochrome, built from the mark: two black corner pieces facing each other.
Prototype: [prototype.html](prototype.html) · Logo: [logo.svg](logo.svg)

## Principles

1. **Black & white first.** UI chrome is pure greyscale. Color is used only for *meaning*: status (green / amber / red) and group tags.
2. **The terminal is the hero.** It's an ink slab (always dark by default, even in light mode) — the rest of the UI steps back.
3. **One signature motif.** The mark's two corner pieces frame the focused terminal pane (top-right + bottom-left). Don't use it anywhere else, so it stays special.
4. **Keyboard-first.** Ctrl/⌘+K command palette connects to any host in two keystrokes.
5. **Quiet motion.** 150–300 ms, one easing curve. The logo pieces snap together on vault unlock — the only "showy" animation.

## Logo

- Single path, second piece is the same path rotated 180° about the center (`viewBox 0 0 300 300`).
- `fill="currentColor"` — inherits text color, so it works on light and dark automatically.
- Min size 16 px. Clear space = one stem width × 4 around it.
- Wordmark: lowercase, Geist 600, `letter-spacing: -0.02em` (name is still a placeholder).

## Tokens

| Token | Light | Dark | Use |
|---|---|---|---|
| `--bg` | `#fafafa` | `#0a0a0a` | App background |
| `--surface` | `#ffffff` | `#101010` | Cards, panels, inputs |
| `--surface-2` | `#f3f3f3` | `#161616` | Badges, icon tiles |
| `--hover` | `rgba(0,0,0,.045)` | `rgba(255,255,255,.05)` | Hover/active fills |
| `--border` | `#e9e9e9` | `#1d1d1d` | Hairlines |
| `--border-strong` | `#d6d6d6` | `#2a2a2a` | Inputs, buttons |
| `--text` | `#0a0a0a` | `#ededed` | Primary text, logo |
| `--text-2` | `#5e5e5e` | `#a3a3a3` | Secondary |
| `--text-3` | `#9b9b9b` | `#636363` | Meta, placeholders |
| `--primary` / `--primary-fg` | `#0a0a0a` / `#fafafa` | `#ededed` / `#0a0a0a` | Primary button (inverts per theme) |
| `--ok` | `#16a34a` | `#4ade80` | Connected |
| `--warn` | `#c27803` | `#fbbf24` | Host-key prompts, staging |
| `--danger` | `#dc2626` | `#f87171` | Errors, production tag |

Radius: `10px` (cards, panes) · `6px` (buttons, inputs) · `999px` (chips)
Easing: `cubic-bezier(.2,.8,.2,1)`

## Type

- UI: **Geist** 400/500/600 — 14 px base, 13 px controls, 22 px page titles (`-0.025em`).
- Mono: **Geist Mono** — terminal 13 px / 1.6, addresses, fingerprints, status bar 11 px.
- Fallbacks: `system-ui` and `ui-monospace, "JetBrains Mono", Consolas`.

## Terminal themes (xterm.js `ITheme`)

| | Ink (default) | Paper | Dusk |
|---|---|---|---|
| background | `#0b0b0b` | `#fbfbf8` | `#14131a` |
| foreground | `#e6e6e6` | `#1a1a1a` | `#dcd8f0` |
| dim / brightBlack | `#6f6f6f` | `#8a8a85` | `#6d6889` |
| green | `#8fd694` | `#2f7d4a` | `#b7a6ff` |
| blue | `#8ab4f8` | — | — |
| yellow | `#e6c47a` | — | — |
| red | `#f28b82` | — | — |

Desaturated ANSI on purpose: color supports reading, it doesn't shout. Fill the remaining 16 ANSI slots in the same muted family when implementing.

## Layout

- Sidebar 236 px → off-canvas drawer under 760 px.
- Tab bar 46 px · status bar 28 px (mono, connection facts: user@host, key type, latency, size, encoding).
- SFTP opens as a 320 px side panel next to the terminal (full-screen sheet on mobile).
- New host key → centered card *inside* the terminal pane, fingerprint in amber mono, "Trust & connect" as primary.

## Moving to the real app

Put the token block in `apps/web/src/styles/tokens.css` and map it in `tailwind.config` (`colors: { bg: 'var(--bg)', surface: 'var(--surface)', ... }`). Keep shadcn/ui components but override its CSS variables with these tokens.
