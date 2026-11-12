import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { isE2EHandle } from '../lib/e2e'
import { api } from '../lib/api'
import type { Session } from '../lib/workspace'
import { Icon } from './icons'

export interface ForwardInfo {
  port: number
  handle: string
  target: string
  auth?: string
}

/** A raw TCP forward, carried to a local port by the companion CLI. */
export interface TcpForwardInfo {
  id: string
  handle: string
  target: string
  conns: number
  token?: string
}

/** The command that runs the companion CLI for a forward. */
export const cliCommand = (token: string, windows: boolean) =>
  windows
    ? `$env:WEBSSH_TOKEN="${token}"; webssh-cli forward -gateway ${location.origin}`
    : `WEBSSH_TOKEN=${token} webssh-cli forward -gateway ${location.origin}`

// Each forward is its own origin (its own preview port) on the gateway's host.
export const previewURL = (port: number, path = '/') => `${location.protocol}//${location.hostname}:${port}${path}`

const message = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')

export function useForwards() {
  const [forwards, setForwards] = useState<ForwardInfo[]>([])
  const [tcp, setTcp] = useState<TcpForwardInfo[]>([])
  const [available, setAvailable] = useState(true)
  const refresh = useCallback(async () => {
    try {
      const [r, t] = await Promise.all([
        api<{ forwards: ForwardInfo[]; available: boolean }>('GET', '/api/forwards'),
        api<{ forwards: TcpForwardInfo[] }>('GET', '/api/tcp-forwards'),
      ])
      setForwards(r.forwards.sort((a, b) => a.port - b.port))
      setTcp(t.forwards.sort((a, b) => a.target.localeCompare(b.target)))
      setAvailable(r.available)
    } catch {
      // signed out or offline; the panel just shows nothing
    }
  }, [])
  useEffect(() => { refresh() }, [refresh])
  return { forwards, tcp, available, refresh }
}

export function ForwardDialog({ session, onClose, onCreated }: { session: Session; onClose: () => void; onCreated: () => void }) {
  const [host, setHost] = useState('127.0.0.1')
  const [port, setPort] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [made, setMade] = useState<ForwardInfo | null>(null)
  const [mode, setMode] = useState<'http' | 'tcp'>('http')
  const [madeTcp, setMadeTcp] = useState<TcpForwardInfo | null>(null)
  const [windows, setWindows] = useState(() => navigator.userAgent.includes('Windows'))
  const [copied, setCopied] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (isE2EHandle(session.meta.sftp)) return setError("This is an end-to-end session: the gateway can't see inside it, so it can't forward ports for it. Turn off end-to-end for this host to forward ports.")
    const p = Number(port)
    if (!Number.isInteger(p) || p < 1 || p > 65535) return setError('Port must be 1–65535.')
    setBusy(true)
    setError('')
    try {
      const req = { handle: session.meta.sftp, host: host.trim(), port: p }
      if (mode === 'tcp') setMadeTcp(await api<TcpForwardInfo>('POST', '/api/tcp-forwards', req))
      else setMade(await api<ForwardInfo>('POST', '/api/forwards', req))
      onCreated()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-label="Forward a port" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="acct-head">
          <div>
            <h2>Forward a port</h2>
            <p className="pop-sub">Reach a service running on <b>{session.title}</b> through this SSH connection.</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        {madeTcp ? (
          <div className="stack-form">
            <p className="ok-note" style={{ margin: 0 }}>Ready to forward {madeTcp.target}</p>
            <p className="pop-sub">Run this on your computer with the <b>webssh-cli</b> companion. It listens on 127.0.0.1:{madeTcp.target.split(':').pop()} (change it with <code>-listen</code>). The token is shown only now; it stops working when you stop the forward or the session ends.</p>
            <div className="seg" role="tablist" style={{ alignSelf: 'flex-start' }}>
              <button role="tab" aria-selected={!windows} className={!windows ? 'on' : ''} onClick={() => setWindows(false)}>macOS / Linux</button>
              <button role="tab" aria-selected={windows} className={windows ? 'on' : ''} onClick={() => setWindows(true)}>PowerShell</button>
            </div>
            <div className="invite-link">
              <code className="secret">{cliCommand(madeTcp.token!, windows)}</code>
              <button className="btn" onClick={() => navigator.clipboard?.writeText(cliCommand(madeTcp.token!, windows)).then(() => setCopied(true)).catch(() => {})}>
                <Icon name="copy" size={13} /> {copied ? 'Copied' : 'Copy'}
              </button>
            </div>
            <div className="form-actions"><button className="btn primary lg" onClick={onClose}>Done</button></div>
          </div>
        ) : made ? (
          <div className="stack-form">
            <p className="ok-note" style={{ margin: 0 }}>Forwarding {made.target} → preview port {made.port}</p>
            <a className="btn primary lg" href={previewURL(made.port, made.auth)} target="_blank" rel="noopener noreferrer" onClick={onClose}>
              <Icon name="external" size={14} /> Open preview
            </a>
            <p className="pop-sub">The link signs this browser in to the preview once. The forward stops when you stop it or the session ends.</p>
          </div>
        ) : (
          <form className="stack-form" onSubmit={submit}>
            <div className="seg" role="tablist" aria-label="Forward type">
              <button type="button" role="tab" aria-selected={mode === 'http'} className={mode === 'http' ? 'on' : ''} onClick={() => setMode('http')}>In the browser (HTTP)</button>
              <button type="button" role="tab" aria-selected={mode === 'tcp'} className={mode === 'tcp' ? 'on' : ''} onClick={() => setMode('tcp')}>Local port (CLI)</button>
            </div>
            <div className="row2">
              <label className="fld">
                <span>Host <em className="hint">(as seen from the server)</em></span>
                <input className="input mono" value={host} onChange={(e) => setHost(e.target.value)} spellCheck={false} />
              </label>
              <label className="fld">
                <span>Port</span>
                <input className="input mono" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ''))} placeholder="3000" inputMode="numeric" autoFocus />
              </label>
            </div>
            <p className="pop-sub" style={{ marginTop: -4 }}>
              {mode === 'http'
                ? 'Opens in a new tab. HTTP and WebSocket apps only (dashboards, dev servers).'
                : 'Any TCP service (databases, Redis, RDP) on a port of your own computer, through the webssh-cli companion.'}
            </p>
            {error && <p className="form-error">{error}</p>}
            <div className="form-actions">
              <button className="btn lg" type="button" onClick={onClose}>Cancel</button>
              <button className="btn primary lg" type="submit" disabled={busy || !port}>{busy ? 'Starting…' : 'Forward'}</button>
            </div>
          </form>
        )}
      </div>
    </div>
  )
}

