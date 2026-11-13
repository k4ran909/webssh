import { lazy, Suspense, useCallback, useEffect, useState, type FormEvent } from 'react'
import { parseCast, type Cast } from '../lib/cast'
import { WatchView, type WatchSource } from './Sharing'
import { api, type AuditEvent } from '../lib/api'
import { Icon } from './icons'

interface UserRow {
  id: string
  email: string
  totp: boolean
  isAdmin: boolean
  createdAt: string
  lastLogin: string
}

interface Invite {
  email: string
  teamId?: string
  role?: string
  expiresAt: string
}

const message = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')
const when = (s: string) => (s && !s.startsWith('0001') && new Date(s).getTime() > 0 ? new Date(s).toLocaleString() : 'never')

export function AdminDialog({ myUserId, myTotp, onClose }: { myUserId: string; myTotp: boolean; onClose: () => void }) {
  const [tab, setTab] = useState<'users' | 'sessions' | 'recordings' | 'policy' | 'audit'>('users')
  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal wide-modal" role="dialog" aria-label="Gateway admin" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="acct-head">
          <div>
            <h2>Gateway admin</h2>
            <p className="pop-sub">Accounts and policy for this gateway. Nobody, admins included, can read another person's vault.</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div className="seg acct-tabs" role="tablist">
          {(['users', 'sessions', 'recordings', 'policy', 'audit'] as const).map((t) => (
            <button key={t} role="tab" aria-selected={tab === t} className={tab === t ? 'on' : ''} onClick={() => setTab(t)}>{t[0].toUpperCase() + t.slice(1)}</button>
          ))}
        </div>
        <div className="acct-body">
          {tab === 'users' && <Users myUserId={myUserId} />}
          {tab === 'sessions' && <Sessions myTotp={myTotp} />}
          {tab === 'recordings' && <Recordings />}
          {tab === 'policy' && <Policy myTotp={myTotp} />}
          {tab === 'audit' && <Audit />}
        </div>
      </div>
    </div>
  )
}

function Users({ myUserId }: { myUserId: string }) {
  const [users, setUsers] = useState<UserRow[]>([])
  const [invites, setInvites] = useState<Invite[]>([])
  const [email, setEmail] = useState('')
  const [link, setLink] = useState('')
  const [confirm, setConfirm] = useState<string | null>(null)
  const [error, setError] = useState('')
  const load = useCallback(async () => {
    try {
      const [u, i] = await Promise.all([api<{ users: UserRow[] }>('GET', '/api/admin/users'), api<{ invites: Invite[] }>('GET', '/api/admin/invites')])
      setUsers(u.users)
      setInvites(i.invites)
    } catch (e) {
      setError(message(e))
    }
  }, [])
  useEffect(() => { load() }, [load])

  const invite = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      const r = await api<{ link: string }>('POST', '/api/admin/invites', { email: email.trim() })
      setLink(location.origin + r.link)
      setEmail('')
      load()
    } catch (err) {
      setError(message(err))
    }
  }

  return (
    <>
      <section className="acct-sec">
        <h3>Accounts · {users.length}</h3>
        <div className="member-list">
          {users.map((u) => (
            <div key={u.id} className="member-row">
              <div className="kc-main">
                <span className="kc-title">{u.email}{u.id === myUserId && <em className="hint"> (you)</em>}</span>
                <span className="kc-sub">Last sign-in {when(u.lastLogin)} · joined {new Date(u.createdAt).toLocaleDateString()}</span>
              </div>
              {u.isAdmin && <span className="tag">admin</span>}
              <span className={`tag${u.totp ? '' : ' warn'}`}>{u.totp ? '2FA' : 'no 2FA'}</span>
              {u.id !== myUserId && (confirm === u.id ? (
                <>
                  <button className="btn danger sm-btn" onClick={async () => {
                    setError('')
                    try { await api('DELETE', `/api/admin/users/${u.id}`); setConfirm(null); load() } catch (e) { setError(message(e)) }
                  }}>Delete account</button>
                  <button className="btn sm-btn" onClick={() => setConfirm(null)}>Keep</button>
                </>
              ) : (
                <button className="icon-btn sm" title="Delete account" aria-label={`Delete ${u.email}`} onClick={() => setConfirm(u.id)}><Icon name="trash" size={13} /></button>
              ))}
            </div>
          ))}
        </div>
        {confirm && <p className="warn-note"><Icon name="alert" size={13} /> Deleting an account erases its vault for good. Nobody can recover it.</p>}
      </section>
      <section className="acct-sec">
        <h3>Invite someone</h3>
        <p className="pop-sub">Sign-up is closed to everyone else; an invitation opens it for one email address for 7 days.</p>
        <form className="invite-form" onSubmit={invite}>
          <input className="input" type="email" placeholder="Email address" aria-label="Email to invite" value={email} onChange={(e) => setEmail(e.target.value)} />
          <button className="btn" type="submit" disabled={!email.trim()}>Create invitation</button>
        </form>
        {link && <div className="invite-link"><code className="secret">{link}</code><button className="btn" onClick={() => navigator.clipboard?.writeText(link)}><Icon name="copy" size={13} /> Copy</button></div>}
        {invites.length > 0 && (
          <div className="member-list" style={{ marginTop: 10 }}>
            {invites.map((i) => (
              <div key={i.email + (i.teamId ?? '')} className="member-row invited">
                <div className="kc-main">
                  <span className="kc-title">{i.email}</span>
                  <span className="kc-sub">{i.teamId ? `Team invite (${i.role})` : 'Gateway invite'} · expires {new Date(i.expiresAt).toLocaleDateString()}</span>
                </div>
                <button className="icon-btn sm" title="Revoke" aria-label={`Revoke invite for ${i.email}`}
                  onClick={async () => { await api('DELETE', '/api/admin/invites', { email: i.email, teamId: i.teamId ?? '' }); load() }}><Icon name="x" size={13} /></button>
              </div>
            ))}
          </div>
        )}
        {error && <p className="form-error">{error}</p>}
      </section>
    </>
  )
}

