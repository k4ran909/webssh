import { useEffect, useState, type FormEvent } from 'react'
import QRCode from 'qrcode'
import { api, type AuditEvent, type SignedIn } from '../lib/api'
import { authHashFor, changePassword, regenerateRecovery } from '../lib/account'
import { MIN_PASSWORD, RecoveryCode, StrengthMeter } from './AuthScreens'
import { Icon } from './icons'

interface Props {
  session: SignedIn
  autoLock: number
  onAutoLock: (minutes: number) => void
  onSession: (s: SignedIn) => void
  onLock: () => void
  onLogout: () => void
  onClose: () => void
}

const message = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')

export function AccountDialog({ session, autoLock, onAutoLock, onSession, onLock, onLogout, onClose }: Props) {
  const [tab, setTab] = useState<'security' | 'activity'>('security')
  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal wide-modal" role="dialog" aria-label="Account and security" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="acct-head">
          <div>
            <h2>Account</h2>
            <p className="pop-sub">{session.user.email}</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div className="seg acct-tabs" role="tablist">
          <button role="tab" aria-selected={tab === 'security'} className={tab === 'security' ? 'on' : ''} onClick={() => setTab('security')}>Security</button>
          <button role="tab" aria-selected={tab === 'activity'} className={tab === 'activity' ? 'on' : ''} onClick={() => setTab('activity')}>Activity</button>
        </div>
        <div className="acct-body">
          {tab === 'security' ? (
            <>
              <Section title="Session">
                <div className="set-row">
                  <span>Auto-lock after</span>
                  <select className="input select" value={autoLock} onChange={(e) => onAutoLock(Number(e.target.value))}>
                    {[5, 15, 30, 60, 240].map((m) => <option key={m} value={m}>{m < 60 ? `${m} minutes` : `${m / 60} hour${m > 60 ? 's' : ''}`}</option>)}
                    <option value={0}>Never</option>
                  </select>
                </div>
                <div className="acts">
                  <button className="btn" onClick={onLock}><Icon name="lock" size={14} /> Lock now</button>
                  <button className="btn" onClick={onLogout}><Icon name="logout" size={14} /> Sign out</button>
                </div>
              </Section>
              <TwoFactor session={session} onSession={onSession} />
              <ChangePassword session={session} onSession={onSession} />
              <Recovery session={session} />
            </>
          ) : (
            <Activity />
          )}
        </div>
      </div>
    </div>
  )
}

function Section({ title, sub, children }: { title: string; sub?: string; children: React.ReactNode }) {
  return (
    <section className="acct-sec">
      <h3>{title}</h3>
      {sub && <p className="pop-sub">{sub}</p>}
      {children}
    </section>
  )
}

function useAction() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState('')
  const run = async (f: () => Promise<string | void>) => {
    setBusy(true)
    setError('')
    setDone('')
    try {
      const msg = await f()
      if (msg) setDone(msg)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  return { busy, error, done, run }
}

function TwoFactor({ session, onSession }: { session: SignedIn; onSession: (s: SignedIn) => void }) {
  const [setup, setSetup] = useState<{ secret: string; qr: string } | null>(null)
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const a = useAction()
  const on = session.user.totp

  const begin = () => a.run(async () => {
    const r = await api<{ secret: string; uri: string }>('POST', '/api/auth/totp/setup', {})
    const qr = await QRCode.toDataURL(r.uri, { margin: 1, width: 176, color: { dark: '#0a0a0a', light: '#ffffff' } })
    setSetup({ secret: r.secret, qr })
  })

  const enable = (e: FormEvent) => {
    e.preventDefault()
    a.run(async () => {
      await api('POST', '/api/auth/totp/enable', { code })
      onSession({ ...session, user: { ...session.user, totp: true } })
      setSetup(null)
      setCode('')
      return 'Two-factor authentication is on.'
    })
  }

  const disable = (e: FormEvent) => {
    e.preventDefault()
    a.run(async () => {
      const authHash = await authHashFor(password, session.keys)
      await api('POST', '/api/auth/totp/disable', { authHash, code })
      onSession({ ...session, user: { ...session.user, totp: false } })
      setPassword('')
      setCode('')
      return 'Two-factor authentication is off.'
    })
  }

  return (
    <Section title="Two-factor authentication" sub={on ? 'On. Sign-ins need a code from your authenticator app.' : 'Off. Add a second step to sign-in with an authenticator app.'}>
      {!on && !setup && <div className="acts"><button className="btn" onClick={begin} disabled={a.busy}><Icon name="shield" size={14} /> Set up</button></div>}
      {!on && setup && (
        <form className="totp-setup" onSubmit={enable}>
          <img src={setup.qr} width={176} height={176} alt="QR code for your authenticator app" />
          <div>
            <p className="pop-sub">Scan with Google Authenticator, 1Password, Authy… or enter this key:</p>
            <code className="secret">{setup.secret.match(/.{1,4}/g)!.join(' ')}</code>
            <label className="fld" style={{ marginTop: 12 }}>
              <span>6-digit code</span>
              <input className="input mono otp" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))} autoFocus />
            </label>
            <button className="btn primary" type="submit" disabled={a.busy || code.length !== 6}>Turn on</button>
          </div>
        </form>
      )}
      {on && (
        <form className="inline-form" onSubmit={disable}>
          <input className="input" type="password" placeholder="Master password" aria-label="Master password" value={password} onChange={(e) => setPassword(e.target.value)} />
          <input className="input mono" inputMode="numeric" maxLength={6} placeholder="Code" aria-label="Authenticator code" value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))} />
          <button className="btn" type="submit" disabled={a.busy || !password || code.length !== 6}>Turn off</button>
        </form>
      )}
      {a.error && <p className="form-error">{a.error}</p>}
      {a.done && <p className="ok-note">{a.done}</p>}
    </Section>
  )
}

