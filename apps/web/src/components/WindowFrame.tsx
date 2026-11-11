import { memo, useRef, useState, type PointerEvent as RPointerEvent, type ReactNode } from 'react'
import { MIN_H, MIN_W, type Action, type Rect, type Session } from '../lib/workspace'
import { Icon } from './icons'
import { Popover } from './Popover'

type Dir = 'n' | 's' | 'e' | 'w' | 'ne' | 'nw' | 'se' | 'sw'
const DIRS: Dir[] = ['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw']
type Snap = 'max' | 'left' | 'right' | null

const SNAP_EDGE = 12

interface Props {
  session: Session
  focused: boolean
  /** Tabs layout: every window fills the workspace and only the focused one shows. */
  tabbed: boolean
  broadcast: boolean
  area: { w: number; h: number }
  dispatch: (a: Action) => void
  onAction: (id: string, action: WindowAction) => void
  onSearch: (id: string) => void
  children: ReactNode
}

function snapRect(snap: Exclude<Snap, null>, area: { w: number; h: number }): Rect | 'max' {
  if (snap === 'max') return 'max'
  const w = Math.floor(area.w / 2)
  return { x: snap === 'left' ? 0 : area.w - w, y: 0, w, h: area.h }
}

export type WindowAction = 'split-right' | 'split-down' | 'duplicate' | 'forward' | 'record'

