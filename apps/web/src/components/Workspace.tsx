import { lazy, Suspense, useEffect, useRef } from 'react'
import type { Action, SessionMeta, WorkspaceState } from '../lib/workspace'
import type { Layout, SavedHost } from '../lib/storage'
import { WindowFrame } from './WindowFrame'
import { Icon, Logo } from './icons'

// xterm.js is most of the bundle; load it with the first session.
const TerminalView = lazy(() => import('./TerminalView').then((m) => ({ default: m.TerminalView })))

interface Props {
  ws: WorkspaceState
  layout: Layout
  broadcast: boolean
  fontSize: number
  hosts: SavedHost[]
  dispatch: (a: Action) => void
  onMeta: (id: string, meta: Partial<SessionMeta>) => void
  onClose: (id: string) => void
  onReconnect: (id: string) => void
  onEdit: (id: string) => void
  onDuplicate: (id: string) => void
  onSearch: (id: string) => void
  onNew: () => void
  onOpenHost: (h: SavedHost) => void
}

export function Workspace(p: Props) {
  const { ws, layout, dispatch } = p
  const areaRef = useRef<HTMLDivElement>(null)
  const tabbed = layout === 'tabs'

  useEffect(() => {
    const el = areaRef.current!
    const ro = new ResizeObserver(() => dispatch({ type: 'area', w: el.clientWidth, h: el.clientHeight }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [dispatch])

  return (
    <div className="workspace">
      {tabbed && ws.sessions.length > 0 && (
        <div className="tabstrip" role="tablist">
          {ws.sessions.map((s) => (
            <button
              key={s.id}
              role="tab"
              aria-selected={s.id === ws.focusedId}
              className={`tab${s.id === ws.focusedId ? ' on' : ''}`}
              onClick={() => dispatch({ type: 'restore', id: s.id })}
              onAuxClick={(e) => e.button === 1 && p.onClose(s.id)}
            >
              <span className="swatch" style={{ background: s.color }} />
              <span className={`dot ${s.meta.phase === 'ready' ? 'ok' : s.meta.phase === 'error' ? 'bad' : s.meta.phase === 'connecting' || s.meta.phase === 'hostkey' ? 'busy' : ''}`} />
              {s.title}
              <span className="x" role="button" aria-label="Close tab" onClick={(e) => { e.stopPropagation(); p.onClose(s.id) }}>
                <Icon name="x" size={12} />
              </span>
            </button>
          ))}
          <button className="icon-btn" title="New connection" aria-label="New connection" onClick={p.onNew}><Icon name="plus" /></button>
        </div>
      )}

      <div className={`area${tabbed ? ' tabbed' : ''}`} ref={areaRef}>
        {ws.sessions.map((s) => (
          <WindowFrame
            key={s.id}
            session={s}
            focused={s.id === ws.focusedId}
            tabbed={tabbed}
            broadcast={p.broadcast}
            area={ws.area}
            dispatch={dispatch}
            onDuplicate={p.onDuplicate}
            onSearch={p.onSearch}
          >
            <Suspense fallback={null}>
              <TerminalView
                key={s.attempt}
                id={s.id}
                target={s.target}
                fontSize={p.fontSize}
                broadcast={p.broadcast}
                onMeta={p.onMeta}
                onClose={p.onClose}
                onReconnect={p.onReconnect}
                onEdit={p.onEdit}
              />
            </Suspense>
          </WindowFrame>
        ))}

        {ws.sessions.length === 0 && (
          <div className="empty">
            <Logo size={48} />
            <h2>No open sessions</h2>
            <p>Open a connection, or pick a saved host. Sessions open as windows you can drag, resize, snap and minimize.</p>
            <button className="btn primary lg" onClick={p.onNew}><Icon name="plus" size={14} /> New connection</button>
            {p.hosts.length > 0 && (
              <div className="empty-hosts">
                {p.hosts.slice(0, 6).map((h) => (
                  <button key={h.id} className="host-chip" onClick={() => p.onOpenHost(h)}>
                    <span className="swatch" style={{ background: h.color }} />
                    {h.label || h.host}
                  </button>
                ))}
              </div>
            )}
            <div className="kbd-hints">
              <span><kbd>Ctrl</kbd><kbd>⇧</kbd><kbd>K</kbd> command palette</span>
              <span><kbd>Alt</kbd><kbd>1-9</kbd> switch session</span>
              <span><kbd>Ctrl</kbd><kbd>⇧</kbd><kbd>F</kbd> find</span>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