function ChangePassword({ session, onSession }: { session: SignedIn; onSession: (s: SignedIn) => void }) {
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const a = useAction()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (next.length < MIN_PASSWORD) return a.run(async () => { throw new Error(`Use at least ${MIN_PASSWORD} characters.`) })
    if (next !== confirm) return a.run(async () => { throw new Error("New passwords don't match.") })
    a.run(async () => {
      onSession(await changePassword(cur, next, session.keys))
      setCur(''); setNext(''); setConfirm('')
      return 'Password changed. Other devices were signed out.'
    })
  }
  return (
    <Section title="Master password" sub="Re-wraps your vault key. Your saved hosts don't need re-encrypting.">
      <form className="stack-form" onSubmit={submit}>
        <input className="input" type="password" autoComplete="current-password" placeholder="Current password" aria-label="Current password" value={cur} onChange={(e) => setCur(e.target.value)} />
        <input className="input" type="password" autoComplete="new-password" placeholder="New password" aria-label="New password" value={next} onChange={(e) => setNext(e.target.value)} />
        <StrengthMeter pw={next} />
        <input className="input" type="password" autoComplete="new-password" placeholder="Confirm new password" aria-label="Confirm new password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        <div className="acts"><button className="btn" type="submit" disabled={a.busy || !cur || !next}>{a.busy ? 'Working…' : 'Change password'}</button></div>
      </form>
      {a.error && <p className="form-error">{a.error}</p>}
      {a.done && <p className="ok-note">{a.done}</p>}
    </Section>
  )
}

function Recovery({ session }: { session: SignedIn }) {
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const a = useAction()
  if (code) return <Section title="Recovery code"><RecoveryCode inline code={code} onDone={() => setCode('')} /></Section>
  return (
    <Section title="Recovery code" sub="Lost it? Make a new one. The old code stops working.">
      <form className="inline-form" onSubmit={(e) => { e.preventDefault(); a.run(async () => { setCode(await regenerateRecovery(password, session.keys)); setPassword('') }) }}>
        <input className="input" type="password" placeholder="Master password" aria-label="Master password" value={password} onChange={(e) => setPassword(e.target.value)} />
        <button className="btn" type="submit" disabled={a.busy || !password}>{a.busy ? 'Working…' : 'New recovery code'}</button>
      </form>
      {a.error && <p className="form-error">{a.error}</p>}
    </Section>
  )
}

const ACTIONS: Record<string, string> = {
  'account.created': 'Account created',
  login: 'Signed in',
  'login.failed': 'Failed sign-in',
  logout: 'Signed out',
  'password.changed': 'Master password changed',
  'totp.enabled': 'Two-factor turned on',
  'totp.disabled': 'Two-factor turned off',
  'recovery.failed': 'Failed recovery attempt',
  'account.recovered': 'Vault recovered',
  'recovery.regenerated': 'New recovery code',
  'ssh.connect': 'SSH connect',
  'ssh.session': 'SSH session ended',
}

const fmtBytes = (n: number) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KB` : `${(n / (1 << 20)).toFixed(1)} MB`)
const fmtDur = (s: number) => (s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`)

function describe(e: AuditEvent): { text: string; bad: boolean } {
  let d: Record<string, string | number> = {}
  try { d = JSON.parse(e.detail) } catch { /* old rows */ }
  const dest = d.host ? `${d.user}@${d.host}${d.port && d.port !== 22 ? ':' + d.port : ''}` : ''
  if (e.action === 'ssh.connect') return { text: `${dest}${d.result !== 'ok' ? ` · ${d.result}` : ''}`, bad: d.result !== 'ok' }
  if (e.action === 'ssh.session') return { text: `${dest} · ${fmtDur(Number(d.seconds))} · ↑${fmtBytes(Number(d.bytesIn))} ↓${fmtBytes(Number(d.bytesOut))}`, bad: false }
  if (e.action === 'login.failed') return { text: `wrong ${d.reason === 'totp' ? '2FA code' : 'password'}`, bad: true }
  return { text: '', bad: e.action.endsWith('.failed') }
}

function Activity() {
  const [events, setEvents] = useState<AuditEvent[] | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    api<{ events: AuditEvent[] }>('GET', '/api/audit').then((r) => setEvents(r.events), (e) => setError(message(e)))
  }, [])
  if (error) return <p className="form-error">{error}</p>
  if (!events) return <p className="pop-sub">Loading…</p>
  return (
    <div className="activity">
      <p className="pop-sub" style={{ marginBottom: 10 }}>Sign-ins and SSH connections for your account. Terminal contents are never recorded.</p>
      {events.map((e) => {
        const d = describe(e)
        return (
          <div key={e.id} className={`act-row${d.bad ? ' bad' : ''}`}>
            <span className="act-what">{ACTIONS[e.action] ?? e.action}</span>
            <span className="act-detail mono">{d.text}</span>
            <span className="act-meta mono">{e.ip} · {new Date(e.createdAt).toLocaleString()}</span>
          </div>
        )
      })}
    </div>
  )
}