export function ForwardsPanel({ forwards, tcp, available, sessions, onChange }: { forwards: ForwardInfo[]; tcp: TcpForwardInfo[]; available: boolean; sessions: Session[]; onChange: () => void }) {
  const [error, setError] = useState('')
  const name = (handle: string) => sessions.find((s) => s.meta.sftp === handle)?.title ?? 'session'
  const stop = async (path: string) => {
    try {
      await api('DELETE', path)
      onChange()
    } catch (e) {
      setError(message(e))
    }
  }
  return (
    <div>
      <div className="pop-head"><span>Port forwarding</span><span className="pop-sub">{forwards.length + tcp.length} active</span></div>
      {!available && <p className="pop-sub" style={{ padding: '0 8px 8px' }}>Browser previews are off on this gateway (set WEBSSH_FORWARD_PORTS).</p>}
      {forwards.length + tcp.length === 0 && <p className="pop-sub" style={{ padding: '0 8px 8px' }}>None yet. Use a window's ⋯ menu → Forward a port.</p>}
      {forwards.map((f) => (
        <div key={f.port} className="fwd-row">
          <div className="kc-main">
            <span className="kc-title mono">{f.target}</span>
            <span className="kc-sub">{name(f.handle)} · preview port {f.port}</span>
          </div>
          {/* Re-opening uses the cookie set by the first link; a new browser needs a new forward. */}
          <a className="icon-btn sm" href={previewURL(f.port)} target="_blank" rel="noopener noreferrer" title="Open preview" aria-label={`Open ${f.target}`}><Icon name="external" size={14} /></a>
          <button className="icon-btn sm" title="Stop forwarding" aria-label={`Stop ${f.target}`} onClick={() => stop(`/api/forwards/${f.port}`)}><Icon name="x" size={14} /></button>
        </div>
      ))}
      {tcp.map((f) => (
        <div key={f.id} className="fwd-row">
          <div className="kc-main">
            <span className="kc-title mono">{f.target}</span>
            <span className="kc-sub">{name(f.handle)} · local port via CLI · {f.conns} open</span>
          </div>
          <button className="icon-btn sm" title="Stop forwarding" aria-label={`Stop ${f.target}`} onClick={() => stop(`/api/tcp-forwards/${f.id}`)}><Icon name="x" size={14} /></button>
        </div>
      ))}
      {error && <p className="form-error" style={{ padding: '0 8px' }}>{error}</p>}
    </div>
  )
}