interface PolicySettings {
  require2fa: boolean
  openSignup: boolean
  recordSessions: boolean
  recordingDays: number
}

function Policy({ myTotp }: { myTotp: boolean }) {
  const [s, setS] = useState<PolicySettings | null>(null)
  const [error, setError] = useState('')
  useEffect(() => { api<PolicySettings>('GET', '/api/admin/settings').then(setS, (e) => setError(message(e))) }, [])
  if (!s) return <p className="pop-sub">{error || 'Loading…'}</p>
  return (
    <section className="acct-sec">
      <h3>Sign-in</h3>
      <label className="check">
        <input type="checkbox" checked={s.require2fa} disabled={!myTotp && !s.require2fa} onChange={async (e) => {
          setError('')
          try { setS(await api('PUT', '/api/admin/settings', { require2fa: e.target.checked })) } catch (err) { setError(message(err)) }
        }} />
        <span>Require two-factor authentication for every account</span>
      </label>
      <p className="pop-sub">Accounts without 2FA can still sign in, but can only set it up: no vault, no SSH until they do.{!myTotp && ' Turn on 2FA for your own account first.'}</p>
      <p className="pop-sub" style={{ marginTop: 12 }}>Open sign-up: <b>{s.openSignup ? 'on' : 'off'}</b> (set with WEBSSH_OPEN_SIGNUP).</p>
      <h3 style={{ marginTop: 18 }}>Recording</h3>
      <label className="check">
        <input type="checkbox" checked={s.recordSessions} onChange={async (e) => {
          setError('')
          try { setS(await api('PUT', '/api/admin/settings', { recordSessions: e.target.checked })) } catch (err) { setError(message(err)) }
        }} />
        <span>Record every session's output on the gateway</span>
      </label>
      <p className="pop-sub">Saved on the gateway's disk for {s.recordingDays > 0 ? `${s.recordingDays} days` : 'ever'} (WEBSSH_RECORDING_DAYS), viewable under Recordings. Keystrokes aren't recorded; end-to-end sessions can't be.</p>
      {error && <p className="form-error">{error}</p>}
    </section>
  )
}

