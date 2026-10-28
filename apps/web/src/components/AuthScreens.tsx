import { useRef, useState, type FormEvent, type ReactNode } from 'react'
import { ApiError, type Keys } from '../lib/api'
import { login, prepareLogin, recover, signup, unlock, type Opened } from '../lib/account'
import type { PasswordKeys } from '../lib/crypto'
import { Icon, Logo } from './icons'

export const MIN_PASSWORD = 10

const message = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')

function Shell({ apart, children }: { apart: boolean; children: ReactNode }) {
  return (
    <div className="auth-shell">
      <div className="auth-card">
        <Logo size={48} apart={apart} />
        {children}
      </div>
    </div>
  )
}

export function strength(pw: string): { score: 0 | 1 | 2 | 3; label: string } {
  if (pw.length < MIN_PASSWORD) return { score: 0, label: `At least ${MIN_PASSWORD} characters` }
  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length
  const s = pw.length >= 16 || (pw.length >= 12 && classes >= 3) ? 3 : classes >= 2 ? 2 : 1
  return { score: s as 1 | 2 | 3, label: ['', 'Weak', 'Good', 'Strong'][s] }
}

export function StrengthMeter({ pw }: { pw: string }) {
  if (!pw) return null
  const s = strength(pw)
  return (
    <div className={`meter s${s.score}`} aria-live="polite">
      <span /><span /><span />
      <em>{s.label}</em>
    </div>
  )
}

type Mode = 'login' | 'signup' | 'recover'

