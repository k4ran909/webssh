import { useMemo, useRef, useState } from 'react'
import { parseSshConfig } from '../lib/sshconfig'
import { HOST_COLORS, vault, type HostItem, type SavedHost } from '../lib/vault'
import { Icon } from './icons'

const EXAMPLE = `Host bastion
  HostName bastion.example.com
  User ubuntu

Host prod-db
  HostName 10.0.1.12
  User postgres
  ProxyJump bastion`

export function ImportConfig({ hosts, onClose, onDone }: { hosts: SavedHost[]; onClose: () => void; onDone: (n: number) => void }) {
  const [text, setText] = useState('')
  const [group, setGroup] = useState('ssh config')
  const [skip, setSkip] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const parsed = useMemo(() => parseSshConfig(text), [text])

  const existing = (h: { host: string; port: number; user?: string }) =>
    hosts.find((x) => x.host.toLowerCase() === h.host.toLowerCase() && x.port === h.port && (!h.user || x.user === h.user))

  const selected = parsed.hosts.filter((h) => !skip.has(h.alias) && !existing(h))

  const doImport = async () => {
    setBusy(true)
    // Ids first so ProxyJump aliases can point at hosts created in this batch.
    const ids = new Map(selected.map((h) => [h.alias, crypto.randomUUID()]))
    const byAlias = (alias: string) => ids.get(alias) ?? hosts.find((x) => x.label === alias)?.id ?? existing(parsed.hosts.find((p) => p.alias === alias) ?? { host: alias, port: 22 })?.id
    await Promise.all(selected.map((h) => {
      const item: HostItem = {
        kind: 'host', label: h.alias, color: HOST_COLORS[0], host: h.host, port: h.port, user: h.user ?? '',
        method: h.identityFile ? 'key' : 'password', group: group.trim() || undefined,
        jumpHostId: h.proxyJump ? byAlias(h.proxyJump) : undefined,
      }
      return vault.put(item, ids.get(h.alias))
    })).catch((e) => console.error('import failed', e))
    onDone(selected.length)
  }

  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal wide-modal" role="dialog" aria-label="Import ssh config" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="acct-head">
          <div>
            <h2>Import from ~/.ssh/config</h2>
            <p className="pop-sub">Hosts, users, ports and ProxyJump come across. Keys and passwords stay on your machine: add them from the keychain after importing.</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div className="acct-body">
          <div className="stack-form" style={{ paddingTop: 4 }}>
            <textarea className="textarea tall" placeholder={EXAMPLE} aria-label="ssh config" value={text} onChange={(e) => { setText(e.target.value); setSkip(new Set()) }} spellCheck={false} autoFocus />
            <div className="key-actions" style={{ margin: 0 }}>
              <span>Paste your config, or</span>
              <button type="button" className="link" onClick={() => fileRef.current?.click()}>load the file…</button>
              <input ref={fileRef} type="file" hidden onChange={async (e) => { const f = e.target.files?.[0]; if (f && f.size < 1 << 20) setText(await f.text()) }} />
            </div>
          </div>

          {parsed.hosts.length > 0 && (
            <div className="acct-sec">
              <h3>{parsed.hosts.length} host{parsed.hosts.length === 1 ? '' : 's'} found</h3>
              <div className="import-list">
                {parsed.hosts.map((h) => {
                  const dup = existing(h)
                  return (
                    <label key={h.alias} className={`import-row${dup ? ' dup' : ''}`}>
                      <input type="checkbox" disabled={!!dup} checked={!dup && !skip.has(h.alias)}
                        onChange={(e) => setSkip((s) => { const n = new Set(s); if (e.target.checked) n.delete(h.alias); else n.add(h.alias); return n })} />
                      <span className="kc-main">
                        <span className="kc-title">{h.alias}</span>
                        <span className="kc-sub mono">
                          {h.user ? h.user + '@' : ''}{h.host}{h.port === 22 ? '' : ':' + h.port}
                          {h.proxyJump && <> · via {h.proxyJump}</>}
                          {h.identityFile && <> · key {h.identityFile.split(/[\\/]/).pop()}</>}
                          {dup && <> · already saved</>}
                        </span>
                      </span>
                    </label>
                  )
                })}
              </div>
              {parsed.warnings.map((w, i) => <p key={i} className="pop-sub">⚠ {w}</p>)}
              <div className="set-row" style={{ paddingTop: 12 }}>
                <span>Put them in group</span>
                <input className="input" style={{ width: 180, height: 32 }} value={group} onChange={(e) => setGroup(e.target.value)} aria-label="Group" />
              </div>
            </div>
          )}
          {text.trim() && !parsed.hosts.length && <p className="pop-sub" style={{ marginTop: 12 }}>No concrete Host entries found (wildcard patterns are skipped).</p>}
        </div>
        <div className="form-actions" style={{ marginTop: 16 }}>
          <button className="btn lg" onClick={onClose}>Cancel</button>
          <button className="btn primary lg" disabled={!selected.length || busy} onClick={doImport}>Import {selected.length || ''}</button>
        </div>
      </div>
    </div>
  )
}
