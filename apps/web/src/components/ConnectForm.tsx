import { useRef, useState, type FormEvent } from 'react'
import type { Auth } from '../lib/protocol'
import { HOST_COLORS, PERSONAL, type AuthMethod, type Identity, type SavedHost, type SavedKey, type Snippet, type VaultInfo } from '../lib/vault'
import { Logo } from './icons'

const MAX_KEY_BYTES = 64 * 1024

// Accepts "host", "host:port", "user@host[:port]" and "[ipv6]:port".
function parseHost(raw: string): { user?: string; host: string; port?: number } | null {
  let s = raw.trim()
  let user: string | undefined
  const at = s.lastIndexOf('@')
  if (at > 0) {
    user = s.slice(0, at)
    s = s.slice(at + 1)
  }
  if (!s) return null
  const bracketed = s.match(/^\[([^\]]+)\](?::(\d+))?$/)
  if (bracketed) return { user, host: bracketed[1], port: bracketed[2] ? Number(bracketed[2]) : undefined }
  if ((s.match(/:/g) ?? []).length > 1) return { user, host: s } // bare IPv6
  const [host, port] = s.split(':')
  if (!host || /\s/.test(host)) return null
  return { user, host, port: port ? Number(port) : undefined }
}

export type CredRef = { kind: 'key' | 'identity'; id: string }

export interface ConnectInitial {
  id?: string
  host?: string
  port?: number
  user?: string
  method?: AuthMethod
  label?: string
  color?: string
  group?: string
  keyId?: string
  identityId?: string
  jumpHostId?: string
  startupSnippetId?: string
  agent?: boolean
  /** Which vault the host lives in (personal or a team id). */
  vault?: string
  /** Editing a saved host that already has inline credentials. */
  hasCreds?: boolean
}

export interface ConnectResult {
  host: string
  port: number
  user: string
  method: AuthMethod
  /** Inline credential; absent when using the keychain or only saving. */
  auth?: Auth
  /** Keychain credential instead of an inline one. */
  ref?: CredRef
  save: boolean
  /** Keep the inline credentials with the saved host (encrypted in the vault). */
  storeCreds: boolean
  clearCreds: boolean
  label: string
  color: string
  group?: string
  jumpHostId?: string
  startupSnippetId?: string
  agent?: boolean
  vault: string
}

export interface VaultChoices {
  vaults?: VaultInfo[]
  hosts: SavedHost[]
  keys: SavedKey[]
  identities: Identity[]
  snippets: Snippet[]
}

interface Props {
  title: string
  /** "save" adds or edits a saved host; credentials are optional there. */
  purpose: 'connect' | 'save'
  initial: ConnectInitial
  choices: VaultChoices
  /** Show the "save to hosts" option (off when reconnecting an open session). */
  offerSave?: boolean
  notice?: string
  onSubmit: (r: ConnectResult) => void
  onCancel?: () => void
}

type Source = 'password' | 'key' | 'keychain'

