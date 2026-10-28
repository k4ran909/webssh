import { useRef, useState, type FormEvent } from 'react'
import type { Target } from '../lib/protocol'
import { getLastTarget, saveLastTarget } from '../lib/storage'
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

export function ConnectForm({ onConnect }: { onConnect: (t: Target) => void }) {
  const last = getLastTarget()
  const [host, setHost] = useState(last.host ?? '')
  const [port, setPort] = useState(String(last.port ?? 22))
  const [user, setUser] = useState(last.user ?? '')
  const [method, setMethod] = useState<'password' | 'key'>(last.method ?? 'password')
  const [password, setPassword] = useState('')
  const [key, setKey] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [error, setError] = useState('')
  const [joined, setJoined] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

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
    if (method === 'password' && !password) return setError('Enter a password.')
    if (method === 'key' && !key.trim()) return setError('Paste or load a private key.')

    saveLastTarget({ host: parsed.host, port: finalPort, user: finalUser, method })
    onConnect({
      host: parsed.host,
      port: finalPort,
      user: finalUser,
      auth: method === 'password' ? { type: 'password', password } : { type: 'key', key, passphrase: passphrase || undefined },
    })
  }

  return (
    <div className="connect">
      <form className="connect-card" onSubmit={submit} onFocus={() => setJoined(true)} autoComplete="off" noValidate>
        <Logo size={44} apart={!joined} />
        <h1>New connection</h1>
        <p className="sub">Open an SSH session in your browser.</p>

        <div className="row2">
          <label className="fld">
            <span>Host</span>
            <input className="input mono" value={host} onChange={(e) => setHost(e.target.value)} placeholder="server.example.com" autoFocus spellCheck={false} autoCapitalize="off" />
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

        {method === 'password' ? (
          <label className="fld">
            <span>Password</span>
            <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" />
          </label>
        ) : (
          <>
            <label className="fld">
              <span>Private key</span>
              <textarea className="textarea" value={key} onChange={(e) => setKey(e.target.value)} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" spellCheck={false} />
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

        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="btn primary lg" type="submit" style={{ width: '100%' }}>Connect</button>
        <p className="foot">Your credentials go to this gateway for the handshake only.<br />They're never stored or logged.</p>
      </form>
    </div>
  )
}
