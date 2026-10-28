import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { algoLabel, gatewayURL, type ClientMsg, type ServerMsg, type Target } from '../lib/protocol'
import { forgetKnownHost, getKnownHost, saveKnownHost } from '../lib/storage'
import { inkTheme } from '../lib/xtermTheme'
import { Icon } from './icons'

type Phase =
  | { kind: 'connecting' }
  | { kind: 'hostkey'; fingerprint: string; algo: string }
  | { kind: 'ready'; fingerprint: string; algo: string }
  | { kind: 'ended'; exitCode: number }
  | { kind: 'lost' }
  | { kind: 'error'; code: string; msg: string; fingerprint?: string }

const DIM = '\x1b[90m'
const RESET = '\x1b[0m'

interface Props {
  target: Target
  onBack: () => void
  onReconnect: () => void
}

export function TerminalView({ target, onBack, onReconnect }: Props) {
  const hostRef = useRef<HTMLDivElement>(null)
  const sendRef = useRef<(m: ClientMsg) => void>(() => {})
  const [phase, setPhase] = useState<Phase>({ kind: 'connecting' })
  const [size, setSize] = useState({ cols: 0, rows: 0 })
  const where = `${target.user}@${target.host}${target.port === 22 ? '' : ':' + target.port}`

  useEffect(() => {
    const el = hostRef.current!
    let disposed = false
    let ready = false
    let finished = false
    let ws: WebSocket | null = null
    let changedFingerprint: string | undefined
    const enc = new TextEncoder()

    const term = new Terminal({
      fontFamily: '"Geist Mono", ui-monospace, "JetBrains Mono", Consolas, monospace',
      fontSize: 13,
      lineHeight: 1.25,
      cursorBlink: true,
      scrollback: 10000,
      allowProposedApi: true, // unicode11
      macOptionIsMeta: true,
      theme: inkTheme,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.loadAddon(new WebLinksAddon())
    term.loadAddon(new Unicode11Addon())
    term.unicode.activeVersion = '11'

    const send = (data: string | Uint8Array) => {
      if (ws?.readyState === WebSocket.OPEN) ws.send(data)
    }
    sendRef.current = (m) => send(JSON.stringify(m))
    const note = (s: string) => term.write(`${DIM}${s}${RESET}\r\n`)

    const start = async () => {
      // xterm measures glyphs on open, so the font must be loaded first.
      await document.fonts.load('13px "Geist Mono"').catch(() => {})
      if (disposed) return
      term.open(el)
      try {
        const gl = new WebglAddon()
        gl.onContextLoss(() => gl.dispose())
        term.loadAddon(gl)
      } catch {
        // falls back to the DOM renderer
      }
      fit.fit()
      setSize({ cols: term.cols, rows: term.rows })
      note(`Connecting to ${target.host}:${target.port} …`)

      ws = new WebSocket(gatewayURL())
      ws.binaryType = 'arraybuffer'
      ws.onopen = () =>
        sendRef.current({
          t: 'connect',
          host: target.host,
          port: target.port,
          user: target.user,
          auth: target.auth,
          knownHostKey: getKnownHost(target.host, target.port) ?? '',
          cols: term.cols,
          rows: term.rows,
          term: 'xterm-256color',
        })
      ws.onmessage = (e) => {
        if (typeof e.data !== 'string') {
          term.write(new Uint8Array(e.data as ArrayBuffer))
          return
        }
        const m = JSON.parse(e.data) as ServerMsg
        switch (m.t) {
          case 'hostkey':
            if (m.status === 'new') setPhase({ kind: 'hostkey', fingerprint: m.fingerprint, algo: m.algo })
            else changedFingerprint = m.fingerprint
            break
          case 'ready':
            ready = true
            note(`\x1b[32m✓${DIM} ${algoLabel(m.algo)} host key verified · authenticated as ${target.user}`)
            term.write('\r\n')
            // The PTY was sized at connect time; catch up with any resize since.
            sendRef.current({ t: 'resize', cols: term.cols, rows: term.rows })
            setPhase({ kind: 'ready', fingerprint: m.fingerprint, algo: m.algo })
            term.focus()
            break
          case 'exit':
            finished = true
            ready = false
            term.write('\r\n')
            // -1: the server dropped the channel without reporting a status
            note(m.exitCode < 0 ? 'Connection closed by the server.' : `Session ended (exit ${m.exitCode}).`)
            setPhase({ kind: 'ended', exitCode: m.exitCode })
            break
          case 'error':
            finished = true
            ready = false
            term.write(`\x1b[31m✕ ${m.msg}${RESET}\r\n`)
            setPhase({ kind: 'error', code: m.code, msg: m.msg, fingerprint: changedFingerprint })
            break
        }
      }
      ws.onclose = () => {
        if (finished || disposed) return
        ready = false
        note('Lost connection to the gateway.')
        setPhase({ kind: 'lost' })
      }
    }
    start()

    const subs = [
      term.onData((d) => ready && send(enc.encode(d))),
      term.onBinary((d) => ready && send(Uint8Array.from(d, (c) => c.charCodeAt(0) & 0xff))),
      term.onResize(({ cols, rows }) => {
        setSize({ cols, rows })
        if (ready) sendRef.current({ t: 'resize', cols, rows })
      }),
      // Copy on select, like most terminal apps; Ctrl+Shift+C is taken by browser dev tools.
      term.onSelectionChange(() => {
        const sel = term.getSelection()
        if (sel) navigator.clipboard?.writeText(sel).catch(() => {})
      }),
    ]

    let raf = 0
    const ro = new ResizeObserver(() => {
      cancelAnimationFrame(raf)
      raf = requestAnimationFrame(() => {
        if (!disposed && term.element) fit.fit()
      })
    })
    ro.observe(el)

    return () => {
      disposed = true
      cancelAnimationFrame(raf)
      ro.disconnect()
      subs.forEach((s) => s.dispose())
      ws?.close()
      sendRef.current = () => {}
      term.dispose()
    }
  }, [target])

  const trust = () => {
    if (phase.kind !== 'hostkey') return
    saveKnownHost(target.host, target.port, phase.fingerprint)
    sendRef.current({ t: 'hostkey-accept' })
    setPhase({ kind: 'connecting' })
  }

  const forgetAndRetry = () => {
    forgetKnownHost(target.host, target.port)
    onReconnect()
  }

  const status =
    phase.kind === 'ready' ? { dot: 'ok', label: 'Connected' }
    : phase.kind === 'connecting' || phase.kind === 'hostkey' ? { dot: 'busy', label: 'Connecting' }
    : phase.kind === 'error' ? { dot: 'bad', label: 'Failed' }
    : { dot: '', label: 'Closed' }

  return (
    <div className="session">
      <div className="term-area">
        <div className="pane">
          <div className="pane-bar">
            <span className={`dot ${status.dot}`} />
            <span className="title">{where}</span>
            <span className="hint">select to copy · Ctrl+Shift+V to paste</span>
          </div>
          <div className="xterm-wrap">
            <div className="xterm-host" ref={hostRef} />
          </div>

          {phase.kind === 'hostkey' && (
            <div className="card" role="dialog" aria-label="New host key">
              <h4><Icon name="shield" /> New host key</h4>
              <p>First connection to <b>{target.host}</b>. Check that this fingerprint matches the server before trusting it.</p>
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
              <div className="fp"><small>Trusted</small>{getKnownHost(target.host, target.port)}</div>
              {phase.fingerprint && <div className="fp now"><small>Server now presents</small>{phase.fingerprint}</div>}
              <div className="acts">
                <button className="btn" onClick={forgetAndRetry}>Forget old key &amp; retry</button>
                <button className="btn primary" onClick={onBack} autoFocus>Back to safety</button>
              </div>
            </div>
          )}

          {phase.kind === 'error' && phase.code !== 'HOSTKEY_CHANGED' && (
            <div className="card" role="alertdialog" aria-label="Connection failed">
              <h4>Couldn't connect</h4>
              <p>{phase.msg}</p>
              <div className="acts">
                <button className="btn" onClick={onReconnect}>Retry</button>
                <button className="btn primary" onClick={onBack} autoFocus>Edit connection</button>
              </div>
            </div>
          )}

          {(phase.kind === 'ended' || phase.kind === 'lost') && (
            <div className="endbar">
              <span>
                {phase.kind === 'lost' ? 'Connection lost'
                  : phase.exitCode < 0 ? 'Closed by server'
                  : `Session ended · exit ${phase.exitCode}`}
              </span>
              <button className="btn" onClick={onBack}>Close</button>
              <button className="btn primary" onClick={onReconnect}>Reconnect</button>
            </div>
          )}
        </div>
      </div>

      <div className="statusbar">
        <span className="st"><span className={`dot ${status.dot}`} /><b>{status.label}</b></span>
        <span>{where}</span>
        {phase.kind === 'ready' && (
          <span className="hide-sm" title={phase.fingerprint}>
            {algoLabel(phase.algo)} {phase.fingerprint.slice(7, 19)}…
          </span>
        )}
        <span className="sp" />
        {size.cols > 0 && <span className="hide-sm">{size.cols}×{size.rows}</span>}
        <span>UTF-8</span>
      </div>
    </div>
  )
}
