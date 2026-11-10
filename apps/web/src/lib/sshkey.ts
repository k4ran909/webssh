// SSH key helpers: generate Ed25519 keys in OpenSSH format and read the
// public half out of an existing private key. Self-contained (no imports) so
// it can be exercised directly with Node.

const enc = new TextEncoder()
const dec = new TextDecoder()

function b64(b: Uint8Array): string {
  let s = ''
  for (const x of b) s += String.fromCharCode(x)
  return btoa(s)
}

const unb64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0))

function concat(...parts: Uint8Array[]): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
  let o = 0
  for (const p of parts) {
    out.set(p, o)
    o += p.length
  }
  return out
}

function u32(n: number) {
  const b = new Uint8Array(4)
  new DataView(b.buffer).setUint32(0, n)
  return b
}

const sshString = (d: Uint8Array | string) => {
  const bytes = typeof d === 'string' ? enc.encode(d) : d
  return concat(u32(bytes.length), bytes)
}

async function fingerprint(pubBlob: Uint8Array<ArrayBuffer>): Promise<string> {
  const h = new Uint8Array(await crypto.subtle.digest('SHA-256', pubBlob))
  return 'SHA256:' + b64(h).replace(/=+$/, '')
}

export interface KeyPair {
  type: string
  privateKey: string
  publicKey: string
  fingerprint: string
}

export const ed25519Supported = async () => {
  try {
    await crypto.subtle.generateKey({ name: 'Ed25519' }, false, ['sign', 'verify'])
    return true
  } catch {
    return false
  }
}

/** New Ed25519 key in the unencrypted openssh-key-v1 format (the vault encrypts it). */
export async function generateEd25519(comment: string): Promise<KeyPair> {
  const kp = (await crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify'])) as CryptoKeyPair
  const pub = new Uint8Array(await crypto.subtle.exportKey('raw', kp.publicKey))
  const pkcs8 = new Uint8Array(await crypto.subtle.exportKey('pkcs8', kp.privateKey))
  const seed = pkcs8.slice(pkcs8.length - 32) // PKCS#8 Ed25519 ends with the 32-byte seed

  const pubBlob = concat(sshString('ssh-ed25519'), sshString(pub))
  const check = crypto.getRandomValues(new Uint8Array(4))
  const secret = concat(seed, pub)
  let priv = concat(check, check, sshString('ssh-ed25519'), sshString(pub), sshString(secret), sshString(comment))
  for (let i = 1; priv.length % 8 !== 0; i++) priv = concat(priv, new Uint8Array([i]))

  const body = concat(enc.encode('openssh-key-v1\0'), sshString('none'), sshString('none'), sshString(''), u32(1), sshString(pubBlob), sshString(priv))
  const privateKey = `-----BEGIN OPENSSH PRIVATE KEY-----\n${b64(body).match(/.{1,70}/g)!.join('\n')}\n-----END OPENSSH PRIVATE KEY-----\n`
  ;[seed, pkcs8, secret, priv].forEach((b) => b.fill(0))
  return {
    type: 'ssh-ed25519',
    privateKey,
    publicKey: `ssh-ed25519 ${b64(pubBlob)}${comment ? ' ' + comment : ''}`,
    fingerprint: await fingerprint(pubBlob),
  }
}

export interface KeyInfo {
  type: string
  encrypted: boolean
  publicKey?: string
  fingerprint?: string
}

/**
 * Describes a pasted private key. OpenSSH-format keys carry their public key
 * in the clear (even when encrypted), so we can show it; PEM keys only
 * reveal their type.
 */
export async function inspectPrivateKey(pem: string, comment = ''): Promise<KeyInfo | null> {
  const text = pem.trim()
  const m = text.match(/-----BEGIN ([A-Z0-9 ]+)-----([\s\S]+?)-----END \1-----/)
  if (!m) return null
  const kind = m[1]
  if (kind !== 'OPENSSH PRIVATE KEY') {
    const type = kind.startsWith('RSA') ? 'ssh-rsa' : kind.startsWith('EC') ? 'ecdsa' : kind.startsWith('DSA') ? 'ssh-dss' : 'private key'
    return { type, encrypted: kind.includes('ENCRYPTED') || /Proc-Type:\s*4,ENCRYPTED/.test(m[2]) }
  }
  try {
    const body = unb64(m[2].replace(/\s+/g, ''))
    const dv = new DataView(body.buffer)
    let o = 0
    const magic = dec.decode(body.slice(0, 15))
    if (magic !== 'openssh-key-v1\0') return null
    o = 15
    const str = () => {
      const n = dv.getUint32(o)
      const s = body.slice(o + 4, o + 4 + n)
      o += 4 + n
      return s
    }
    const cipher = dec.decode(str())
    str() // kdf name
    str() // kdf options
    o += 4 // number of keys
    const pubBlob = str()
    const type = dec.decode(pubBlob.slice(4, 4 + new DataView(pubBlob.buffer).getUint32(0)))
    return {
      type,
      encrypted: cipher !== 'none',
      publicKey: `${type} ${b64(pubBlob)}${comment ? ' ' + comment : ''}`,
      fingerprint: await fingerprint(pubBlob),
    }
  } catch {
    return null
  }
}

/** One-liner that appends a public key to ~/.ssh/authorized_keys (idempotent). */
export function installCommand(publicKey: string): string {
  const k = publicKey.replace(/'/g, `'\\''`)
  return `mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && ` +
    `(grep -qxF '${k}' ~/.ssh/authorized_keys || echo '${k}' >> ~/.ssh/authorized_keys) && echo 'key installed'`
}