export const WindowFrame = memo(function WindowFrame({ session, focused, tabbed, broadcast, area, dispatch, onAction, onSearch, children }: Props) {
  const [menu, setMenu] = useState(false)
  const { id, rect, minimized, meta } = session
  const maximized = session.maximized || tabbed
  const frameRef = useRef<HTMLDivElement>(null)
  const [snap, setSnap] = useState<Snap>(null)
  const [dragging, setDragging] = useState(false)

  // Pointer capture keeps the drag alive over the terminal canvas and outside the window.
  const track = (e: RPointerEvent, onMove: (dx: number, dy: number, ev: PointerEvent) => void, onUp?: (ev: PointerEvent) => void) => {
    const el = e.currentTarget as HTMLElement
    const sx = e.clientX
    const sy = e.clientY
    el.setPointerCapture(e.pointerId)
    setDragging(true)
    const move = (ev: PointerEvent) => onMove(ev.clientX - sx, ev.clientY - sy, ev)
    const up = (ev: PointerEvent) => {
      el.removeEventListener('pointermove', move)
      el.removeEventListener('pointerup', up)
      el.removeEventListener('pointercancel', up)
      setDragging(false)
      onUp?.(ev)
    }
    el.addEventListener('pointermove', move)
    el.addEventListener('pointerup', up)
    el.addEventListener('pointercancel', up)
  }

  const startMove = (e: RPointerEvent) => {
    if (tabbed || e.button !== 0 || (e.target as HTMLElement).closest('button')) return
    e.preventDefault()
    const bounds = frameRef.current!.offsetParent!.getBoundingClientRect()
    // Dragging a maximized window restores it under the cursor, like a desktop OS.
    const start = maximized
      ? { ...rect, x: Math.max(0, e.clientX - bounds.left - rect.w / 2), y: 0 }
      : rect
    let pending: Snap = null
    track(
      e,
      (dx, dy, ev) => {
        const px = ev.clientX - bounds.left
        const py = ev.clientY - bounds.top
        pending = py <= SNAP_EDGE ? 'max' : px <= SNAP_EDGE ? 'left' : px >= bounds.width - SNAP_EDGE ? 'right' : null
        setSnap(pending)
        dispatch({ type: 'setRect', id, rect: { ...start, x: start.x + dx, y: start.y + dy } })
      },
      () => {
        setSnap(null)
        if (pending) dispatch({ type: 'snap', id, rect: snapRect(pending, area) })
      },
    )
  }

  const startResize = (dir: Dir) => (e: RPointerEvent) => {
    if (e.button !== 0 || maximized) return
    e.preventDefault()
    e.stopPropagation()
    dispatch({ type: 'focus', id })
    const s = rect
    track(e, (dx, dy) => {
      let { x, y, w, h } = s
      if (dir.includes('e')) w = s.w + dx
      if (dir.includes('s')) h = s.h + dy
      if (dir.includes('w')) {
        w = Math.max(MIN_W, s.w - dx)
        x = s.x + s.w - w
      }
      if (dir.includes('n')) {
        h = Math.max(MIN_H, s.h - dy)
        y = s.y + s.h - h
      }
      dispatch({ type: 'setRect', id, rect: { x, y, w, h } })
    })
  }

  const style = maximized
    ? { left: 0, top: 0, width: '100%', height: '100%', zIndex: session.z }
    : { left: rect.x, top: rect.y, width: rect.w, height: rect.h, zIndex: session.z }

  const statusDot = meta.phase === 'ready' ? 'ok' : meta.phase === 'error' ? 'bad' : meta.phase === 'ended' || meta.phase === 'lost' ? '' : 'busy'
  const preview = snap && snapRect(snap, area)

  return (
    <>
      {preview && (
        <div
          className="snap-preview"
          style={preview === 'max' ? { inset: 0 } : { left: preview.x, top: preview.y, width: preview.w, height: preview.h }}
        />
      )}
      <section
        ref={frameRef}
        className={`win${focused ? ' focused' : ''}${maximized ? ' maximized' : ''}${tabbed ? ' tabbed' : ''}${broadcast && meta.phase === 'ready' ? ' broadcasting' : ''}${dragging ? ' dragging' : ''}`}
        style={style}
        hidden={tabbed ? !focused : minimized}
        aria-label={session.title}
        onPointerDownCapture={() => dispatch({ type: 'focus', id })}
      >
        <header className="win-bar" onPointerDown={startMove} onDoubleClick={(e) => !tabbed && !(e.target as HTMLElement).closest('button') && dispatch({ type: 'toggleMaximize', id })}>
          <span className={`dot ${statusDot}`} />
          <span className="swatch" style={{ background: session.color }} />
          <span className="win-title">{session.title}</span>
          {meta.recording && <span className="rec-badge" title="Recording"><span className="rec-dot" />REC</span>}
          {meta.phase === 'resuming' && <span className="win-size">reconnecting…</span>}
          {meta.cols > 0 && <span className="win-size">{meta.cols}×{meta.rows}</span>}
          <span className="win-actions">
            <button
              title={meta.sftp ? 'Files (SFTP)' : 'Files are available once connected'}
              aria-label="Files"
              aria-pressed={session.files}
              className={session.files ? 'on' : ''}
              disabled={!meta.sftp}
              onClick={() => dispatch({ type: 'toggleFiles', id })}
            >
              <Icon name="folder" size={14} />
            </button>
            <button title="Find (Ctrl+Shift+F)" aria-label="Find" onClick={() => onSearch(id)}><Icon name="search" size={14} /></button>
            <span className="anchor">
              <button title="More" aria-label="More window actions" aria-expanded={menu} data-popover-anchor onClick={() => setMenu((m) => !m)}><Icon name="more" size={14} /></button>
              {menu && (
                <Popover onClose={() => setMenu(false)} className="right win-menu">
                  {([
                    ['split-right', 'split', 'Split right'],
                    ['split-down', 'splitDown', 'Split down'],
                    ['duplicate', 'copy', 'Duplicate session'],
                    ['forward', 'forward', 'Forward a port…'],
                    ['record', 'record', meta.recording ? 'Stop recording & save' : 'Record session'],
                  ] as const).map(([act, icon, label]) => (
                    <button key={act} className="pop-item" disabled={(act === 'forward' || act === 'record') && meta.phase !== 'ready' && !meta.recording}
                      onClick={() => { setMenu(false); onAction(id, act) }}>
                      <Icon name={icon} size={14} /><span>{label}</span>
                    </button>
                  ))}
                </Popover>
              )}
            </span>
            {!tabbed && (
              <>
                <button title="Minimize" aria-label="Minimize" onClick={() => dispatch({ type: 'minimize', id })}><Icon name="min" size={14} /></button>
                <button title={maximized ? 'Restore' : 'Maximize'} aria-label={maximized ? 'Restore' : 'Maximize'} onClick={() => dispatch({ type: 'toggleMaximize', id })}>
                  <Icon name={maximized ? 'restore' : 'max'} size={14} />
                </button>
              </>
            )}
            <button className="close" title="Close" aria-label="Close" onClick={() => dispatch({ type: 'close', id })}><Icon name="x" size={14} /></button>
          </span>
        </header>
        <div className="win-body">{children}</div>
        {!maximized && DIRS.map((d) => <div key={d} className={`rz rz-${d}`} onPointerDown={startResize(d)} />)}
      </section>
    </>
  )
})
