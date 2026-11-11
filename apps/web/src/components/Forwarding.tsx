import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api } from '../lib/api'
import type { Session } from '../lib/workspace'
import { Icon } from './icons'

export interface ForwardInfo {
  port: number
  handle: string
  target: string
  auth?: string
}

// Each forward is its own origin (its own preview port) on the gateway's host.
export const previewURL = (port: number, path = '/') => `${location.protocol}//${location.hostname}:${port}${path}`

const message = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')

export function useForwards() {
  const [forwards, setForwards] = useState<ForwardInfo[]>([])
  const [available, setAvailable] = useState(true)
  const refresh = useCallback(async () => {
    try {
      const r = await api<{ forwards: ForwardInfo[]; available: boolean }>('GET', '/api/forwards')
      setForwards(r.forwards.sort((a, b) => a.port - b.port))
      setAvailable(r.available)
    } catch {
      // signed out or offline; the panel just shows nothing
    }
  }, [])
  useEffect(() => { refresh() }, [refresh])
  return { forwards, available, refresh }
}

export function ForwardDialog({ session, onClose, onCreated }: { session: Session; onClose: () => void; onCreated: () => void }) {
  const [host, setHost] = useState('127.0.0.1')
  const [port, setPort] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [made, setMade] = useState<ForwardInfo | null>(null)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const p = Number(port)
    if (!Number.isInteger(p) || p < 1 || p > 65535) return setError('Port must be 1–65535.')
    setBusy(true)
    setError('')
    try {
      const f = await api<ForwardInfo>('POST', '/api/forwards', { handle: session.meta.sftp, host: host.trim(), port: p })
      setMade(f)
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
            <p className="pop-sub">Open a web app running on <b>{session.title}</b> in your browser, through this SSH connection.</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        {made ? (
          <div className="stack-form">
            <p className="ok-note" style={{ margin: 0 }}>Forwarding {made.target} → preview port {made.port}</p>
            <a className="btn primary lg" href={previewURL(made.port, made.auth)} target="_blank" rel="noopener noreferrer" onClick={onClose}>
              <Icon name="external" size={14} /> Open preview
            </a>
            <p className="pop-sub">The link signs this browser in to the preview once. The forward stops when you stop it or the session ends.</p>
          </div>
        ) : (
          <form className="stack-form" onSubmit={submit}>
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
            <p className="pop-sub" style={{ marginTop: -4 }}>HTTP and WebSocket apps only (dashboards, dev servers). Raw TCP like databases needs a desktop SSH client.</p>
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

export function ForwardsPanel({ forwards, available, sessions, onChange }: { forwards: ForwardInfo[]; available: boolean; sessions: Session[]; onChange: () => void }) {
  const [error, setError] = useState('')
  const name = (handle: string) => sessions.find((s) => s.meta.sftp === handle)?.title ?? 'session'
  const stop = async (port: number) => {
    try {
      await api('DELETE', `/api/forwards/${port}`)
      onChange()
    } catch (e) {
      setError(message(e))
    }
  }
  return (
    <div>
      <div className="pop-head"><span>Port forwarding</span><span className="pop-sub">{forwards.length} active</span></div>
      {!available && <p className="pop-sub" style={{ padding: '0 8px 8px' }}>Off on this gateway (set WEBSSH_FORWARD_PORTS).</p>}
      {available && forwards.length === 0 && <p className="pop-sub" style={{ padding: '0 8px 8px' }}>None yet. Use a window's ⋯ menu → Forward a port.</p>}
      {forwards.map((f) => (
        <div key={f.port} className="fwd-row">
          <div className="kc-main">
            <span className="kc-title mono">{f.target}</span>
            <span className="kc-sub">{name(f.handle)} · preview port {f.port}</span>
          </div>
          {/* Re-opening uses the cookie set by the first link; a new browser needs a new forward. */}
          <a className="icon-btn sm" href={previewURL(f.port)} target="_blank" rel="noopener noreferrer" title="Open preview" aria-label={`Open ${f.target}`}><Icon name="external" size={14} /></a>
          <button className="icon-btn sm" title="Stop forwarding" aria-label={`Stop ${f.target}`} onClick={() => stop(f.port)}><Icon name="x" size={14} /></button>
        </div>
      ))}
      {error && <p className="form-error" style={{ padding: '0 8px' }}>{error}</p>}
    </div>
  )
}
