import { lazy, Suspense, useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react'
import type { Auth, Target } from './lib/protocol'
import type { SignedIn } from './lib/api'
import { credentialCache, credKey, liveSessions, readySessions, sendText } from './lib/live'
import { credentialsFor, resolveHost, type VaultView } from './lib/resolve'
import { getLastTarget, saveLastTarget, savePrefs, type Prefs } from './lib/storage'
import { HOST_COLORS, useVault, vault, type HostItem, type SavedHost, type Snippet } from './lib/vault'
import { initialWorkspace, sessionLabel, workspaceReducer, type SessionMeta } from './lib/workspace'
import { AccountDialog } from './components/AccountDialog'
import { CommandPalette, type PaletteItem } from './components/CommandPalette'
import { ConnectForm, type ConnectInitial, type ConnectResult } from './components/ConnectForm'
import { Dock } from './components/Dock'
import { ImportConfig } from './components/ImportConfig'
import { Keychain } from './components/Keychain'
import { Popover } from './components/Popover'
import { Sidebar } from './components/Sidebar'
import { SnippetsPanel } from './components/SnippetsPanel'
import { useTheme } from './components/ThemeToggle'
import { Workspace } from './components/Workspace'
import type { WindowAction } from './components/WindowFrame'
import { ForwardDialog, ForwardsPanel, useForwards } from './components/Forwarding'
import { KeysBar } from './components/KeysBar'
import { downloadText, parseCast, type Cast } from './lib/cast'
import { Icon, Logo } from './components/icons'

type Dialog =
  | { purpose: 'connect'; title: string; initial: ConnectInitial; offerSave: boolean; sessionId?: string; hostId?: string; notice?: string }
  | { purpose: 'save'; title: string; initial: ConnectInitial; hostId?: string; notice?: string }

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

// xterm-based; loaded only when a recording is opened.
const CastPlayer = lazy(() => import('./components/CastPlayer'))

const hostInitial = (h: SavedHost): ConnectInitial => ({ ...h, hasCreds: !!(h.password || h.key) })

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

  const { hosts, snippets, keys, identities, error: syncError } = useVault()
  const view: VaultView = useMemo(() => ({ hosts, snippets, keys, identities }), [hosts, snippets, keys, identities])
  const viewRef = useRef(view)
  viewRef.current = view
  const hostsRef = useRef(hosts)
  hostsRef.current = hosts
  const [broadcast, setBroadcast] = useState(false)
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [account, setAccount] = useState(false)
  const [keychain, setKeychain] = useState(false)
  const [importing, setImporting] = useState(false)
  const [notice, setNotice] = useState('')
  const [palette, setPalette] = useState(false)
  const [pop, setPop] = useState<'snippets' | 'settings' | 'forwards' | null>(null)
  const [forwarding, setForwarding] = useState<string | null>(null)
  const [playback, setPlayback] = useState<{ cast: Cast; name: string } | null>(null)
  const castInput = useRef<HTMLInputElement>(null)
  const fwd = useForwards()
  const theme = useTheme()
  const narrow = useMediaQuery('(max-width: 760px)')
  const coarse = useMediaQuery('(pointer: coarse)')
  const layout = narrow ? 'tabs' : prefs.layout
  const showSidebar = prefs.sidebar && !narrow
  const layoutRef = useRef(layout)
  layoutRef.current = layout
  const [mobileSidebar, setMobileSidebar] = useState(false)

  useEffect(() => savePrefs(prefs), [prefs])
  const setPref = useCallback(<K extends keyof Prefs>(k: K, v: Prefs[K]) => setPrefs((p) => ({ ...p, [k]: v })), [setPrefs])

  // ── sessions ──
  const open = useCallback((target: Target, title?: string, color?: string, split?: { id: string; dir: 'right' | 'down' }) => {
    credentialCache.set(credKey(target.host, target.port, target.user), target.auth)
    const saved = hostsRef.current.find((h) => sameEndpoint(h, target))
    dispatch({
      type: 'open',
      target,
      title: title ?? (saved?.label || sessionLabel(target)),
      color: color ?? saved?.color ?? HOST_COLORS[0],
      split,
    })
  }, [])

  const newConnection = useCallback(() => {
    setDialog({ purpose: 'connect', title: 'New connection', initial: { ...getLastTarget() }, offerSave: true })
  }, [])

  const openHost = useCallback((h: SavedHost) => {
    setMobileSidebar(false)
    const v = viewRef.current
    const cached = credentialCache.get(credKey(h.host, h.port, h.user))
    const r = resolveHost(h, v, credentialsFor(h, v) ? undefined : cached && { user: h.user, auth: cached })
    if (r.ok) return open(r.target, r.title, h.color)
    if (r.reason === 'credentials' && r.host.id === h.id) {
      return setDialog({ purpose: 'connect', title: `Connect to ${h.label || h.host}`, initial: hostInitial(h), offerSave: false, hostId: h.id })
    }
    // A jump host (or keychain entry) needs fixing first.
    setDialog({ purpose: 'save', title: `Edit ${r.host.label || r.host.host}`, initial: hostInitial(r.host), hostId: r.host.id, notice: r.message })
  }, [open])

  const onMeta = useCallback((id: string, meta: Partial<SessionMeta>) => dispatch({ type: 'meta', id, meta }), [])
  const onClose = useCallback((id: string) => dispatch({ type: 'close', id }), [])
  const onReconnect = useCallback((id: string) => dispatch({ type: 'reconnect', id }), [])
  const onAction = useCallback((id: string, action: WindowAction) => {
    const s = wsRef.current.sessions.find((x) => x.id === id)
    if (!s) return
    switch (action) {
      case 'duplicate':
        return open(s.target, s.title, s.color)
      case 'split-right':
      case 'split-down':
        if (layoutRef.current === 'tabs') setPref('layout', 'windows')
        return open(s.target, s.title, s.color, { id, dir: action === 'split-right' ? 'right' : 'down' })
      case 'forward':
        return setForwarding(id)
      case 'record': {
        const live = liveSessions.get(id)
        if (!live) return
        if (!s.meta.recording) return live.startRecording()
        const cast = live.stopRecording()
        if (cast) downloadText(`${s.title.replace(/[^\w.-]+/g, '_')}-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.cast`, cast)
        setNotice('Recording saved to your downloads. Play it back from the command palette.')
      }
    }
  }, [open, setPref])
  const onSearch = useCallback((id: string) => liveSessions.get(id)?.openSearch(), [])
  const onEdit = useCallback((id: string) => {
    const s = wsRef.current.sessions.find((x) => x.id === id)
    if (!s) return
    const { host, port, user, auth } = s.target
    const saved = hostsRef.current.find((h) => sameEndpoint(h, s.target))
    setDialog({
      purpose: 'connect', title: 'Edit connection', offerSave: false, sessionId: id, hostId: saved?.id,
      initial: { ...(saved ? hostInitial(saved) : {}), host, port, user, method: auth.type },
    })
  }, [])

  const upsertHost = (r: ConnectResult, hostId?: string): string => {
    const existing = hostsRef.current.find((h) => h.id === hostId) ?? hostsRef.current.find((h) => sameEndpoint(h, r))
    const item: HostItem = {
      kind: 'host', label: r.label || existing?.label || '', color: r.color,
      host: r.host, port: r.port, user: r.user, method: r.method,
      group: r.group, jumpHostId: r.jumpHostId, startupSnippetId: r.startupSnippetId, agent: r.agent || undefined,
    }
    if (r.ref) {
      if (r.ref.kind === 'identity') item.identityId = r.ref.id
      else item.keyId = r.ref.id
    } else if (r.storeCreds && r.auth) {
      if (r.auth.type === 'password') item.password = r.auth.password
      else Object.assign(item, { key: r.auth.key, passphrase: r.auth.passphrase })
    } else if (existing && !r.clearCreds && existing.method === r.method) {
      Object.assign(item, { password: existing.password, key: existing.key, passphrase: existing.passphrase })
    }
    const id = existing?.id ?? crypto.randomUUID()
    vault.put(item, id).catch(report)
    return id
  }

  /** Credentials from the form: inline, or looked up in the keychain. */
  const formCredentials = (r: ConnectResult): { user: string; auth: Auth } | undefined => {
    if (r.auth) return { user: r.user, auth: r.auth }
    if (!r.ref) return undefined
    const v = viewRef.current
    if (r.ref.kind === 'identity') {
      const id = v.identities.find((i) => i.id === r.ref!.id)
      const k = v.keys.find((x) => x.id === id?.keyId)
      if (id && k) return { user: id.username, auth: { type: 'key', key: k.privateKey, passphrase: k.passphrase } }
      if (id?.password) return { user: id.username, auth: { type: 'password', password: id.password } }
      return undefined
    }
    const k = v.keys.find((x) => x.id === r.ref!.id)
    return k ? { user: r.user, auth: { type: 'key', key: k.privateKey, passphrase: k.passphrase } } : undefined
  }

  const submitDialog = (r: ConnectResult) => {
    if (!dialog) return
    if (dialog.purpose === 'save') {
      upsertHost(r, dialog.hostId)
      setDialog(null)
      return
    }
    const creds = formCredentials(r)
    if (!creds) return setNotice('That keychain entry has no usable credential.')
    saveLastTarget({ host: r.host, port: r.port, user: r.user, method: r.method })
    const savedId = r.save ? upsertHost(r) : undefined
    // Resolve as a (possibly unsaved) host so jump hosts and run-on-connect apply.
    const existing = hostsRef.current.find((h) => h.id === (dialog.hostId ?? savedId))
    const asHost: SavedHost = {
      ...(existing ?? { kind: 'host', id: savedId ?? 'adhoc', label: r.label, color: r.color, method: r.method }),
      host: r.host, port: r.port, user: creds.user,
      jumpHostId: r.jumpHostId ?? existing?.jumpHostId,
      agent: r.agent ?? existing?.agent,
      startupSnippetId: r.startupSnippetId ?? existing?.startupSnippetId,
    } as SavedHost
    const res = resolveHost(asHost, viewRef.current, creds)
    if (!res.ok) return setNotice(res.message)
    credentialCache.set(credKey(r.host, r.port, creds.user), creds.auth)
    if (dialog.sessionId) {
      dispatch({ type: 'reconnect', id: dialog.sessionId, target: res.target })
      dispatch({ type: 'restore', id: dialog.sessionId })
    } else {
      open(res.target, r.save && r.label ? r.label : existing?.label || undefined, r.save ? r.color : existing?.color)
    }
    setDialog(null)
  }

  const editHost = useCallback((h: SavedHost) => {
    setDialog({ purpose: 'save', title: 'Edit host', initial: hostInitial(h), hostId: h.id })
  }, [])

  useEffect(() => {
    if (!notice) return
    const t = setTimeout(() => setNotice(''), 5000)
    return () => clearTimeout(t)
  }, [notice])

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
      { id: 'a-keychain', section: 'Actions', label: 'Open keychain', hint: 'keys & identities', icon: <Icon name="key" size={14} />, run: () => setKeychain(true) },
      { id: 'a-import', section: 'Actions', label: 'Import ~/.ssh/config', icon: <Icon name="import" size={14} />, run: () => setImporting(true) },
      ...(ws.focusedId ? [{ id: 'a-files', section: 'Actions', label: 'Toggle files (SFTP)', icon: <Icon name="folder" size={14} />, run: () => dispatch({ type: 'toggleFiles', id: ws.focusedId! }) }] : []),
      { id: 'a-play', section: 'Actions', label: 'Play a recording…', hint: '.cast', icon: <Icon name="play" size={14} />, run: () => castInput.current?.click() },
      { id: 'a-fwd', section: 'Actions', label: 'Port forwarding', icon: <Icon name="forward" size={14} />, run: () => { fwd.refresh(); setPop('forwards') } },
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
          <button className="icon-btn" data-popover-anchor title="Port forwarding" aria-label="Port forwarding" aria-expanded={pop === 'forwards'}
            onClick={() => { fwd.refresh(); setPop((p) => (p === 'forwards' ? null : 'forwards')) }}>
            <Icon name="forward" />
            {fwd.forwards.length > 0 && <span className="badge">{fwd.forwards.length}</span>}
          </button>
          {pop === 'forwards' && (
            <Popover onClose={() => setPop(null)} className="right">
              <ForwardsPanel forwards={fwd.forwards} available={fwd.available} sessions={ws.sessions} onChange={fwd.refresh} />
            </Popover>
          )}
        </span>
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
              onKeychain={() => setKeychain(true)}
              onImport={() => setImporting(true)}
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
            onAction={onAction}
            onSearch={onSearch}
            onNew={newConnection}
            onOpenHost={openHost}
          />
          {(narrow || coarse) && ws.focusedId && <KeysBar sessionId={ws.focusedId} />}
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
              choices={view}
              notice={dialog.notice}
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

      {keychain && (
        <Keychain
          keys={keys}
          identities={identities}
          hosts={hosts}
          canInstall={!!ws.focusedId && !!liveSessions.get(ws.focusedId)?.isReady()}
          onInstall={(cmd) => {
            sendText(cmd + '\r', wsRef.current.focusedId, false)
            setKeychain(false)
            setNotice('Installing the public key in the focused session…')
          }}
          onClose={() => setKeychain(false)}
        />
      )}
      {importing && (
        <ImportConfig hosts={hosts} onClose={() => setImporting(false)}
          onDone={(n) => { setImporting(false); setNotice(`Imported ${n} host${n === 1 ? '' : 's'}.`) }} />
      )}

      {forwarding && ws.sessions.some((s) => s.id === forwarding) && (
        <ForwardDialog session={ws.sessions.find((s) => s.id === forwarding)!} onClose={() => setForwarding(null)} onCreated={fwd.refresh} />
      )}
      <input ref={castInput} type="file" accept=".cast,application/x-asciicast,application/json" hidden
        onChange={async (e) => {
          const f = e.target.files?.[0]
          e.target.value = ''
          if (!f) return
          if (f.size > 50 << 20) return setNotice('That recording is too large to play here (over 50 MB).')
          try {
            setPlayback({ cast: parseCast(await f.text()), name: f.name })
          } catch (err) {
            setNotice(err instanceof Error ? err.message : 'Could not read that recording.')
          }
        }} />
      {playback && (
        <Suspense fallback={null}>
          <CastPlayer cast={playback.cast} name={playback.name} onClose={() => setPlayback(null)} />
        </Suspense>
      )}

      {palette && <CommandPalette items={paletteItems} onClose={() => setPalette(false)} />}
      {notice && <div className="notice" role="status">{notice}</div>}
    </div>
  )
}
