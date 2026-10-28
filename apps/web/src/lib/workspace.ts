import type { Target } from './protocol'

export type PhaseKind = 'connecting' | 'hostkey' | 'ready' | 'ended' | 'lost' | 'error'

export interface SessionMeta {
  phase: PhaseKind
  fingerprint?: string
  algo?: string
  cols: number
  rows: number
}

export interface Rect {
  x: number
  y: number
  w: number
  h: number
}

export interface Session {
  id: string
  target: Target
  title: string
  color: string
  /** Bumped to remount the terminal (reconnect). */
  attempt: number
  rect: Rect
  minimized: boolean
  maximized: boolean
  z: number
  meta: SessionMeta
}

export interface WorkspaceState {
  sessions: Session[]
  focusedId: string | null
  z: number
  area: { w: number; h: number }
}

export type Action =
  | { type: 'open'; target: Target; title: string; color: string }
  | { type: 'close'; id: string }
  | { type: 'focus'; id: string }
  | { type: 'setRect'; id: string; rect: Rect }
  | { type: 'snap'; id: string; rect: Rect | 'max' }
  | { type: 'minimize'; id: string }
  | { type: 'toggleMaximize'; id: string }
  | { type: 'restore'; id: string }
  | { type: 'reconnect'; id: string; target?: Target }
  | { type: 'meta'; id: string; meta: Partial<SessionMeta> }
  | { type: 'arrange'; mode: 'tile' | 'cascade' }
  | { type: 'area'; w: number; h: number }

export const MIN_W = 360
export const MIN_H = 220
const GAP = 8
const CASCADE = 28

export const initialWorkspace: WorkspaceState = { sessions: [], focusedId: null, z: 1, area: { w: 1200, h: 700 } }

const clamp = (v: number, lo: number, hi: number) => Math.min(Math.max(v, lo), Math.max(lo, hi))

export function clampRect(r: Rect, area: { w: number; h: number }): Rect {
  const w = clamp(r.w, Math.min(MIN_W, area.w), area.w)
  const h = clamp(r.h, Math.min(MIN_H, area.h), area.h)
  return { w, h, x: clamp(r.x, 0, area.w - w), y: clamp(r.y, 0, area.h - h) }
}

function defaultRect(i: number, area: { w: number; h: number }): Rect {
  const off = (i % 6) * CASCADE
  return clampRect({ x: 16 + off, y: 16 + off, w: Math.min(880, area.w - 32), h: Math.min(540, area.h - 32) }, area)
}

/** Top-most visible window, used when the focused one goes away. */
function topVisible(sessions: Session[], exclude?: string): string | null {
  const vis = sessions.filter((s) => !s.minimized && s.id !== exclude)
  if (!vis.length) return null
  return vis.reduce((a, b) => (b.z > a.z ? b : a)).id
}

const update = (s: WorkspaceState, id: string, f: (x: Session) => Session): Session[] =>
  s.sessions.map((x) => (x.id === id ? f(x) : x))

export function workspaceReducer(s: WorkspaceState, a: Action): WorkspaceState {
  switch (a.type) {
    case 'open': {
      const z = s.z + 1
      const session: Session = {
        id: crypto.randomUUID(),
        target: a.target,
        title: a.title,
        color: a.color,
        attempt: 0,
        rect: defaultRect(s.sessions.length, s.area),
        minimized: false,
        maximized: false,
        z,
        meta: { phase: 'connecting', cols: 0, rows: 0 },
      }
      return { ...s, z, sessions: [...s.sessions, session], focusedId: session.id }
    }
    case 'close': {
      const sessions = s.sessions.filter((x) => x.id !== a.id)
      const focusedId = s.focusedId === a.id ? topVisible(sessions) : s.focusedId
      return { ...s, sessions, focusedId }
    }
    case 'focus':
    case 'restore': {
      if (a.type === 'focus' && s.focusedId === a.id) return s
      const z = s.z + 1
      return { ...s, z, focusedId: a.id, sessions: update(s, a.id, (x) => ({ ...x, z, minimized: false })) }
    }
    case 'setRect':
      return { ...s, sessions: update(s, a.id, (x) => ({ ...x, maximized: false, rect: clampRect(a.rect, s.area) })) }
    case 'snap':
      return {
        ...s,
        sessions: update(s, a.id, (x) =>
          a.rect === 'max' ? { ...x, maximized: true } : { ...x, maximized: false, rect: clampRect(a.rect, s.area) },
        ),
      }
    case 'minimize': {
      const sessions = update(s, a.id, (x) => ({ ...x, minimized: true }))
      return { ...s, sessions, focusedId: s.focusedId === a.id ? topVisible(sessions, a.id) : s.focusedId }
    }
    case 'toggleMaximize':
      return { ...s, sessions: update(s, a.id, (x) => ({ ...x, maximized: !x.maximized })) }
    case 'reconnect':
      return {
        ...s,
        sessions: update(s, a.id, (x) => ({
          ...x,
          target: a.target ?? x.target,
          attempt: x.attempt + 1,
          meta: { phase: 'connecting', cols: x.meta.cols, rows: x.meta.rows },
        })),
      }
    case 'meta':
      return { ...s, sessions: update(s, a.id, (x) => ({ ...x, meta: { ...x.meta, ...a.meta } })) }
    case 'arrange': {
      const vis = s.sessions.filter((x) => !x.minimized).sort((p, q) => p.z - q.z)
      if (!vis.length) return s
      const { w: W, h: H } = s.area
      const rects = new Map<string, Rect>()
      if (a.mode === 'tile') {
        const cols = Math.ceil(Math.sqrt(vis.length))
        const rows = Math.ceil(vis.length / cols)
        const cw = (W - GAP * (cols + 1)) / cols
        const ch = (H - GAP * (rows + 1)) / rows
        vis.forEach((x, i) => {
          const c = i % cols
          const r = Math.floor(i / cols)
          rects.set(x.id, { x: GAP + c * (cw + GAP), y: GAP + r * (ch + GAP), w: cw, h: ch })
        })
      } else {
        vis.forEach((x, i) => rects.set(x.id, defaultRect(i, s.area)))
      }
      return {
        ...s,
        sessions: s.sessions.map((x) => (rects.has(x.id) ? { ...x, maximized: false, rect: rects.get(x.id)! } : x)),
      }
    }
    case 'area': {
      if (a.w <= 0 || a.h <= 0 || (a.w === s.area.w && a.h === s.area.h)) return s
      const area = { w: a.w, h: a.h }
      return { ...s, area, sessions: s.sessions.map((x) => ({ ...x, rect: clampRect(x.rect, area) })) }
    }
  }
}

export const sessionLabel = (t: Target) => `${t.user}@${t.host}${t.port === 22 ? '' : ':' + t.port}`
