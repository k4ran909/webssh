import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react'
import type { Auth, Target } from './lib/protocol'
import type { SignedIn } from './lib/api'
import { credentialCache, credKey, liveSessions, readySessions, sendText } from './lib/live'
import { getLastTarget, saveLastTarget, savePrefs, type Prefs } from './lib/storage'
import { HOST_COLORS, hasCredentials, useVault, vault, type HostItem, type SavedHost, type Snippet } from './lib/vault'
import { initialWorkspace, sessionLabel, workspaceReducer, type SessionMeta } from './lib/workspace'
import { AccountDialog } from './components/AccountDialog'
import { CommandPalette, type PaletteItem } from './components/CommandPalette'
import { ConnectForm, type ConnectInitial, type ConnectResult } from './components/ConnectForm'
import { Dock } from './components/Dock'
import { Popover } from './components/Popover'
import { Sidebar } from './components/Sidebar'
import { SnippetsPanel } from './components/SnippetsPanel'
import { useTheme } from './components/ThemeToggle'
import { Workspace } from './components/Workspace'
import { Icon, Logo } from './components/icons'

type Dialog =
  | { purpose: 'connect'; title: string; initial: ConnectInitial; offerSave: boolean; sessionId?: string; hostId?: string }
  | { purpose: 'save'; title: string; initial: ConnectInitial; hostId?: string }

function useMediaQuery(q: string) {
  const [match, setMatch] = useState(() => matchMedia(q).matches)
  useEffect(() => {
    const m = matchMedia(q)
    const on = () => setMatch(m.matches)
    m.addEventListener('change', on)
    return () => m.removeEventListener('change', on)
  }, [q])
  return match
}

const sameEndpoint = (h: { host: string; port: number; user: string }, t: { host: string; port: number; user: string }) =>
  h.host.toLowerCase() === t.host.toLowerCase() && h.port === t.port && h.user === t.user

const hostAuth = (h: HostItem): Auth | undefined =>
  !hasCredentials(h) ? undefined
  : h.method === 'password' ? { type: 'password', password: h.password! }
  : { type: 'key', key: h.key!, passphrase: h.passphrase }

const report = (e: unknown) => console.error('vault write failed', e)

interface Props {
  session: SignedIn
  locked: boolean
  prefs: Prefs
  setPrefs: (f: (p: Prefs) => Prefs) => void
  onSession: (s: SignedIn) => void
  onLock: () => void
  onLogout: () => void
}

