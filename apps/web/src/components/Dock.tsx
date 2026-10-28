import { algoLabel } from '../lib/protocol'
import type { Action, WorkspaceState } from '../lib/workspace'

const PHASE_LABEL = { ready: 'Connected', connecting: 'Connecting', hostkey: 'Verify host key', error: 'Failed', ended: 'Ended', lost: 'Lost' }
const dotClass = (p: string) => (p === 'ready' ? 'ok' : p === 'error' ? 'bad' : p === 'connecting' || p === 'hostkey' ? 'busy' : '')

/** Bottom bar: a taskbar for windows plus facts about the focused session. */
export function Dock({ ws, showTasks, dispatch }: { ws: WorkspaceState; showTasks: boolean; dispatch: (a: Action) => void }) {
  const focused = ws.sessions.find((s) => s.id === ws.focusedId)
  const ready = ws.sessions.filter((s) => s.meta.phase === 'ready').length

  return (
    <footer className="dock">
      {showTasks && (
        <div className="tasks">
          {ws.sessions.map((s, i) => (
            <button
              key={s.id}
              className={`task${s.id === ws.focusedId && !s.minimized ? ' on' : ''}${s.minimized ? ' min' : ''}`}
              title={`${s.title}${i < 9 ? ` (Alt+${i + 1})` : ''}`}
              onClick={() => dispatch(s.id === ws.focusedId && !s.minimized ? { type: 'minimize', id: s.id } : { type: 'restore', id: s.id })}
            >
              <span className={`dot ${dotClass(s.meta.phase)}`} />
              <span className="task-title">{s.title}</span>
            </button>
          ))}
        </div>
      )}
      <span className="sp" />
      {focused ? (
        <span className="facts">
          <span className="st"><span className={`dot ${dotClass(focused.meta.phase)}`} /><b>{PHASE_LABEL[focused.meta.phase]}</b></span>
          {focused.meta.algo && <span className="hide-sm" title={focused.meta.fingerprint}>{algoLabel(focused.meta.algo)} {focused.meta.fingerprint?.slice(7, 17)}…</span>}
          {focused.meta.cols > 0 && <span className="hide-sm">{focused.meta.cols}×{focused.meta.rows}</span>}
        </span>
      ) : (
        <span className="facts"><span><b>{ws.sessions.length}</b> sessions</span></span>
      )}
      <span className="facts hide-sm"><span>{ready} live</span><span>UTF-8</span></span>
    </footer>
  )
}
