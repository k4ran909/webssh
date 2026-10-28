import { useRef, useState, type FormEvent } from 'react'
import type { Auth } from '../lib/protocol'
import { HOST_COLORS, type AuthMethod } from '../lib/storage'
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

export interface ConnectInitial {
  host?: string
  port?: number
  user?: string
  method?: AuthMethod
  label?: string
  color?: string
}

export interface ConnectResult {
  host: string
  port: number
  user: string
  method: AuthMethod
  /** Absent when only saving a host. */
  auth?: Auth
  save: boolean
  label: string
  color: string
}

interface Props {
  title: string
  /** "save" edits a saved host without credentials. */
  purpose: 'connect' | 'save'
  initial: ConnectInitial
  /** Show the "save to hosts" option (off when reconnecting an open session). */
  offerSave?: boolean
  onSubmit: (r: ConnectResult) => void
  onCancel?: () => void
}

export function ConnectForm({ title, purpose, initial, offerSave = true, onSubmit, onCancel }: Props) {
  const [host, setHost] = useState(initial.host ?? '')
  const [port, setPort] = useState(String(initial.port ?? 22))
  const [user, setUser] = useState(initial.user ?? '')
  const [method, setMethod] = useState<AuthMethod>(initial.method ?? 'password')
  const [password, setPassword] = useState('')
  const [key, setKey] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [save, setSave] = useState(purpose === 'save')
  const [label, setLabel] = useState(initial.label ?? '')
  const [color, setColor] = useState(initial.color ?? HOST_COLORS[0])
  const [error, setError] = useState('')
  const [joined, setJoined] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const credsFirst = purpose === 'connect' && !!initial.host

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
    const finalUser = parsed.user ?? user.trim()
    const finalPort = parsed.port ?? Number(port)
    if (!finalUser) return setError('Enter a username.')
    if (!Number.isInteger(finalPort) || finalPort < 1 || finalPort > 65535) return setError('Port must be 1–65535.')
    let auth: Auth | undefined
    if (purpose === 'connect') {
      if (method === 'password' && !password) return setError('Enter a password.')
      if (method === 'key' && !key.trim()) return setError('Paste or load a private key.')
      auth = method === 'password' ? { type: 'password', password } : { type: 'key', key, passphrase: passphrase || undefined }
    }
    onSubmit({ host: parsed.host, port: finalPort, user: finalUser, method, auth, save, label: label.trim(), color })
  }

  return (
    <form className="connect-card" onSubmit={submit} onFocus={() => setJoined(true)} autoComplete="off" noValidate>
      <Logo size={36} apart={!joined} />
      <h1>{title}</h1>
      <p className="sub">{purpose === 'save' ? 'Credentials are asked for when you connect.' : 'Open an SSH session in your browser.'}</p>

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
        <span>Username</span>
        <input className="input mono" value={user} onChange={(e) => setUser(e.target.value)} placeholder="root" spellCheck={false} autoCapitalize="off" />
      </label>

      <div className="seg" role="tablist" aria-label="Authentication method">
        <button type="button" role="tab" aria-selected={method === 'password'} className={method === 'password' ? 'on' : ''} onClick={() => setMethod('password')}>Password</button>
        <button type="button" role="tab" aria-selected={method === 'key'} className={method === 'key' ? 'on' : ''} onClick={() => setMethod('key')}>Private key</button>
      </div>

      {purpose === 'connect' && method === 'password' && (
        <label className="fld">
          <span>Password</span>
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" autoFocus={credsFirst} />
        </label>
      )}
      {purpose === 'connect' && method === 'key' && (
        <>
          <label className="fld">
            <span>Private key</span>
            <textarea className="textarea" value={key} onChange={(e) => setKey(e.target.value)} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" spellCheck={false} autoFocus={credsFirst} />
          </label>
          <div className="key-actions">
            <span>OpenSSH, PEM or PKCS#8</span>
            <button type="button" className="link" onClick={() => fileRef.current?.click()}>Load from file…</button>
            <input ref={fileRef} type="file" hidden onChange={(e) => loadKeyFile(e.target.files?.[0])} />
          </div>
          <label className="fld">
            <span>Passphrase <span style={{ color: 'var(--text-3)' }}>(if encrypted)</span></span>
            <input className="input" type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} autoComplete="off" />
          </label>
        </>
      )}

      {purpose === 'connect' && offerSave && (
        <label className="check">
          <input type="checkbox" checked={save} onChange={(e) => setSave(e.target.checked)} />
          <span>Save to hosts <span style={{ color: 'var(--text-3)' }}>(without credentials)</span></span>
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

      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="form-actions">
        {onCancel && <button className="btn lg" type="button" onClick={onCancel}>Cancel</button>}
        <button className="btn primary lg" type="submit">{purpose === 'save' ? 'Save host' : 'Connect'}</button>
      </div>
      {purpose === 'connect' && <p className="foot">Credentials go to this gateway for the handshake only and are never stored.</p>}
    </form>
  )
}
