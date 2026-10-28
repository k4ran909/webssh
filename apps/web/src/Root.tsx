import { lazy, Suspense, useCallback, useEffect, useState } from 'react'
import { api, type SessionInfo, type SignedIn } from './lib/api'
import { logout, type Opened } from './lib/account'
import { credentialCache } from './lib/live'
import { getPrefs, type Prefs } from './lib/storage'
import { vault } from './lib/vault'
import { AuthScreen, LockScreen, RecoveryCode } from './components/AuthScreens'
import { Logo } from './components/icons'

const App = lazy(() => import('./App').then((m) => ({ default: m.App })))

type Phase =
  | { k: 'loading' }
  | { k: 'offline'; msg: string }
  | { k: 'auth'; signupOpen: boolean }
  // App stays mounted while locked so open SSH sessions survive a screen lock.
  | { k: 'app'; session: SignedIn; locked: boolean; mounted: boolean; recoveryCode?: string }

export function Root() {
  const [phase, setPhase] = useState<Phase>({ k: 'loading' })
  const [prefs, setPrefs] = useState<Prefs>(getPrefs)
  const [notice, setNotice] = useState('')

  const load = useCallback(async () => {
    try {
      const s = await api<SessionInfo>('GET', '/api/session')
      setPhase(s.user && s.keys ? { k: 'app', session: { user: s.user, keys: s.keys }, locked: true, mounted: false } : { k: 'auth', signupOpen: s.signupOpen })
    } catch (e) {
      setPhase({ k: 'offline', msg: e instanceof Error ? e.message : 'Gateway unreachable.' })
    }
  }, [])

  useEffect(() => {
    load()
    vault.onSignedOut = () => location.reload()
  }, [load])

  const openVault = async (key: CryptoKey, isNew = false) => {
    await vault.open(key)
    const imported = await vault.importLegacy().catch(() => 0)
    if (isNew && !vault.getState().entries.some((e) => e.kind === 'snippet')) await vault.seedDefaults().catch(() => {})
    if (imported) setNotice(`Moved ${imported} saved host${imported === 1 ? '' : 's'} from this browser into your vault.`)
  }

  const opened = async (o: Opened, isNew: boolean) => {
    await openVault(o.vaultKey, isNew)
    setPhase({ k: 'app', session: o.session, locked: false, mounted: true, recoveryCode: o.recoveryCode })
  }

  const lock = useCallback(() => {
    vault.lock()
    credentialCache.clear()
    setPhase((p) => (p.k === 'app' ? { ...p, locked: true } : p))
  }, [])

  const signOut = useCallback(async () => {
    try {
      await logout()
    } finally {
      vault.lock()
      location.reload() // also tears down every SSH session
    }
  }, [])

  const locked = phase.k === 'app' && phase.locked

  // Auto-lock after inactivity (keystrokes in terminals count as activity).
  useEffect(() => {
    if (phase.k !== 'app' || locked || !prefs.autoLock) return
    const ms = prefs.autoLock * 60_000
    let t = window.setTimeout(lock, ms)
    const bump = () => {
      clearTimeout(t)
      t = window.setTimeout(lock, ms)
    }
    const evs = ['pointerdown', 'keydown', 'wheel'] as const
    evs.forEach((ev) => window.addEventListener(ev, bump, { capture: true, passive: true }))
    return () => {
      clearTimeout(t)
      evs.forEach((ev) => window.removeEventListener(ev, bump, { capture: true }))
    }
  }, [phase.k, locked, prefs.autoLock, lock])

  useEffect(() => {
    if (!notice) return
    const t = setTimeout(() => setNotice(''), 6000)
    return () => clearTimeout(t)
  }, [notice])

  if (phase.k === 'loading') return <div className="boot"><Logo size={40} /></div>
  if (phase.k === 'offline') {
    return (
      <div className="boot">
        <Logo size={40} />
        <p>{phase.msg}</p>
        <button className="btn" onClick={() => { setPhase({ k: 'loading' }); load() }}>Retry</button>
      </div>
    )
  }
  if (phase.k === 'auth') return <AuthScreen signupOpen={phase.signupOpen} onOpened={opened} />

  return (
    <>
      {phase.mounted && (
        <Suspense fallback={<div className="boot"><Logo size={40} /></div>}>
          <App
            session={phase.session}
            locked={phase.locked}
            prefs={prefs}
            setPrefs={setPrefs}
            onSession={(session) => setPhase((p) => (p.k === 'app' ? { ...p, session } : p))}
            onLock={lock}
            onLogout={signOut}
          />
        </Suspense>
      )}
      {phase.locked && (
        <LockScreen
          email={phase.session.user.email}
          keys={phase.session.keys}
          onUnlocked={async (key) => {
            await openVault(key)
            setPhase((p) => (p.k === 'app' ? { ...p, locked: false, mounted: true } : p))
          }}
          onLogout={signOut}
        />
      )}
      {!phase.locked && phase.recoveryCode && (
        <RecoveryCode code={phase.recoveryCode} onDone={() => setPhase((p) => (p.k === 'app' ? { ...p, recoveryCode: undefined } : p))} />
      )}
      {notice && <div className="notice" role="status">{notice}</div>}
    </>
  )
}