export function App({ session, locked, prefs, setPrefs, onSession, onLock, onLogout }: Props) {
  const [ws, dispatch] = useReducer(workspaceReducer, initialWorkspace)
  const wsRef = useRef(ws)
  wsRef.current = ws

  const { hosts, snippets, error: syncError } = useVault()
  const hostsRef = useRef(hosts)
  hostsRef.current = hosts
  const [broadcast, setBroadcast] = useState(false)
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [account, setAccount] = useState(false)
  const [palette, setPalette] = useState(false)
  const [pop, setPop] = useState<'snippets' | 'settings' | null>(null)
  const theme = useTheme()
  const narrow = useMediaQuery('(max-width: 760px)')
  const layout = narrow ? 'tabs' : prefs.layout
  const showSidebar = prefs.sidebar && !narrow
  const [mobileSidebar, setMobileSidebar] = useState(false)

  useEffect(() => savePrefs(prefs), [prefs])
  const setPref = useCallback(<K extends keyof Prefs>(k: K, v: Prefs[K]) => setPrefs((p) => ({ ...p, [k]: v })), [setPrefs])

  // ── sessions ──
  const open = useCallback((target: Target, title?: string, color?: string) => {
    credentialCache.set(credKey(target.host, target.port, target.user), target.auth)
    const saved = hostsRef.current.find((h) => sameEndpoint(h, target))
    dispatch({
      type: 'open',
      target,
      title: title ?? (saved?.label || sessionLabel(target)),
      color: color ?? saved?.color ?? HOST_COLORS[0],
    })
  }, [])

  const newConnection = useCallback(() => {
    setDialog({ purpose: 'connect', title: 'New connection', initial: { ...getLastTarget() }, offerSave: true })
  }, [])

  const openHost = useCallback((h: SavedHost) => {
    setMobileSidebar(false)
    const auth = hostAuth(h) ?? credentialCache.get(credKey(h.host, h.port, h.user))
    if (auth) return open({ host: h.host, port: h.port, user: h.user, auth }, h.label || undefined, h.color)
    setDialog({ purpose: 'connect', title: `Connect to ${h.label || h.host}`, initial: h, offerSave: false, hostId: h.id })
  }, [open])

  const onMeta = useCallback((id: string, meta: Partial<SessionMeta>) => dispatch({ type: 'meta', id, meta }), [])
  const onClose = useCallback((id: string) => dispatch({ type: 'close', id }), [])
  const onReconnect = useCallback((id: string) => dispatch({ type: 'reconnect', id }), [])
  const onDuplicate = useCallback((id: string) => {
    const s = wsRef.current.sessions.find((x) => x.id === id)
    if (s) open(s.target, s.title, s.color)
  }, [open])
  const onSearch = useCallback((id: string) => liveSessions.get(id)?.openSearch(), [])
  const onEdit = useCallback((id: string) => {
    const s = wsRef.current.sessions.find((x) => x.id === id)
    if (!s) return
    const { host, port, user, auth } = s.target
    setDialog({ purpose: 'connect', title: 'Edit connection', initial: { host, port, user, method: auth.type }, offerSave: false, sessionId: id })
  }, [])

  const upsertHost = (r: ConnectResult, hostId?: string) => {
    const existing = hostsRef.current.find((h) => h.id === hostId) ?? hostsRef.current.find((h) => sameEndpoint(h, r))
    const item: HostItem = {
      kind: 'host', label: r.label || existing?.label || '', color: r.color,
      host: r.host, port: r.port, user: r.user, method: r.method,
    }
    if (r.storeCreds && r.auth) {
      if (r.auth.type === 'password') item.password = r.auth.password
      else Object.assign(item, { key: r.auth.key, passphrase: r.auth.passphrase })
    } else if (existing && !r.clearCreds && existing.method === r.method) {
      Object.assign(item, { password: existing.password, key: existing.key, passphrase: existing.passphrase })
    }
    vault.put(item, existing?.id).catch(report)
  }

  const submitDialog = (r: ConnectResult) => {
    if (!dialog) return
    if (dialog.purpose === 'save') {
      upsertHost(r, dialog.hostId)
      setDialog(null)
      return
    }
    const target: Target = { host: r.host, port: r.port, user: r.user, auth: r.auth! }
    saveLastTarget({ host: r.host, port: r.port, user: r.user, method: r.method })
    if (r.save) upsertHost(r)
    if (dialog.sessionId) {
      credentialCache.set(credKey(target.host, target.port, target.user), target.auth)
      dispatch({ type: 'reconnect', id: dialog.sessionId, target })
      dispatch({ type: 'restore', id: dialog.sessionId })
    } else {
      open(target, r.save && r.label ? r.label : undefined, r.save ? r.color : undefined)
    }
    setDialog(null)
  }

  const editHost = useCallback((h: SavedHost) => {
    setDialog({ purpose: 'save', title: 'Edit host', initial: { ...h, hasCreds: hasCredentials(h) }, hostId: h.id })
  }, [])

  const runSnippet = useCallback((s: Snippet) => {
    const n = sendText(s.command + '\r', wsRef.current.focusedId, broadcast)
    if (n) liveSessions.get(wsRef.current.focusedId ?? '')?.focus()
    setPop(null)
  }, [broadcast])

  const bumpFont = useCallback((d: number) => setPrefs((p) => ({ ...p, fontSize: Math.min(22, Math.max(10, p.fontSize + d)) })), [setPrefs])

  // ── global shortcuts ──
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const k = e.key.toLowerCase()
      const inTerminal = !!(e.target as HTMLElement)?.closest?.('.xterm')
      if ((e.ctrlKey || e.metaKey) && k === 'k' && (e.shiftKey || !inTerminal)) {
        e.preventDefault()
        setPalette((v) => !v)
      } else if (e.ctrlKey && e.shiftKey && k === 'f') {
        e.preventDefault()
        const id = wsRef.current.focusedId
        if (id) liveSessions.get(id)?.openSearch()
      } else if (e.altKey && !e.ctrlKey && !e.metaKey && /^[1-9]$/.test(e.key)) {
        const s = wsRef.current.sessions[Number(e.key) - 1]
        if (s) {
          e.preventDefault()
          dispatch({ type: 'restore', id: s.id })
          setTimeout(() => liveSessions.get(s.id)?.focus(), 0)
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // Keep keyboard focus with the focused window's terminal (also after unlocking).
  useEffect(() => {
    if (ws.focusedId && !dialog && !palette && !account && !locked) liveSessions.get(ws.focusedId)?.focus()
  }, [ws.focusedId, dialog, palette, account, locked])

  // Nothing left to broadcast to.
  useEffect(() => {
    if (broadcast && ws.sessions.length === 0) setBroadcast(false)
  }, [broadcast, ws.sessions.length])

  const liveCount = ws.sessions.filter((s) => s.meta.phase === 'ready').length

  // ── palette ──
  const paletteItems = useMemo<PaletteItem[]>(() => {
    const dot = (c: string) => <span className="swatch" style={{ background: c }} />
    return [
      ...ws.sessions.map((s, i) => ({
        id: 's' + s.id, section: 'Open sessions', label: s.title, hint: i < 9 ? `Alt+${i + 1}` : undefined, icon: dot(s.color),
        run: () => { dispatch({ type: 'restore', id: s.id }); setTimeout(() => liveSessions.get(s.id)?.focus(), 0) },
      })),
      ...hosts.map((h) => ({
        id: 'h' + h.id, section: 'Connect', label: h.label || h.host, hint: `${h.user}@${h.host}${h.port === 22 ? '' : ':' + h.port}`, icon: dot(h.color),
        run: () => openHost(h),
      })),
      ...snippets.map((s) => ({
        id: 'n' + s.id, section: broadcast ? 'Run snippet (all sessions)' : 'Run snippet', label: s.name, hint: s.command, icon: <Icon name="bolt" size={14} />,
        run: () => runSnippet(s),
      })),
      { id: 'a-new', section: 'Actions', label: 'New connection', icon: <Icon name="plus" size={14} />, run: newConnection },
      { id: 'a-host', section: 'Actions', label: 'Add saved host', icon: <Icon name="server" size={14} />, run: () => setDialog({ purpose: 'save', title: 'Add host', initial: {} }) },
      { id: 'a-layout', section: 'Actions', label: layout === 'windows' ? 'Switch to tabs' : 'Switch to windows', icon: <Icon name={layout === 'windows' ? 'tabs' : 'windows'} size={14} />, run: () => setPref('layout', layout === 'windows' ? 'tabs' : 'windows') },
      { id: 'a-tile', section: 'Actions', label: 'Tile windows', icon: <Icon name="tile" size={14} />, run: () => { setPref('layout', 'windows'); dispatch({ type: 'arrange', mode: 'tile' }) } },
      { id: 'a-cascade', section: 'Actions', label: 'Cascade windows', icon: <Icon name="cascade" size={14} />, run: () => { setPref('layout', 'windows'); dispatch({ type: 'arrange', mode: 'cascade' }) } },
      { id: 'a-bc', section: 'Actions', label: broadcast ? 'Stop broadcasting input' : 'Broadcast input to all sessions', icon: <Icon name="broadcast" size={14} />, run: () => setBroadcast((b) => !b) },
      { id: 'a-find', section: 'Actions', label: 'Find in terminal', hint: 'Ctrl+Shift+F', icon: <Icon name="search" size={14} />, run: () => ws.focusedId && liveSessions.get(ws.focusedId)?.openSearch() },
      { id: 'a-font+', section: 'Actions', label: 'Increase font size', icon: <Icon name="plus" size={14} />, run: () => bumpFont(1) },
      { id: 'a-font-', section: 'Actions', label: 'Decrease font size', icon: <Icon name="minus" size={14} />, run: () => bumpFont(-1) },
      { id: 'a-side', section: 'Actions', label: prefs.sidebar ? 'Hide sidebar' : 'Show sidebar', icon: <Icon name="sidebar" size={14} />, run: () => setPref('sidebar', !prefs.sidebar) },
      { id: 'a-theme', section: 'Actions', label: theme.resolved === 'dark' ? 'Light theme' : 'Dark theme', icon: <Icon name={theme.resolved === 'dark' ? 'sun' : 'moon'} size={14} />, run: () => theme.setPref(theme.resolved === 'dark' ? 'light' : 'dark') },
      { id: 'a-account', section: 'Account', label: 'Account & security', hint: session.user.email, icon: <Icon name="user" size={14} />, run: () => setAccount(true) },
      { id: 'a-lock', section: 'Account', label: 'Lock vault', icon: <Icon name="lock" size={14} />, run: onLock },
      { id: 'a-logout', section: 'Account', label: 'Sign out', icon: <Icon name="logout" size={14} />, run: onLogout },
    ]
  }, [ws.sessions, ws.focusedId, hosts, snippets, broadcast, layout, prefs.sidebar, theme, session.user.email, openHost, runSnippet, newConnection, setPref, bumpFont, onLock, onLogout])

  return (
    <div className="app">
      <header className="topbar">
        <button
          className="icon-btn"
          title="Toggle sidebar"
          aria-label="Toggle sidebar"
          onClick={() => (narrow ? setMobileSidebar((v) => !v) : setPref('sidebar', !prefs.sidebar))}
        >
          <Icon name="sidebar" />
        </button>
        <Logo size={20} />
        <span className="wordmark">webssh</span>
        <button className="cmd-btn" onClick={() => setPalette(true)}>
          <Icon name="search" size={14} />
          <span>Search or run…</span>
          <kbd>Ctrl ⇧ K</kbd>
        </button>
        <span className="spacer" />

        {!narrow && (
          <div className="seg mini" role="tablist" aria-label="Layout">
            <button role="tab" aria-selected={layout === 'windows'} className={layout === 'windows' ? 'on' : ''} title="Windows" onClick={() => setPref('layout', 'windows')}><Icon name="windows" size={14} /></button>
            <button role="tab" aria-selected={layout === 'tabs'} className={layout === 'tabs' ? 'on' : ''} title="Tabs" onClick={() => setPref('layout', 'tabs')}><Icon name="tabs" size={14} /></button>
          </div>
        )}
        {!narrow && layout === 'windows' && (
          <>
            <button className="icon-btn" title="Tile windows" aria-label="Tile windows" disabled={!ws.sessions.length} onClick={() => dispatch({ type: 'arrange', mode: 'tile' })}><Icon name="tile" /></button>
            <button className="icon-btn" title="Cascade windows" aria-label="Cascade windows" disabled={!ws.sessions.length} onClick={() => dispatch({ type: 'arrange', mode: 'cascade' })}><Icon name="cascade" /></button>
          </>
        )}
        <button
          className={`icon-btn${broadcast ? ' warn-on' : ''}`}
          title={broadcast ? 'Stop broadcasting' : 'Broadcast input to all sessions'}
          aria-label="Broadcast input"
          aria-pressed={broadcast}
          disabled={!ws.sessions.length}
          onClick={() => setBroadcast((b) => !b)}
        >
          <Icon name="broadcast" />
        </button>
        <span className="anchor">
          <button className="icon-btn" data-popover-anchor title="Snippets" aria-label="Snippets" aria-expanded={pop === 'snippets'} onClick={() => setPop((p) => (p === 'snippets' ? null : 'snippets'))}><Icon name="bolt" /></button>
          {pop === 'snippets' && (
            <Popover onClose={() => setPop(null)} className="right">
              <SnippetsPanel
                snippets={snippets}
                canRun={broadcast ? readySessions().length > 0 : !!ws.focusedId && !!liveSessions.get(ws.focusedId)?.isReady()}
                broadcast={broadcast}
                onRun={runSnippet}
                onAdd={(name, command) => vault.put({ kind: 'snippet', name, command }).catch(report)}
                onDelete={(id) => vault.remove(id).catch(report)}
              />
            </Popover>
          )}
        </span>
        <span className="anchor">
          <button className="icon-btn" data-popover-anchor title="Settings" aria-label="Settings" aria-expanded={pop === 'settings'} onClick={() => setPop((p) => (p === 'settings' ? null : 'settings'))}><Icon name="gear" /></button>
          {pop === 'settings' && (
            <Popover onClose={() => setPop(null)} className="right settings">
              <div className="pop-head"><span>Appearance</span></div>
              <div className="set-row">
                <span>Theme</span>
                <div className="seg mini">
                  {(['system', 'light', 'dark'] as const).map((t) => (
                    <button key={t} className={theme.pref === t ? 'on' : ''} onClick={() => theme.setPref(t)}>{t[0].toUpperCase() + t.slice(1)}</button>
                  ))}
                </div>
              </div>
              <div className="set-row">
                <span>Terminal font</span>
                <div className="stepper">
                  <button aria-label="Smaller" onClick={() => bumpFont(-1)}><Icon name="minus" size={14} /></button>
                  <span className="mono">{prefs.fontSize}px</span>
                  <button aria-label="Larger" onClick={() => bumpFont(1)}><Icon name="plus" size={14} /></button>
                </div>
              </div>
              <div className="pop-sep" />
              <button className="pop-item" onClick={() => { setPop(null); setAccount(true) }}>
                <Icon name="user" size={14} /><span>Account &amp; security</span><span className="pop-hint">{session.user.email}</span>
              </button>
            </Popover>
          )}
        </span>
        <button className="icon-btn" title="Lock vault" aria-label="Lock vault" onClick={onLock}><Icon name="lock" /></button>
        <button className="btn primary" onClick={newConnection}><Icon name="plus" size={14} /><span className="hide-sm">New</span></button>
      </header>

      <div className="body">
        {(showSidebar || mobileSidebar) && (
          <>
            {mobileSidebar && <div className="scrim side-scrim" onClick={() => setMobileSidebar(false)} />}
            <Sidebar
              hosts={hosts}
              sessions={ws.sessions}
              syncError={syncError}
              onOpen={openHost}
              onAdd={() => setDialog({ purpose: 'save', title: 'Add host', initial: {} })}
              onEdit={editHost}
              onDelete={(id) => vault.remove(id).catch(report)}
            />
          </>
        )}
        <div className="main-col">
          {broadcast && (
            <div className="broadcast-bar" role="status">
              <Icon name="broadcast" size={14} />
              <span>Broadcasting keystrokes to <b>{liveCount}</b> connected session{liveCount === 1 ? '' : 's'}</span>
              <button className="btn" onClick={() => setBroadcast(false)}>Stop</button>
            </div>
          )}
          <Workspace
            ws={ws}
            layout={layout}
            broadcast={broadcast}
            fontSize={prefs.fontSize}
            hosts={hosts}
            dispatch={dispatch}
            onMeta={onMeta}
            onClose={onClose}
            onReconnect={onReconnect}
            onEdit={onEdit}
            onDuplicate={onDuplicate}
            onSearch={onSearch}
            onNew={newConnection}
            onOpenHost={openHost}
          />
          <Dock ws={ws} showTasks={layout === 'windows'} dispatch={dispatch} />
        </div>
      </div>

      {dialog && (
        <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && setDialog(null)}>
          <div className="modal" role="dialog" aria-label={dialog.title} onKeyDown={(e) => e.key === 'Escape' && setDialog(null)}>
            <ConnectForm
              title={dialog.title}
              purpose={dialog.purpose}
              initial={dialog.initial}
              offerSave={dialog.purpose === 'connect' && dialog.offerSave}
              onSubmit={submitDialog}
              onCancel={() => setDialog(null)}
            />
          </div>
        </div>
      )}

      {account && (
        <AccountDialog
          session={session}
          autoLock={prefs.autoLock}
          onAutoLock={(m) => setPref('autoLock', m)}
          onSession={onSession}
          onLock={() => { setAccount(false); onLock() }}
          onLogout={onLogout}
          onClose={() => setAccount(false)}
        />
      )}

      {palette && <CommandPalette items={paletteItems} onClose={() => setPalette(false)} />}
    </div>
  )
}
