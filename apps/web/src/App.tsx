import { lazy, Suspense, useState } from 'react'
import type { Target } from './lib/protocol'
import { ConnectForm } from './components/ConnectForm'
import { ThemeToggle } from './components/ThemeToggle'
import { Logo } from './components/icons'

// xterm.js is most of the bundle; the connect screen doesn't need it.
const TerminalView = lazy(() => import('./components/TerminalView').then((m) => ({ default: m.TerminalView })))

export function App() {
  // The credential stays in memory only while a session view is open, so
  // "Reconnect" works without retyping. Leaving the session drops it.
  const [target, setTarget] = useState<Target | null>(null)
  const [attempt, setAttempt] = useState(0)

  return (
    <div className="app">
      <header className="topbar">
        <Logo size={20} />
        <span className="wordmark">webssh</span>
        {target && <span className="crumb">/ {target.user}@{target.host}</span>}
        <span className="spacer" />
        {target && <button className="btn" onClick={() => setTarget(null)}>Disconnect</button>}
        <ThemeToggle />
      </header>
      <main>
        {target ? (
          <Suspense fallback={null}>
            <TerminalView
              key={attempt}
              target={target}
              onBack={() => setTarget(null)}
              onReconnect={() => setAttempt((a) => a + 1)}
            />
          </Suspense>
        ) : (
          <ConnectForm onConnect={(t) => { setTarget(t); setAttempt((a) => a + 1) }} />
        )}
      </main>
    </div>
  )
}
