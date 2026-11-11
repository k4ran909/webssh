import { liveSessions, setModifiers, useModifiers } from '../lib/live'

// Keys phones don't have. Ctrl and Alt are sticky: they apply to the next
// key, whether it comes from this bar or the on-screen keyboard.
const KEYS: [string, string][] = [
  ['Esc', '\x1b'], ['Tab', '\t'],
  ['↑', '\x1b[A'], ['↓', '\x1b[B'], ['←', '\x1b[D'], ['→', '\x1b[C'],
  ['|', '|'], ['~', '~'], ['/', '/'], ['-', '-'],
  ['Home', '\x1b[H'], ['End', '\x1b[F'], ['PgUp', '\x1b[5~'], ['PgDn', '\x1b[6~'],
]

const enc = new TextEncoder()

export function KeysBar({ sessionId }: { sessionId: string | null }) {
  const mods = useModifiers()
  const send = (seq: string) => {
    const s = liveSessions.get(sessionId ?? '')
    if (!s?.isReady()) return
    let out = seq
    if (mods.ctrl && seq.length === 1) {
      const c = seq.toUpperCase().charCodeAt(0)
      if (c >= 0x40 && c <= 0x5f) out = String.fromCharCode(c & 0x1f)
    }
    if (mods.alt) out = '\x1b' + out
    setModifiers({ ctrl: false, alt: false })
    s.write(enc.encode(out))
    s.focus()
  }
  // preventDefault on pointerdown keeps focus (and the phone keyboard) on the terminal.
  const keep = (e: React.PointerEvent) => e.preventDefault()
  return (
    <div className="keysbar" role="toolbar" aria-label="Extra keys">
      <button className={mods.ctrl ? 'on' : ''} aria-pressed={mods.ctrl} onPointerDown={keep} onClick={() => setModifiers({ ctrl: !mods.ctrl })}>Ctrl</button>
      <button className={mods.alt ? 'on' : ''} aria-pressed={mods.alt} onPointerDown={keep} onClick={() => setModifiers({ alt: !mods.alt })}>Alt</button>
      {KEYS.map(([label, seq]) => (
        <button key={label} onPointerDown={keep} onClick={() => send(seq)}>{label}</button>
      ))}
    </div>
  )
}