function Audit() {
  const [events, setEvents] = useState<AuditEvent[] | null>(null)
  const [error, setError] = useState('')
  useEffect(() => { api<{ events: AuditEvent[] }>('GET', '/api/admin/audit').then((r) => setEvents(r.events), (e) => setError(message(e))) }, [])
  if (error) return <p className="form-error">{error}</p>
  if (!events) return <p className="pop-sub">Loading…</p>
  return (
    <div className="activity">
      <div className="audit-head">
        <p className="pop-sub">Every account's sign-ins, team changes, SSH and file activity. Terminal contents are never recorded.</p>
        <a className="btn" href="/api/admin/audit?format=csv" download><Icon name="download" size={13} /> Export CSV</a>
      </div>
      {events.map((e) => (
        <div key={e.id} className={`act-row${e.action.endsWith('.failed') ? ' bad' : ''}`}>
          <span className="act-what">{e.action}</span>
          <span className="act-detail mono">{e.email}</span>
          <span className="act-meta mono">{e.ip} · {new Date(e.createdAt).toLocaleString()} · {e.detail}</span>
        </div>
      ))}
    </div>
  )
}

interface LiveSession {
  handle: string
  email: string
  user: string
  host: string
  port: number
  started: string
  attached: boolean
}

const since = (iso: string) => {
  const m = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 60000))
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${m % 60} min`
}

/** Every live session on the gateway, to watch or take over. */
function Sessions({ myTotp }: { myTotp: boolean }) {
  const [sessions, setSessions] = useState<LiveSession[] | null>(null)
  const [error, setError] = useState('')
  const [watching, setWatching] = useState<WatchSource | null>(null)
  const load = useCallback(async () => {
    try {
      setSessions((await api<{ sessions: LiveSession[] }>('GET', '/api/admin/sessions')).sessions)
      setError('')
    } catch (e) {
      setError(message(e))
    }
  }, [])
  useEffect(() => {
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [load])

  return (
    <section className="acct-sec">
      <h3>Live sessions{sessions ? ` · ${sessions.length}` : ''}</h3>
      <p className="pop-sub">Every SSH session running through this gateway. Watch one, or take control and type into it. Joining is recorded in the audit log. End-to-end and Telnet sessions aren't listed: the gateway can't see inside them.</p>
      {!myTotp && <p className="warn-note"><Icon name="alert" size={13} /> Turn on two-factor sign-in for your account (Account &amp; security) to use this.</p>}
      {error && <p className="form-error">{error}</p>}
      {sessions && sessions.length === 0 && !error && <p className="pop-sub">Nobody is connected right now.</p>}
      <div className="member-list">
        {sessions?.map((s) => (
          <div key={s.handle} className="member-row">
            <div className="kc-main">
              <span className="kc-title mono">{s.user}@{s.host}{s.port !== 22 ? `:${s.port}` : ''}</span>
              <span className="kc-sub">{s.email || 'unknown account'} · {since(s.started)}{s.attached ? '' : ' · browser disconnected'}</span>
            </div>
            <button className="btn sm-btn" onClick={() => setWatching({ handle: s.handle, control: false })}><Icon name="eye" size={13} /> Watch</button>
            <button className="btn sm-btn" onClick={() => setWatching({ handle: s.handle, control: true })}>Take control</button>
          </div>
        ))}
      </div>
      {watching && <WatchView source={watching} onClose={() => setWatching(null)} />}
    </section>
  )
}

const CastPlayer = lazy(() => import('./CastPlayer'))

interface RecordingRow {
  id: string
  userId: string
  email: string
  host: string
  login: string
  port: number
  proto: string
  started: string
  ended: string
  bytes: number
}

const live = (r: RecordingRow) => !r.ended || r.ended.startsWith('0001')
const duration = (r: RecordingRow) => {
  const s = Math.max(0, Math.round(((live(r) ? Date.now() : new Date(r.ended).getTime()) - new Date(r.started).getTime()) / 1000))
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}
const size = (n: number) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KB` : `${(n / (1 << 20)).toFixed(1)} MB`)

