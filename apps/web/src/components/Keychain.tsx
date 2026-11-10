import { useEffect, useRef, useState } from 'react'
import { ed25519Supported, generateEd25519, inspectPrivateKey, installCommand } from '../lib/sshkey'
import { vault, type Identity, type SavedHost, type SavedKey } from '../lib/vault'
import { Icon } from './icons'

interface Props {
  keys: SavedKey[]
  identities: Identity[]
  hosts: SavedHost[]
  /** A connected session has focus, so "install" can type into it. */
  canInstall: boolean
  onInstall: (command: string) => void
  onClose: () => void
}

const message = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')
const report = (e: unknown) => console.error('vault write failed', e)
const shortFp = (fp?: string) => (fp ? fp.replace('SHA256:', '').slice(0, 16) + '…' : 'public key unknown')

export function Keychain({ keys, identities, hosts, canInstall, onInstall, onClose }: Props) {
  const [tab, setTab] = useState<'keys' | 'identities'>('keys')
  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal wide-modal" role="dialog" aria-label="Keychain" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="acct-head">
          <div>
            <h2>Keychain</h2>
            <p className="pop-sub">Keys and logins you can reuse across hosts. End-to-end encrypted.</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div className="seg acct-tabs" role="tablist">
          <button role="tab" aria-selected={tab === 'keys'} className={tab === 'keys' ? 'on' : ''} onClick={() => setTab('keys')}>Keys · {keys.length}</button>
          <button role="tab" aria-selected={tab === 'identities'} className={tab === 'identities' ? 'on' : ''} onClick={() => setTab('identities')}>Identities · {identities.length}</button>
        </div>
        <div className="acct-body">
          {tab === 'keys'
            ? <Keys keys={keys} identities={identities} hosts={hosts} canInstall={canInstall} onInstall={onInstall} />
            : <Identities keys={keys} identities={identities} hosts={hosts} />}
        </div>
      </div>
    </div>
  )
}

function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <button className="btn" onClick={() => navigator.clipboard?.writeText(text).then(() => { setDone(true); setTimeout(() => setDone(false), 1500) }).catch(() => {})}>
      <Icon name="copy" size={13} /> {done ? 'Copied' : label}
    </button>
  )
}

function Keys({ keys, identities, hosts, canInstall, onInstall }: Omit<Props, 'onClose'>) {
  const [mode, setMode] = useState<'none' | 'generate' | 'import'>(keys.length ? 'none' : 'generate')
  const [fresh, setFresh] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<string | null>(null)
  const usedBy = (id: string) => hosts.filter((h) => h.keyId === id).length + identities.filter((i) => i.keyId === id).length

  return (
    <>
      <div className="acct-sec">
        {keys.length === 0 && mode === 'none' && <p className="pop-sub">No keys yet.</p>}
        {keys.map((k) => (
          <div key={k.id} className={`kc-row${fresh === k.id ? ' fresh' : ''}`}>
            <span className="kc-icon"><Icon name="key" size={15} /></span>
            <div className="kc-main">
              <div className="kc-title">{k.label} <span className="tag">{k.type.replace('ssh-', '').toUpperCase()}</span></div>
              <div className="kc-sub mono" title={k.fingerprint}>{shortFp(k.fingerprint)}{usedBy(k.id) ? ` · used by ${usedBy(k.id)}` : ''}</div>
              {fresh === k.id && k.publicKey && (
                <div className="pubkey">
                  <p className="pop-sub">Add this public key to <code>~/.ssh/authorized_keys</code> on your servers:</p>
                  <code className="secret">{k.publicKey}</code>
                </div>
              )}
            </div>
            <div className="kc-acts">
              {k.publicKey && <CopyButton text={k.publicKey} label="Public key" />}
              {k.publicKey && (
                <button className="btn" disabled={!canInstall} title={canInstall ? 'Append to authorized_keys on the focused session' : 'Connect a session first'}
                  onClick={() => onInstall(installCommand(k.publicKey!))}>
                  <Icon name="terminal" size={13} /> Install
                </button>
              )}
              {confirm === k.id ? (
                <>
                  <button className="btn danger" onClick={() => { vault.remove(k.id).catch(report); setConfirm(null) }}>Delete</button>
                  <button className="btn" onClick={() => setConfirm(null)}>Keep</button>
                </>
              ) : (
                <button className="icon-btn sm" title="Delete key" aria-label={`Delete ${k.label}`} onClick={() => setConfirm(k.id)}><Icon name="trash" size={14} /></button>
              )}
            </div>
            {confirm === k.id && usedBy(k.id) > 0 && <p className="warn-note kc-warn"><Icon name="alert" size={13} /> {usedBy(k.id)} host(s) or identities use this key and will need new credentials.</p>}
          </div>
        ))}
      </div>
      <div className="acct-sec">
        {mode === 'none' ? (
          <div className="acts">
            <button className="btn primary" onClick={() => setMode('generate')}><Icon name="plus" size={13} /> Generate key</button>
            <button className="btn" onClick={() => setMode('import')}><Icon name="import" size={13} /> Import key</button>
          </div>
        ) : mode === 'generate' ? (
          <GenerateKey onDone={(id) => { setMode('none'); if (id) setFresh(id) }} />
        ) : (
          <ImportKey onDone={(id) => { setMode('none'); if (id) setFresh(id) }} />
        )}
      </div>
    </>
  )
}

