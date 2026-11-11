import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import type { Cast } from '../lib/cast'
import { inkTheme } from '../lib/xtermTheme'
import { Icon } from './icons'

const fmt = (s: number) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, '0')}`
const SPEEDS = [1, 2, 4, 8]
/** Long idle gaps are shortened so playback doesn't sit on a blank prompt. */
const MAX_IDLE = 2

export default function CastPlayer({ cast, name, onClose }: { cast: Cast; name: string; onClose: () => void }) {
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const [playing, setPlaying] = useState(true)
  const [speed, setSpeed] = useState(1)
  const [pos, setPos] = useState(0) // seconds, on the idle-capped timeline
  const state = useRef({ idx: 0, clock: 0 })

  // Idle-capped timestamps.
  const times = useRef<number[]>([])
  if (times.current.length !== cast.events.length) {
    let prev = 0
    let acc = 0
    times.current = cast.events.map((e) => {
      acc += Math.min(e.t - prev, MAX_IDLE)
      prev = e.t
      return acc
    })
  }
  const duration = times.current.at(-1) ?? 0

  useEffect(() => {
    const term = new Terminal({ cols: cast.width, rows: cast.height, fontFamily: '"Geist Mono", ui-monospace, monospace', fontSize: 13, theme: inkTheme, disableStdin: true, scrollback: 2000 })
    term.loadAddon(new FitAddon())
    term.open(hostRef.current!)
    termRef.current = term
    return () => term.dispose()
  }, [cast])

  const seek = (to: number) => {
    const term = termRef.current!
    term.reset()
    let i = 0
    let out = ''
    while (i < cast.events.length && times.current[i] <= to) {
      const e = cast.events[i]
      if (e.type === 'o') out += e.data
      else {
        const [c, r] = e.data.split('x').map(Number)
        if (c && r) term.resize(c, r)
      }
      i++
    }
    term.write(out)
    state.current = { idx: i, clock: to }
    setPos(to)
  }

  useEffect(() => {
    if (!playing) return
    let last = performance.now()
    let raf = 0
    const tick = (now: number) => {
      const term = termRef.current
      if (!term) return
      const st = state.current
      st.clock += ((now - last) / 1000) * speed
      last = now
      let chunk = ''
      while (st.idx < cast.events.length && times.current[st.idx] <= st.clock) {
        const e = cast.events[st.idx++]
        if (e.type === 'o') chunk += e.data
        else {
          if (chunk) { term.write(chunk); chunk = '' }
          const [c, r] = e.data.split('x').map(Number)
          if (c && r) term.resize(c, r)
        }
      }
      if (chunk) term.write(chunk)
      setPos(Math.min(st.clock, duration))
      if (st.idx >= cast.events.length) {
        setPlaying(false)
        return
      }
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [playing, speed, cast, duration])

  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal player" role="dialog" aria-label={`Playback of ${name}`}
        onKeyDown={(e) => {
          if (e.key === 'Escape') onClose()
          if (e.key === ' ') { e.preventDefault(); setPlaying((p) => !p) }
        }}>
        <div className="editor-bar">
          <span className="mono editor-path">{cast.title || name}</span>
          <span className="pop-sub">{cast.width}×{cast.height}</span>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div className="player-screen"><div ref={hostRef} /></div>
        <div className="player-bar">
          <button className="icon-btn" aria-label={playing ? 'Pause' : 'Play'} autoFocus
            onClick={() => {
              if (!playing && state.current.idx >= cast.events.length) seek(0)
              setPlaying((p) => !p)
            }}>
            <Icon name={playing ? 'pause' : 'play'} />
          </button>
          <span className="mono pop-sub">{fmt(pos)}</span>
          <input type="range" className="scrub" min={0} max={Math.max(duration, 0.01)} step={0.05} value={pos} aria-label="Position"
            onChange={(e) => { setPlaying(false); seek(Number(e.target.value)) }} />
          <span className="mono pop-sub">{fmt(duration)}</span>
          <div className="seg mini">
            {SPEEDS.map((s) => <button key={s} className={speed === s ? 'on' : ''} onClick={() => setSpeed(s)}>{s}×</button>)}
          </div>
        </div>
      </div>
    </div>
  )
}
