import { memo, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { SearchAddon, type ISearchOptions } from '@xterm/addon-search'
import { algoLabel, gatewayURL, type ClientMsg, type ServerMsg, type Target } from '../lib/protocol'
import { E2ESocket, type TermSocket } from '../lib/e2e'
import { forgetKnownHost, getKnownHost, saveKnownHost } from '../lib/vault'
import { applyModifiers, liveSessions, readySessions } from '../lib/live'
import { CastRecorder } from '../lib/cast'
import type { SessionMeta } from '../lib/workspace'
import { inkTheme } from '../lib/xtermTheme'
import { Icon } from './icons'

type Phase =
  | { kind: 'connecting' }
  | { kind: 'hostkey'; host: string; port: number; fingerprint: string; algo: string; via: boolean }
  | { kind: 'ready'; fingerprint: string; algo: string }
  | { kind: 'resuming'; attempt: number }
  | { kind: 'ended'; exitCode: number }
  | { kind: 'lost'; why?: string }
  | { kind: 'error'; code: string; msg: string; changed?: { host: string; port: number; fingerprint: string } }

const DIM = '\x1b[90m'
const RESET = '\x1b[0m'
/** Keep trying a little longer than the gateway's default 5-minute resume window. */
const RESUME_GIVE_UP_MS = 6 * 60_000

const SEARCH_OPTS: ISearchOptions = {
  incremental: true,
  decorations: {
    matchBackground: '#3a3320',
    activeMatchBackground: '#7a6327',
    matchOverviewRuler: '#e6c47a',
    activeMatchColorOverviewRuler: '#ffffff',
  },
}

/** Shortcuts the workspace owns; the terminal must not swallow them. */
export function isAppShortcut(e: KeyboardEvent) {
  const k = e.key.toLowerCase()
  if (e.ctrlKey && e.shiftKey && (k === 'k' || k === 'f')) return true
  return e.altKey && !e.ctrlKey && !e.metaKey && /^[1-9]$/.test(e.key)
}

interface Props {
  id: string
  title: string
  target: Target
  fontSize: number
  broadcast: boolean
  onMeta: (id: string, meta: Partial<SessionMeta>) => void
  onClose: (id: string) => void
  onReconnect: (id: string) => void
  onEdit: (id: string) => void
}

export const TerminalView = memo(function TerminalView({ id, title, target, fontSize, broadcast, onMeta, onClose, onReconnect, onEdit }: Props) {
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<() => void>(() => {})
  const searchRef = useRef<SearchAddon | null>(null)
  const sendRef = useRef<(m: ClientMsg) => void>(() => {})
  const broadcastRef = useRef(broadcast)
  const fontRef = useRef(fontSize)
  const metaRef = useRef(onMeta)
  const titleRef = useRef(title)
  broadcastRef.current = broadcast
  metaRef.current = onMeta
  titleRef.current = title

  const [phase, setPhaseState] = useState<Phase>({ kind: 'connecting' })
  const [search, setSearch] = useState<{ open: boolean; q: string; idx: number; count: number }>({ open: false, q: '', idx: -1, count: 0 })
  const searchInput = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const el = hostRef.current!
    let disposed = false
    let ready = false
    let finished = false
    let ws: TermSocket | null = null
    let changed: { host: string; port: number; fingerprint: string } | undefined
    // Resume state: token from "ready" and output bytes received since then.
    let resumeToken = ''
    let received = 0
    let lastReady: { fingerprint: string; algo: string } | null = null
    let retryTimer = 0
    let lostAt = 0
    let attempt = 0
    let recorder: CastRecorder | null = null
    const enc = new TextEncoder()

    const setPhase = (p: Phase, extra: Partial<SessionMeta> = {}) => {
      setPhaseState(p)
      metaRef.current(id, {
        phase: p.kind,
        ...(p.kind === 'ready' ? { fingerprint: p.fingerprint, algo: p.algo } : {}),
        ...extra,
      })
    }

    const term = new Terminal({
      fontFamily: '"Geist Mono", ui-monospace, "JetBrains Mono", Consolas, monospace',
      fontSize: fontRef.current,
      lineHeight: 1.25,
      cursorBlink: true,
      scrollback: 10000,
      allowProposedApi: true, // unicode11 + search decorations
      macOptionIsMeta: true,
      theme: inkTheme,
    })
    termRef.current = term
    const fit = new FitAddon()
    const searchAddon = new SearchAddon()
    searchRef.current = searchAddon
    term.loadAddon(fit)
    term.loadAddon(searchAddon)
    term.loadAddon(new WebLinksAddon())
    term.loadAddon(new Unicode11Addon())
    term.unicode.activeVersion = '11'

    // Hidden (minimized / background tab) terminals report 0×0; fitting then
    // would shrink the remote PTY to 2×1.
    const fitSafe = () => {
      if (!disposed && term.element && el.clientWidth > 0 && el.clientHeight > 0) fit.fit()
    }
    fitRef.current = fitSafe

    const send = (data: string | Uint8Array) => {
      if (ws?.readyState === WebSocket.OPEN) ws.send(data)
    }
    sendRef.current = (m) => send(JSON.stringify(m))
    const note = (s: string) => term.write(`${DIM}${s}${RESET}\r\n`)

    liveSessions.set(id, {
      isReady: () => ready,
      write: (data) => ready && send(data),
      focus: () => term.focus(),
      getSelection: () => term.getSelection(),
      openSearch: () => setSearch((s) => ({ ...s, open: true })),
      startRecording: () => {
        recorder = new CastRecorder(term.cols, term.rows, titleRef.current)
        metaRef.current(id, { recording: true })
      },
      stopRecording: () => {
        const r = recorder
        recorder = null
        metaRef.current(id, { recording: false })
        return r?.finish() ?? null
      },
    })

    const scheduleResume = () => {
      if (disposed || finished) return
      if (Date.now() - lostAt > RESUME_GIVE_UP_MS) {
        finished = true
        note('Could not reconnect to the gateway.')
        setPhase({ kind: 'lost' })
        return
      }
      attempt++
      setPhase({ kind: 'resuming', attempt })
      clearTimeout(retryTimer)
      retryTimer = window.setTimeout(() => {
        openSocket({ t: 'resume', token: resumeToken, offset: received, cols: term.cols, rows: term.rows })
      }, Math.min(10_000, 500 * 2 ** Math.min(attempt - 1, 5)))
    }

    // Retry right away when the network comes back.
    const onOnline = () => {
      if (lostAt && !ready && !finished) {
        clearTimeout(retryTimer)
        openSocket({ t: 'resume', token: resumeToken, offset: received, cols: term.cols, rows: term.rows })
      }
    }
    window.addEventListener('online', onOnline)

    const openSocket = (first: ClientMsg) => {
      if (disposed) return
      const sock: TermSocket = target.e2e ? new E2ESocket() : new WebSocket(gatewayURL())
      ws = sock
      sock.binaryType = 'arraybuffer'
      sock.onopen = () => sock.send(JSON.stringify(first))
      sock.onmessage = (e) => {
        if (ws !== sock) return
        if (typeof e.data !== 'string') {
          const bytes = new Uint8Array(e.data as ArrayBuffer)
          if (ready) {
            received += bytes.byteLength
            recorder?.output(bytes)
          }
          term.write(bytes)
          return
        }
        const m = JSON.parse(e.data) as ServerMsg
        switch (m.t) {
          case 'hostkey': {
            const isJump = !(m.host === target.host && m.port === target.port)
            if (m.status === 'new') setPhase({ kind: 'hostkey', host: m.host, port: m.port, fingerprint: m.fingerprint, algo: m.algo, via: isJump })
            else changed = { host: m.host, port: m.port, fingerprint: m.fingerprint }
            break
          }
          case 'ready':
            ready = true
            received = 0
            resumeToken = m.resume ?? ''
            lastReady = { fingerprint: m.fingerprint, algo: m.algo }
            note(`\x1b[32m✓${DIM} ${algoLabel(m.algo)} host key verified · authenticated as ${target.user}${target.e2e ? ' · end-to-end' : ''}${target.agent ? ' · agent forwarded' : ''}`)
            term.write('\r\n')
            // The PTY was sized at connect time; catch up with any resize since.
            sendRef.current({ t: 'resize', cols: term.cols, rows: term.rows })
            setPhase({ kind: 'ready', ...lastReady }, { sftp: m.sftp })
            term.focus()
            // Run-on-connect snippet, once the shell has had a moment to print its prompt.
            if (target.startup) setTimeout(() => ready && send(enc.encode(target.startup + '\r')), 400)
            break
          case 'resumed':
            ready = true
            lostAt = 0
            attempt = 0
            if (m.gap) note('\r\n(Some output was lost while disconnected.)')
            received = m.offset ?? 0
            setPhase({ kind: 'ready', ...lastReady! }, { sftp: m.sftp })
            break
          case 'exit':
            finished = true
            ready = false
            term.write('\r\n')
            // -1: the server dropped the channel without reporting a status
            note(m.exitCode < 0 ? 'Connection closed by the server.' : `Session ended (exit ${m.exitCode}).`)
            setPhase({ kind: 'ended', exitCode: m.exitCode }, { sftp: undefined })
            break
          case 'error':
            finished = true
            ready = false
            if (m.code === 'RESUME_GONE') {
              note('\r\nThe session ended while you were disconnected.')
              setPhase({ kind: 'lost', why: 'Session ended while disconnected' }, { sftp: undefined })
              break
            }
            term.write(`\x1b[31m✕ ${m.msg}${RESET}\r\n`)
            setPhase({ kind: 'error', code: m.code, msg: m.msg, changed })
            break
        }
      }
      sock.onclose = () => {
        if (ws !== sock || finished || disposed) return
        if (resumeToken) {
          // The SSH session is still alive on the gateway: reattach.
          if (ready) {
            ready = false
            lostAt = Date.now()
            note('\r\nConnection to the gateway dropped. Reconnecting…')
          }
          scheduleResume()
          return
        }
        note('Lost connection to the gateway.')
        setPhase({ kind: 'lost' })
      }
    }

    const start = async () => {
      // xterm measures glyphs on open, so the font must be loaded first.
      await document.fonts.load(`${fontRef.current}px "Geist Mono"`).catch(() => {})
      if (disposed) return
      term.open(el)
      try {
        const gl = new WebglAddon()
        gl.onContextLoss(() => gl.dispose())
        term.loadAddon(gl)
      } catch {
        // falls back to the DOM renderer
      }
      fitSafe()
      metaRef.current(id, { cols: term.cols, rows: term.rows })
      const via = target.jump?.map((j) => j.label || j.host).join(' → ')
      note(`Connecting to ${target.host}:${target.port}${via ? ` via ${via}` : ''}${target.e2e ? ' (end-to-end)' : ''} …`)
      openSocket({
        t: 'connect',
        host: target.host,
        port: target.port,
        user: target.user,
        auth: target.auth,
        knownHostKey: getKnownHost(target.host, target.port) ?? '',
        jump: target.jump?.map((j) => ({
          host: j.host, port: j.port, user: j.user, auth: j.auth, knownHostKey: getKnownHost(j.host, j.port) ?? '',
        })),
        agent: target.agent || undefined,
        cols: term.cols,
        rows: term.rows,
        term: 'xterm-256color',
      })
    }
    start()

    term.attachCustomKeyEventHandler((e) => !(e.type === 'keydown' && isAppShortcut(e)))

    const subs = [
      term.onData((d) => {
        const bytes = enc.encode(applyModifiers(d))
        if (broadcastRef.current) readySessions().forEach((s) => s.write(bytes))
        else if (ready) send(bytes)
      }),
      term.onBinary((d) => ready && send(Uint8Array.from(d, (c) => c.charCodeAt(0) & 0xff))),
      term.onResize(({ cols, rows }) => {
        metaRef.current(id, { cols, rows })
        recorder?.resize(cols, rows)
        if (ready) sendRef.current({ t: 'resize', cols, rows })
      }),
      searchAddon.onDidChangeResults(({ resultIndex, resultCount }) =>
        setSearch((s) => ({ ...s, idx: resultIndex, count: resultCount })),
      ),
    ]

    // Copy on mouse selection, like most terminal apps (Ctrl+Shift+C belongs to browser
    // dev tools). Not onSelectionChange: search selects matches and would clobber the clipboard.
    const copySelection = () => {
      const sel = term.getSelection()
      if (sel) navigator.clipboard?.writeText(sel).catch(() => {})
    }
    el.addEventListener('mouseup', copySelection)

    let raf = 0
    const ro = new ResizeObserver(() => {
      cancelAnimationFrame(raf)
      raf = requestAnimationFrame(fitSafe)
    })
    ro.observe(el)

    return () => {
      disposed = true
      clearTimeout(retryTimer)
      cancelAnimationFrame(raf)
      ro.disconnect()
      window.removeEventListener('online', onOnline)
      el.removeEventListener('mouseup', copySelection)
      subs.forEach((s) => s.dispose())
      liveSessions.delete(id)
      ws?.close()
      sendRef.current = () => {}
      termRef.current = null
      term.dispose()
    }
  }, [id, target])

  useEffect(() => {
    fontRef.current = fontSize
    const term = termRef.current
    if (term && term.options.fontSize !== fontSize) {
      term.options.fontSize = fontSize
      fitRef.current()
    }
  }, [fontSize])

  useEffect(() => {
    if (search.open) searchInput.current?.select()
  }, [search.open])

  const runSearch = (q: string, back = false) => {
    const s = searchRef.current
    if (!s) return
    if (!q) return s.clearDecorations()
    if (back) s.findPrevious(q, SEARCH_OPTS)
    else s.findNext(q, SEARCH_OPTS)
  }

  const closeSearch = () => {
    searchRef.current?.clearDecorations()
    setSearch({ open: false, q: '', idx: -1, count: 0 })
    termRef.current?.focus()
  }

  const trust = () => {
    if (phase.kind !== 'hostkey') return
    saveKnownHost(phase.host, phase.port, phase.fingerprint)
    sendRef.current({ t: 'hostkey-accept' })
    setPhaseState({ kind: 'connecting' })
  }

  const changedHop = phase.kind === 'error' ? phase.changed : undefined
  const forgetAndRetry = () => {
    forgetKnownHost(changedHop?.host ?? target.host, changedHop?.port ?? target.port)
    onReconnect(id)
  }

  return (
    <div className="term-root">
      <div className="xterm-wrap">
        <div className="xterm-host" ref={hostRef} />
      </div>

      {search.open && (
        <div className="term-search" role="search">
          <input
            ref={searchInput}
            value={search.q}
            placeholder="Find"
            spellCheck={false}
            onChange={(e) => {
              setSearch((s) => ({ ...s, q: e.target.value }))
              runSearch(e.target.value)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') runSearch(search.q, e.shiftKey)
              if (e.key === 'Escape') closeSearch()
            }}
          />
          <span className="count">{search.q ? (search.count ? `${search.idx + 1}/${search.count}` : '0/0') : ''}</span>
          <button title="Previous (Shift+Enter)" onClick={() => runSearch(search.q, true)}><Icon name="up" size={14} /></button>
          <button title="Next (Enter)" onClick={() => runSearch(search.q)}><Icon name="down" size={14} /></button>
          <button title="Close (Esc)" onClick={closeSearch}><Icon name="x" size={14} /></button>
        </div>
      )}

      {phase.kind === 'resuming' && (
        <div className="resume-pill" role="status">
          <span className="dot busy" /> Reconnecting{phase.attempt > 1 ? ` · attempt ${phase.attempt}` : ''}…
        </div>
      )}

      {phase.kind === 'hostkey' && (
        <div className="card" role="dialog" aria-label="New host key">
          <h4><Icon name="shield" /> New host key{phase.via ? ' · jump host' : ''}</h4>
          <p>First connection to <b>{phase.host}{phase.port === 22 ? '' : ':' + phase.port}</b>. Check that this fingerprint matches the server before trusting it.</p>
          <div className="fp"><small>{algoLabel(phase.algo)}</small>{phase.fingerprint}</div>
          <div className="acts">
            <button className="btn" onClick={() => sendRef.current({ t: 'hostkey-reject' })}>Cancel</button>
            <button className="btn primary" onClick={trust} autoFocus>Trust &amp; connect</button>
          </div>
        </div>
      )}

      {phase.kind === 'error' && phase.code === 'HOSTKEY_CHANGED' && (
        <div className="card danger" role="alertdialog" aria-label="Host key changed">
          <h4><Icon name="alert" /> Host key changed</h4>
          <p>{phase.msg} Only continue if you know why the key changed.</p>
          <div className="fp"><small>Trusted</small>{getKnownHost(changedHop?.host ?? target.host, changedHop?.port ?? target.port)}</div>
          {changedHop && <div className="fp now"><small>Server now presents</small>{changedHop.fingerprint}</div>}
          <div className="acts">
            <button className="btn" onClick={forgetAndRetry}>Forget old key &amp; retry</button>
            <button className="btn primary" onClick={() => onClose(id)} autoFocus>Close session</button>
          </div>
        </div>
      )}

      {phase.kind === 'error' && phase.code !== 'HOSTKEY_CHANGED' && (
        <div className="card" role="alertdialog" aria-label="Connection failed">
          <h4>Couldn't connect</h4>
          <p>{phase.msg}</p>
          <div className="acts">
            <button className="btn" onClick={() => onReconnect(id)}>Retry</button>
            <button className="btn primary" onClick={() => onEdit(id)} autoFocus>Edit connection</button>
          </div>
        </div>
      )}

      {(phase.kind === 'ended' || phase.kind === 'lost') && (
        <div className="endbar">
          <span>
            {phase.kind === 'lost' ? (phase.why ?? 'Connection lost')
              : phase.exitCode < 0 ? 'Closed by server'
              : `Session ended · exit ${phase.exitCode}`}
          </span>
          <button className="btn" onClick={() => onClose(id)}>Close</button>
          <button className="btn primary" onClick={() => onReconnect(id)}>Reconnect</button>
        </div>
      )}
    </div>
  )
})