function GenerateKey({ onDone }: { onDone: (id?: string) => void }) {
  const [label, setLabel] = useState('')
  const [comment, setComment] = useState('webssh')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [supported, setSupported] = useState<boolean | null>(null)
  useEffect(() => { ed25519Supported().then(setSupported) }, [])

  const go = async () => {
    setBusy(true)
    setError('')
    try {
      const kp = await generateEd25519(comment.trim())
      const id = await vault.put({ kind: 'key', label: label.trim() || 'ed25519 key', ...kp })
      onDone(id)
    } catch (e) {
      setError(message(e))
      setBusy(false)
    }
  }
  return (
    <form className="stack-form" onSubmit={(e) => { e.preventDefault(); go() }}>
      <h3>Generate an Ed25519 key</h3>
      <p className="pop-sub">Made in this browser and stored only in your vault.</p>
      {supported === false && <p className="form-error">This browser can't generate Ed25519 keys. Generate one with ssh-keygen and import it instead.</p>}
      <input className="input" placeholder="Label, e.g. laptop" aria-label="Key label" value={label} onChange={(e) => setLabel(e.target.value)} autoFocus />
      <input className="input mono" placeholder="Comment" aria-label="Key comment" value={comment} onChange={(e) => setComment(e.target.value)} />
      {error && <p className="form-error">{error}</p>}
      <div className="acts">
        <button className="btn primary" type="submit" disabled={busy || supported === false}>{busy ? 'Generating…' : 'Generate'}</button>
        <button className="btn" type="button" onClick={() => onDone()}>Cancel</button>
      </div>
    </form>
  )
}

function ImportKey({ onDone }: { onDone: (id?: string) => void }) {
  const [label, setLabel] = useState('')
  const [pem, setPem] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [error, setError] = useState('')
  const [encrypted, setEncrypted] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    let live = true
    inspectPrivateKey(pem).then((i) => live && setEncrypted(!!i?.encrypted))
    return () => { live = false }
  }, [pem])

  const save = async () => {
    setError('')
    const info = await inspectPrivateKey(pem, label.trim())
    if (!info) return setError("That doesn't look like a private key. Paste the whole file, including the BEGIN/END lines.")
    if (info.encrypted && !passphrase) return setError('This key is encrypted. Enter its passphrase.')
    try {
      const id = await vault.put({
        kind: 'key', label: label.trim() || info.type, type: info.type, privateKey: pem.trim() + '\n',
        passphrase: passphrase || undefined, publicKey: info.publicKey, fingerprint: info.fingerprint,
      })
      onDone(id)
    } catch (e) {
      setError(message(e))
    }
  }

  return (
    <form className="stack-form" onSubmit={(e) => { e.preventDefault(); save() }}>
      <h3>Import a private key</h3>
      <input className="input" placeholder="Label" aria-label="Key label" value={label} onChange={(e) => setLabel(e.target.value)} autoFocus />
      <textarea className="textarea" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" aria-label="Private key" value={pem} onChange={(e) => setPem(e.target.value)} spellCheck={false} />
      <div className="key-actions" style={{ margin: 0 }}>
        <span>OpenSSH, PEM or PKCS#8</span>
        <button type="button" className="link" onClick={() => fileRef.current?.click()}>Load from file…</button>
        <input ref={fileRef} type="file" hidden onChange={async (e) => { const f = e.target.files?.[0]; if (f && f.size < 64 * 1024) setPem(await f.text()) }} />
      </div>
      {encrypted && <input className="input" type="password" placeholder="Passphrase" aria-label="Key passphrase" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} />}
      {error && <p className="form-error">{error}</p>}
      <div className="acts">
        <button className="btn primary" type="submit" disabled={!pem.trim()}>Import</button>
        <button className="btn" type="button" onClick={() => onDone()}>Cancel</button>
      </div>
    </form>
  )
}