/** Server-side recordings of every session, to replay or delete. */
function Recordings() {
  const [rows, setRows] = useState<RecordingRow[] | null>(null)
  const [meta, setMeta] = useState<{ enabled: boolean; days: number } | null>(null)
  const [users, setUsers] = useState<{ id: string; email: string }[]>([])
  const [who, setWho] = useState('')
  const [error, setError] = useState('')
  const [confirm, setConfirm] = useState<string | null>(null)
  const [playing, setPlaying] = useState<{ cast: Cast; name: string } | null>(null)

  const load = useCallback(async () => {
    try {
      const r = await api<{ recordings: RecordingRow[]; enabled: boolean; days: number }>('GET', `/api/admin/recordings${who ? `?user=${encodeURIComponent(who)}` : ''}`)
      setRows(r.recordings)
      setMeta({ enabled: r.enabled, days: r.days })
      setError('')
    } catch (e) {
      setError(message(e))
    }
  }, [who])
  useEffect(() => { load() }, [load])
  useEffect(() => { api<{ users: { id: string; email: string }[] }>('GET', '/api/admin/users').then((r) => setUsers(r.users), () => {}) }, [])

  const play = async (r: RecordingRow) => {
    setError('')
    try {
      const res = await fetch(`/api/admin/recordings/${r.id}/cast`, { credentials: 'same-origin' })
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).msg ?? res.statusText)
      const name = `${r.email || 'session'} ${r.login ? r.login + '@' : ''}${r.host} ${new Date(r.started).toLocaleString()}`
      setPlaying({ cast: parseCast(await res.text()), name })
    } catch (e) {
      setError(message(e))
    }
  }

  const remove = async (id: string) => {
    setError('')
    try {
      await api('DELETE', `/api/admin/recordings/${id}`)
      setConfirm(null)
      load()
    } catch (e) {
      setError(message(e))
    }
  }

  return (
    <section className="acct-sec">
      <div className="audit-head">
        <h3>Recordings{rows ? ` · ${rows.length}` : ''}</h3>
        <select className="input select" value={who} aria-label="Filter by account" onChange={(e) => setWho(e.target.value)}>
          <option value="">Everyone</option>
          {users.map((u) => <option key={u.id} value={u.id}>{u.email}</option>)}
        </select>
      </div>
      <p className="pop-sub">
        {meta && !meta.enabled ? 'Recording is off (Policy tab). ' : ''}
        Output of every session, kept {meta?.days ? `${meta.days} days` : 'until deleted'}. Each playback is recorded in the audit log.
      </p>
      {error && <p className="form-error">{error}</p>}
      {rows && rows.length === 0 && !error && <p className="pop-sub">No recordings yet.</p>}
      <div className="member-list">
        {rows?.map((r) => (
          <div key={r.id} className="member-row">
            <div className="kc-main">
              <span className="kc-title mono">{r.login ? `${r.login}@` : ''}{r.host}{r.port !== 22 && r.port ? `:${r.port}` : ''}{r.proto === 'telnet' && <span className="tag">telnet</span>}</span>
              <span className="kc-sub">{r.email || 'deleted account'} · {new Date(r.started).toLocaleString()} · {duration(r)} · {size(r.bytes)}{live(r) ? ' · running' : ''}</span>
            </div>
            {confirm === r.id ? (
              <>
                <button className="btn danger sm-btn" onClick={() => remove(r.id)}>Delete</button>
                <button className="btn sm-btn" onClick={() => setConfirm(null)}>Keep</button>
              </>
            ) : (
              <>
                <button className="btn sm-btn" onClick={() => play(r)}><Icon name="play" size={13} /> Play</button>
                {!live(r) && <button className="icon-btn sm" title="Delete recording" aria-label="Delete recording" onClick={() => setConfirm(r.id)}><Icon name="trash" size={13} /></button>}
              </>
            )}
          </div>
        ))}
      </div>
      {playing && (
        <Suspense fallback={null}>
          <CastPlayer cast={playing.cast} name={playing.name} onClose={() => setPlaying(null)} />
        </Suspense>
      )}
    </section>
  )
}