export function AuthScreen({ signupOpen, onOpened }: { signupOpen: boolean; onOpened: (o: Opened, isNew: boolean) => void }) {
  const [mode, setMode] = useState<Mode>(signupOpen ? 'signup' : 'login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [code, setCode] = useState('')
  const [totp, setTotp] = useState('')
  const [needTotp, setNeedTotp] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const prepared = useRef<PasswordKeys | null>(null)

  const switchMode = (m: Mode) => {
    setMode(m)
    setError('')
    setNeedTotp(false)
    setPassword('')
    setConfirm('')
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    const em = email.trim()
    if (!/^[^@\s]+@[^@\s]+$/.test(em)) return setError('Enter your email address.')
    if (mode !== 'login') {
      if (password.length < MIN_PASSWORD) return setError(`Use at least ${MIN_PASSWORD} characters.`)
      if (password !== confirm) return setError("Passwords don't match.")
    }
    try {
      if (mode === 'login') {
        if (!prepared.current) {
          setBusy('Deriving keys…')
          prepared.current = await prepareLogin(em, password)
        }
        setBusy('Signing in…')
        onOpened(await login(em, prepared.current, totp), false)
      } else if (mode === 'signup') {
        setBusy('Creating your encrypted vault…')
        onOpened(await signup(em, password), true)
      } else {
        setBusy('Recovering vault…')
        onOpened(await recover(em, code, password), false)
      }
    } catch (err) {
      if (err instanceof ApiError && err.code === 'TOTP_REQUIRED') {
        setNeedTotp(true)
      } else {
        if (err instanceof ApiError && err.code === 'INVALID_CREDENTIALS') prepared.current = null
        setError(message(err))
      }
    } finally {
      setBusy('')
    }
  }

  const titles = { login: 'Sign in', signup: signupOpen ? 'Create your account' : 'Create account', recover: 'Recover your vault' }
  const subs = {
    login: 'Your master password unlocks your encrypted vault.',
    signup: 'Your master password encrypts your vault in this browser. The server never sees it.',
    recover: 'Use the recovery code you saved when you created the account.',
  }

  return (
    <Shell apart={!busy && !needTotp}>
      <h1>{needTotp ? 'Two-factor code' : titles[mode]}</h1>
      <p className="sub">{needTotp ? 'Enter the 6-digit code from your authenticator app.' : subs[mode]}</p>

      <form onSubmit={submit} noValidate autoComplete="on">
        {!needTotp && (
          <>
            <label className="fld">
              <span>Email</span>
              <input className="input" type="email" autoComplete="username" value={email} autoFocus
                onChange={(e) => { setEmail(e.target.value); prepared.current = null }} />
            </label>
            {mode === 'recover' && (
              <label className="fld">
                <span>Recovery code</span>
                <input className="input mono" value={code} onChange={(e) => setCode(e.target.value)} placeholder="XXXX-XXXX-XXXX-…" spellCheck={false} autoCapitalize="characters" />
              </label>
            )}
            <label className="fld">
              <span>{mode === 'recover' ? 'New master password' : 'Master password'}</span>
              <input className="input" type="password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} value={password}
                onChange={(e) => { setPassword(e.target.value); prepared.current = null }} />
            </label>
            {mode !== 'login' && (
              <>
                <StrengthMeter pw={password} />
                <label className="fld">
                  <span>Confirm password</span>
                  <input className="input" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
                </label>
              </>
            )}
          </>
        )}
        {needTotp && (
          <label className="fld">
            <span>Code</span>
            <input className="input mono otp" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={totp} autoFocus
              onChange={(e) => setTotp(e.target.value.replace(/\D/g, ''))} />
          </label>
        )}

        {mode === 'signup' && !needTotp && (
          <p className="warn-note"><Icon name="alert" size={14} /> If you forget this password, only your recovery code can get your vault back.</p>
        )}
        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="btn primary lg wide" type="submit" disabled={!!busy}>
          {busy || (needTotp ? 'Verify' : mode === 'login' ? 'Sign in' : mode === 'signup' ? 'Create account' : 'Reset password')}
        </button>
      </form>

      <div className="auth-links">
        {needTotp ? (
          <button className="link" onClick={() => { setNeedTotp(false); setTotp('') }}>Back</button>
        ) : (
          <>
            {mode !== 'login' && <button className="link" onClick={() => switchMode('login')}>I have an account</button>}
            {mode !== 'signup' && signupOpen && <button className="link" onClick={() => switchMode('signup')}>Create account</button>}
            {mode !== 'recover' && <button className="link" onClick={() => switchMode('recover')}>Forgot password?</button>}
          </>
        )}
      </div>
    </Shell>
  )
}

export function LockScreen({ email, keys, onUnlocked, onLogout }: { email: string; keys: Keys; onUnlocked: (k: CryptoKey) => Promise<void>; onLogout: () => void }) {
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [shake, setShake] = useState(0)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!password || busy) return
    setBusy(true)
    setError('')
    try {
      await onUnlocked(await unlock(password, keys))
    } catch (err) {
      setError(message(err))
      setShake((n) => n + 1)
      setBusy(false)
    }
  }

  return (
    <div className="lock-overlay">
      <Shell apart={!busy}>
        <h1>Vault locked</h1>
        <p className="sub">{email}</p>
        <form onSubmit={submit}>
          <label key={shake} className={`unlock-field${shake ? ' shake' : ''}`}>
            <input type="password" placeholder="Master password" aria-label="Master password" autoComplete="current-password"
              value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
            <button className="go" type="submit" aria-label="Unlock" disabled={busy}><Icon name="arrow" size={16} /></button>
          </label>
          {error && <p className="form-error" role="alert" style={{ marginTop: 10 }}>{error}</p>}
          {busy && <p className="pop-sub" style={{ marginTop: 10 }}>Deriving keys…</p>}
        </form>
        <div className="auth-links"><button className="link" onClick={onLogout}>Sign out</button></div>
      </Shell>
    </div>
  )
}

export function RecoveryCode({ code, onDone, inline }: { code: string; onDone: () => void; inline?: boolean }) {
  const [saved, setSaved] = useState(false)
  const [copied, setCopied] = useState(false)
  const body = (
    <>
      <h1>Save your recovery code</h1>
      <p className="sub">It's the only way back into your vault if you forget your master password. It's shown once.</p>
      <div className="rcode mono" aria-label="Recovery code">
        {code.split('-').map((g, i) => <span key={i}>{g}</span>)}
      </div>
      <button className="btn wide" onClick={() => navigator.clipboard?.writeText(code).then(() => setCopied(true)).catch(() => {})}>
        <Icon name="copy" size={14} /> {copied ? 'Copied' : 'Copy code'}
      </button>
      <label className="check" style={{ marginTop: 16 }}>
        <input type="checkbox" checked={saved} onChange={(e) => setSaved(e.target.checked)} />
        <span>I've stored it somewhere safe, like a password manager</span>
      </label>
      <button className="btn primary lg wide" disabled={!saved} onClick={onDone}>Continue</button>
    </>
  )
  if (inline) return <div className="rcode-inline">{body}</div>
  return <div className="lock-overlay"><Shell apart={false}>{body}</Shell></div>
}