function Identities({ keys, identities, hosts }: { keys: SavedKey[]; identities: Identity[]; hosts: SavedHost[] }) {
  const [editing, setEditing] = useState<Identity | 'new' | null>(identities.length ? null : 'new')
  const [confirm, setConfirm] = useState<string | null>(null)
  const usedBy = (id: string) => hosts.filter((h) => h.identityId === id).length

  return (
    <>
      <div className="acct-sec">
        {identities.length === 0 && editing === null && <p className="pop-sub">No identities yet.</p>}
        {identities.map((i) => (
          <div key={i.id} className="kc-row">
            <span className="kc-icon"><Icon name="user" size={15} /></span>
            <div className="kc-main">
              <div className="kc-title">{i.label}</div>
              <div className="kc-sub mono">
                {i.username} · {i.keyId ? `key: ${keys.find((k) => k.id === i.keyId)?.label ?? 'deleted key'}` : i.password ? 'password' : 'no credential'}
                {usedBy(i.id) ? ` · used by ${usedBy(i.id)}` : ''}
              </div>
            </div>
            <div className="kc-acts">
              <button className="icon-btn sm" title="Edit" aria-label={`Edit ${i.label}`} onClick={() => setEditing(i)}><Icon name="edit" size={14} /></button>
              {confirm === i.id ? (
                <>
                  <button className="btn danger" onClick={() => { vault.remove(i.id).catch(report); setConfirm(null) }}>Delete</button>
                  <button className="btn" onClick={() => setConfirm(null)}>Keep</button>
                </>
              ) : (
                <button className="icon-btn sm" title="Delete" aria-label={`Delete ${i.label}`} onClick={() => setConfirm(i.id)}><Icon name="trash" size={14} /></button>
              )}
            </div>
          </div>
        ))}
      </div>
      <div className="acct-sec">
        {editing === null ? (
          <div className="acts"><button className="btn primary" onClick={() => setEditing('new')}><Icon name="plus" size={13} /> New identity</button></div>
        ) : (
          <IdentityForm keys={keys} initial={editing === 'new' ? undefined : editing} onDone={() => setEditing(null)} />
        )}
      </div>
    </>
  )
}

function IdentityForm({ keys, initial, onDone }: { keys: SavedKey[]; initial?: Identity; onDone: () => void }) {
  const [label, setLabel] = useState(initial?.label ?? '')
  const [username, setUsername] = useState(initial?.username ?? '')
  const [kind, setKind] = useState<'password' | 'key'>(initial?.keyId || (!initial && keys.length) ? 'key' : 'password')
  const [password, setPassword] = useState('')
  const [keyId, setKeyId] = useState(initial?.keyId ?? keys[0]?.id ?? '')
  const [error, setError] = useState('')

  const save = () => {
    if (!username.trim()) return setError('Enter a username.')
    if (kind === 'key' && !keyId) return setError('Pick a key, or add one on the Keys tab.')
    if (kind === 'password' && !password && !initial?.password) return setError('Enter a password.')
    vault.put({
      kind: 'identity', label: label.trim() || username.trim(), username: username.trim(),
      ...(kind === 'key' ? { keyId } : { password: password || initial?.password }),
    }, initial?.id).catch(report)
    onDone()
  }

  return (
    <form className="stack-form" onSubmit={(e) => { e.preventDefault(); save() }}>
      <h3>{initial ? 'Edit identity' : 'New identity'}</h3>
      <input className="input" placeholder="Label, e.g. deploy" aria-label="Identity label" value={label} onChange={(e) => setLabel(e.target.value)} autoFocus />
      <input className="input mono" placeholder="Username" aria-label="Username" value={username} onChange={(e) => setUsername(e.target.value)} />
      <div className="seg mini">
        <button type="button" className={kind === 'key' ? 'on' : ''} onClick={() => setKind('key')}>Keychain key</button>
        <button type="button" className={kind === 'password' ? 'on' : ''} onClick={() => setKind('password')}>Password</button>
      </div>
      {kind === 'key' ? (
        <select className="input select" value={keyId} onChange={(e) => setKeyId(e.target.value)} aria-label="Key">
          {!keys.length && <option value="">No keys in the keychain</option>}
          {keys.map((k) => <option key={k.id} value={k.id}>{k.label} ({k.type.replace('ssh-', '')})</option>)}
        </select>
      ) : (
        <input className="input" type="password" placeholder={initial?.password ? 'Leave blank to keep the saved password' : 'Password'} aria-label="Password" value={password} onChange={(e) => setPassword(e.target.value)} />
      )}
      {error && <p className="form-error">{error}</p>}
      <div className="acts">
        <button className="btn primary" type="submit">Save identity</button>
        <button className="btn" type="button" onClick={onDone}>Cancel</button>
      </div>
    </form>
  )
}
