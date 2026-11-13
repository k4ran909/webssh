import { useCallback, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { api } from '../lib/api'
import { isE2EHandle } from '../lib/e2e'
import type { Session } from '../lib/workspace'
import { inkTheme } from '../lib/xtermTheme'
import { Icon } from './icons'

interface ShareInfo {
  id: string
  handle: string
  mode: 'view' | 'control'
  watchers: { email: string; since: string }[]
  token?: string
}

const message = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')
const shareLink = (token: string) => `${location.origin}/?watch=${encodeURIComponent(token)}`

/** The owner's side: create share links for a session and see who's watching. */
export function ShareDialog({ session, onClose }: { session: Session; onClose: () => void }) {
  const handle = session.meta.sftp ?? ''
  const [shares, setShares] = useState<ShareInfo[]>([])
  const [mode, setMode] = useState<'view' | 'control'>('view')
  const [made, setMade] = useState<{ id: string; link: string } | null>(null)
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')
  const e2e = isE2EHandle(handle)

  const load = useCallback(async () => {
    try {
      const r = await api<{ shares: ShareInfo[] }>('GET', '/api/shares')
      setShares(r.shares.filter((s) => s.handle === handle))
    } catch {
      // offline; keep what we have
    }
  }, [handle])
  useEffect(() => {
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [load])

  const create = async () => {
    setError('')
    try {
      const s = await api<ShareInfo>('POST', '/api/shares', { handle, mode })
      setMade({ id: s.id, link: shareLink(s.token!) })
      setCopied(false)
      load()
    } catch (e) {
      setError(message(e))
    }
  }

  const stop = async (id: string) => {
    try {
      await api('DELETE', `/api/shares/${id}`)
      if (made?.id === id) setMade(null)
      load()
    } catch (e) {
      setError(message(e))
    }
  }

  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal wide-modal" role="dialog" aria-label="Share session" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="acct-head">
          <div>
            <h2>Share session</h2>
            <p className="pop-sub">Let someone watch <b>{session.title}</b> live, or work in it with you. They need an account on this gateway.</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div className="acct-body">
          {e2e ? (
            <p className="warn-note"><Icon name="alert" size={13} /> This is an end-to-end session: the gateway can't see it, so it can't share it.</p>
          ) : (
            <section className="acct-sec">
              <div className="seg" role="tablist" aria-label="Access">
                <button role="tab" aria-selected={mode === 'view'} className={mode === 'view' ? 'on' : ''} onClick={() => setMode('view')}>Watch only</button>
                <button role="tab" aria-selected={mode === 'control'} className={mode === 'control' ? 'on' : ''} onClick={() => setMode('control')}>Watch and type</button>
              </div>
              {mode === 'control' && <p className="warn-note" style={{ marginTop: 10 }}><Icon name="alert" size={13} /> Anyone with this link can run commands as <b>{session.target.user}</b> on this server while you're connected.</p>}
              <div className="acts"><button className="btn primary" onClick={create}><Icon name="link" size={13} /> Create link</button></div>
              {made && (
                <div className="invite-link">
                  <code className="secret">{made.link}</code>
                  <button className="btn" onClick={() => navigator.clipboard?.writeText(made.link).then(() => setCopied(true)).catch(() => {})}>
                    <Icon name="copy" size={13} /> {copied ? 'Copied' : 'Copy'}
                  </button>
                </div>
              )}
              {made && <p className="pop-sub" style={{ marginTop: 6 }}>The link is shown only now. It stops working when you stop sharing or the session ends.</p>}
            </section>
          )}
          {shares.length > 0 && (
            <section className="acct-sec">
              <h3>Active links</h3>
              <div className="member-list">
                {shares.map((s) => (
                  <div key={s.id} className="member-row">
                    <div className="kc-main">
                      <span className="kc-title">{s.mode === 'control' ? 'Watch and type' : 'Watch only'}</span>
                      <span className="kc-sub">{s.watchers.length ? s.watchers.map((w) => w.email || 'someone').join(', ') : 'Nobody has joined yet'}</span>
                    </div>
                    <button className="btn sm-btn" onClick={() => stop(s.id)}>Stop sharing</button>
                  </div>
                ))}
              </div>
            </section>
          )}
          {error && <p className="form-error">{error}</p>}
        </div>
      </div>
    </div>
  )
}

/** Joining someone's session: by share link, or as the gateway admin by handle. */
export type WatchSource = { token: string } | { handle: string; control: boolean }

/** The watcher's side: a read-only (or shared-control) view of someone's session. */
export function WatchView({ source, onClose }: { source: WatchSource; onClose: () => void }) {
  const admin = 'handle' in source
  // Reconnect only when the target changes, not on every parent render.
  const key = 'handle' in source ? `admin:${source.handle}:${source.control}` : `link:${source.token}`
  const hostRef = useRef<HTMLDivElement>(null)
  const [info, setInfo] = useState<{ mode: string; owner: string; host: string } | null>(null)
  const [status, setStatus] = useState('Joining…')

  useEffect(() => {
    const term = new Terminal({
      fontFamily: '"Geist Mono", ui-monospace, "JetBrains Mono", Consolas, monospace',
      fontSize: 13,
      lineHeight: 1.25,
      scrollback: 5000,
      theme: inkTheme,
      disableStdin: true,
    })
    term.open(hostRef.current!)
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}/ws/watch`)
    ws.binaryType = 'arraybuffer'
    let control = false
    ws.onopen = () => ws.send(JSON.stringify('handle' in source
      ? { t: 'admin-watch', handle: source.handle, control: source.control }
      : { t: 'watch', token: source.token }))
    ws.onmessage = (e) => {
      if (typeof e.data !== 'string') return term.write(new Uint8Array(e.data as ArrayBuffer))
      const m = JSON.parse(e.data)
      if (m.t === 'watching') {
        control = m.mode === 'control'
        term.options.disableStdin = !control
        if (m.cols && m.rows) term.resize(m.cols, m.rows)
        setInfo({ mode: m.mode, owner: m.owner, host: m.host })
        setStatus('')
        if (control) term.focus()
      } else if (m.t === 'size') {
        term.resize(m.cols, m.rows)
      } else if (m.t === 'exit') {
        setStatus('The session has ended.')
      } else if (m.t === 'error') {
        setStatus(m.msg)
      }
    }
    ws.onclose = () => setStatus((s) => s || 'Disconnected.')
    const enc = new TextEncoder()
    const sub = term.onData((d) => {
      if (control && ws.readyState === WebSocket.OPEN) ws.send(enc.encode(d))
    })
    return () => {
      sub.dispose()
      ws.close()
      term.dispose()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  return (
    <div className="scrim modal-scrim watch-scrim">
      <div className="watch-view" role="dialog" aria-label="Shared session">
        <div className="watch-head">
          <Icon name="eye" size={14} />
          <span className="watch-title">{info ? <>{info.host} <em className="hint">{admin ? 'session of' : 'shared by'} {info.owner || 'someone'}</em></> : admin ? 'Session' : 'Shared session'}</span>
          {info && <span className={`tag${info.mode === 'control' ? ' warn' : ''}`}>{info.mode === 'control' ? (admin ? 'in control' : 'you can type') : 'watch only'}</span>}
          <button className="icon-btn" aria-label="Leave" title="Leave" onClick={onClose}><Icon name="x" /></button>
        </div>
        {status && <p className="watch-status">{status}</p>}
        <div className="watch-term" ref={hostRef} />
      </div>
    </div>
  )
}