export function ConnectForm({ title, purpose, initial, choices, offerSave = true, notice, onSubmit, onCancel }: Props) {
  const { keys, identities, snippets } = choices
  const hasKeychain = keys.length + identities.length > 0
  const initialRef = initial.identityId ? `identity:${initial.identityId}` : initial.keyId ? `key:${initial.keyId}` : ''
  const [host, setHost] = useState(initial.host ?? '')
  const [port, setPort] = useState(String(initial.port ?? 22))
  const [user, setUser] = useState(initial.user ?? '')
  const [source, setSource] = useState<Source>(initialRef ? 'keychain' : (initial.method ?? 'password'))
  const [refValue, setRefValue] = useState(initialRef || (identities[0] ? `identity:${identities[0].id}` : keys[0] ? `key:${keys[0].id}` : ''))
  const [password, setPassword] = useState('')
  const [key, setKey] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [save, setSave] = useState(purpose === 'save')
  const [storeCreds, setStoreCreds] = useState(true)
  const [clearCreds, setClearCreds] = useState(false)
  const [label, setLabel] = useState(initial.label ?? '')
  const [color, setColor] = useState(initial.color ?? HOST_COLORS[0])
  const [group, setGroup] = useState(initial.group ?? '')
  const [jumpHostId, setJumpHostId] = useState(initial.jumpHostId ?? '')
  const [startupSnippetId, setStartupSnippetId] = useState(initial.startupSnippetId ?? '')
  const [agent, setAgent] = useState(!!initial.agent)
  const [vaultId, setVaultId] = useState(initial.vault ?? PERSONAL)
  const vaultChoices = (choices.vaults ?? []).filter((v) => v.writable && !v.error)
  const [more, setMore] = useState(!!(initial.group || initial.jumpHostId || initial.startupSnippetId || initial.agent))
  const [error, setError] = useState('')
  const [joined, setJoined] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const credsFirst = purpose === 'connect' && !!initial.host
  const saving = purpose === 'save'
  const ref: CredRef | undefined = source === 'keychain' && refValue
    ? { kind: refValue.startsWith('identity:') ? 'identity' : 'key', id: refValue.slice(refValue.indexOf(':') + 1) }
    : undefined
  const identity = ref?.kind === 'identity' ? identities.find((i) => i.id === ref.id) : undefined
  const groups = [...new Set(choices.hosts.map((h) => h.group).filter(Boolean))] as string[]
  const jumpChoices = choices.hosts.filter((h) => h.id !== initial.id)

  const loadKeyFile = async (file: File | undefined) => {
    if (!file) return
    if (file.size > MAX_KEY_BYTES) return setError('That file is too large to be a private key.')
    setKey(await file.text())
    setError('')
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const parsed = parseHost(host)
    if (!parsed) return setError('Enter a hostname or IP address.')
    const finalUser = identity?.username ?? parsed.user ?? user.trim()
    const finalPort = parsed.port ?? Number(port)
    if (!finalUser) return setError('Enter a username.')
    if (!Number.isInteger(finalPort) || finalPort < 1 || finalPort > 65535) return setError('Port must be 1–65535.')
    if (source === 'keychain' && !ref) return setError('Pick a key or identity, or add one in the keychain.')
    const given = source === 'password' ? !!password : source === 'key' ? !!key.trim() : false
    if (!saving && source !== 'keychain' && !given) return setError(source === 'password' ? 'Enter a password.' : 'Paste or load a private key.')
    const auth: Auth | undefined = !given
      ? undefined
      : source === 'password' ? { type: 'password', password } : { type: 'key', key, passphrase: passphrase || undefined }
    onSubmit({
      host: parsed.host, port: finalPort, user: finalUser, method: source === 'password' ? 'password' : 'key', auth, ref,
      save, storeCreds: saving ? given : save && storeCreds, clearCreds,
      label: label.trim(), color, group: group.trim() || undefined,
      jumpHostId: jumpHostId || undefined, startupSnippetId: startupSnippetId || undefined, agent, vault: vaultId,
    })
  }

  const credsHint = saving ? (initial.hasCreds ? 'leave blank to keep the saved one' : 'optional, stored encrypted') : ''

  return (
    <form className="connect-card" onSubmit={submit} onFocus={() => setJoined(true)} autoComplete="off" noValidate>
      <Logo size={36} apart={!joined} />
      <h1>{title}</h1>
      <p className="sub">{notice ?? (saving ? 'Saved hosts sync to your encrypted vault.' : 'Open an SSH session in your browser.')}</p>

      <div className="row2">
        <label className="fld">
          <span>Host</span>
          <input className="input mono" value={host} onChange={(e) => setHost(e.target.value)} placeholder="server.example.com" autoFocus={!credsFirst} spellCheck={false} autoCapitalize="off" />
        </label>
        <label className="fld">
          <span>Port</span>
          <input className="input mono" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ''))} inputMode="numeric" />
        </label>
      </div>
      <label className="fld">
        <span>Username {identity && <em className="hint">(from identity)</em>}</span>
        <input className="input mono" value={identity ? identity.username : user} disabled={!!identity} onChange={(e) => setUser(e.target.value)} placeholder="root" spellCheck={false} autoCapitalize="off" />
      </label>

      <div className="seg" role="tablist" aria-label="Credentials">
        <button type="button" role="tab" aria-selected={source === 'password'} className={source === 'password' ? 'on' : ''} onClick={() => setSource('password')}>Password</button>
        <button type="button" role="tab" aria-selected={source === 'key'} className={source === 'key' ? 'on' : ''} onClick={() => setSource('key')}>Private key</button>
        <button type="button" role="tab" aria-selected={source === 'keychain'} className={source === 'keychain' ? 'on' : ''} onClick={() => setSource('keychain')}>Keychain</button>
      </div>

      {source === 'keychain' && (
        <label className="fld">
          <span>Key or identity</span>
          <select className="input select wide" value={refValue} onChange={(e) => setRefValue(e.target.value)}>
            {!hasKeychain && <option value="">Keychain is empty</option>}
            {identities.length > 0 && (
              <optgroup label="Identities">
                {identities.map((i) => <option key={i.id} value={`identity:${i.id}`}>{i.label} — {i.username}</option>)}
              </optgroup>
            )}
            {keys.length > 0 && (
              <optgroup label="Keys">
                {keys.map((k) => <option key={k.id} value={`key:${k.id}`}>{k.label} ({k.type.replace('ssh-', '')})</option>)}
              </optgroup>
            )}
          </select>
        </label>
      )}
      {source === 'password' && !clearCreds && (
        <label className="fld">
          <span>Password {credsHint && <em className="hint">({credsHint})</em>}</span>
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" autoFocus={credsFirst} />
        </label>
      )}
      {source === 'key' && !clearCreds && (
        <>
          <label className="fld">
            <span>Private key {credsHint && <em className="hint">({credsHint})</em>}</span>
            <textarea className="textarea" value={key} onChange={(e) => setKey(e.target.value)} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" spellCheck={false} autoFocus={credsFirst} />
          </label>
          <div className="key-actions">
            <span>OpenSSH, PEM or PKCS#8</span>
            <button type="button" className="link" onClick={() => fileRef.current?.click()}>Load from file…</button>
            <input ref={fileRef} type="file" hidden onChange={(e) => loadKeyFile(e.target.files?.[0])} />
          </div>
          <label className="fld">
            <span>Passphrase <em className="hint">(if encrypted)</em></span>
            <input className="input" type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} autoComplete="off" />
          </label>
        </>
      )}

      {saving && initial.hasCreds && source !== 'keychain' && (
        <label className="check">
          <input type="checkbox" checked={clearCreds} onChange={(e) => setClearCreds(e.target.checked)} />
          <span>Remove saved credentials</span>
        </label>
      )}
      {!saving && offerSave && (
        <label className="check">
          <input type="checkbox" checked={save} onChange={(e) => setSave(e.target.checked)} />
          <span>Save to hosts</span>
        </label>
      )}
      {!saving && save && source !== 'keychain' && (
        <label className="check indent">
          <input type="checkbox" checked={storeCreds} onChange={(e) => setStoreCreds(e.target.checked)} />
          <span>Remember {source === 'password' ? 'password' : 'key'} <em className="hint">(end-to-end encrypted)</em></span>
        </label>
      )}
      {save && vaultChoices.length > 1 && (
        <label className="fld">
          <span>Vault {vaultId !== (initial.vault ?? PERSONAL) && <em className="hint">(its keys, jump host and snippet are copied along)</em>}</span>
          <select className="input select wide" value={vaultId} onChange={(e) => setVaultId(e.target.value)}>
            {vaultChoices.map((v) => <option key={v.id} value={v.id}>{v.id === PERSONAL ? 'Personal (only you)' : `Team · ${v.name}`}</option>)}
          </select>
        </label>
      )}
      {save && (
        <div className="save-row">
          <label className="fld" style={{ flex: 1 }}>
            <span>Label</span>
            <input className="input" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="prod-api" />
          </label>
          <div className="fld">
            <span>Tag</span>
            <div className="colors" role="radiogroup" aria-label="Color tag">
              {HOST_COLORS.map((c) => (
                <button key={c} type="button" role="radio" aria-checked={color === c} aria-label={c} className={color === c ? 'on' : ''} style={{ background: c }} onClick={() => setColor(c)} />
              ))}
            </div>
          </div>
        </div>
      )}

      {(save || jumpChoices.length > 0) && (
        <button type="button" className="link more-toggle" onClick={() => setMore((m) => !m)} aria-expanded={more}>
          {more ? 'Fewer options' : 'More options'}{!more && (jumpHostId || group || startupSnippetId) ? ' (set)' : ''}
        </button>
      )}
      {more && (
        <div className="more">
          {jumpChoices.length > 0 && (
            <label className="fld">
              <span>Jump host <em className="hint">(ProxyJump)</em></span>
              <select className="input select wide" value={jumpHostId} onChange={(e) => setJumpHostId(e.target.value)}>
                <option value="">Direct connection</option>
                {jumpChoices.map((h) => <option key={h.id} value={h.id}>{h.label || h.host} — {h.user}@{h.host}</option>)}
              </select>
            </label>
          )}
          {source !== 'password' && (
            <label className="check">
              <input type="checkbox" checked={agent} onChange={(e) => setAgent(e.target.checked)} />
              <span>Forward SSH agent <em className="hint">(lets the server use this key, e.g. for git; only for servers you trust)</em></span>
            </label>
          )}
          {save && (
            <>
              <label className="fld">
                <span>Group</span>
                <input className="input" list="host-groups" value={group} onChange={(e) => setGroup(e.target.value)} placeholder="production" />
                <datalist id="host-groups">{groups.map((g) => <option key={g} value={g} />)}</datalist>
              </label>
              <label className="fld">
                <span>Run on connect</span>
                <select className="input select wide" value={startupSnippetId} onChange={(e) => setStartupSnippetId(e.target.value)}>
                  <option value="">Nothing</option>
                  {snippets.map((s) => <option key={s.id} value={s.id}>{s.name} — {s.command}</option>)}
                </select>
              </label>
            </>
          )}
        </div>
      )}

      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="form-actions">
        {onCancel && <button className="btn lg" type="button" onClick={onCancel}>Cancel</button>}
        <button className="btn primary lg" type="submit">{saving ? 'Save host' : 'Connect'}</button>
      </div>
      {!saving && <p className="foot">Credentials go to this gateway for the handshake only and are never logged.</p>}
    </form>
  )
}
