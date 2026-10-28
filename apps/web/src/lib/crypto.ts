// Client-side vault crypto. Nothing here talks to the network.
//
//   master   = Argon2id(password, salt)                  (64 MiB, t=3)
//   authHash = HKDF(master, "webssh/v1/auth")            → sent to the server as a login token
//   wrapKey  = HKDF(master, "webssh/v1/wrap")            → never leaves the browser
//   vaultKey = random 256-bit AES-GCM key, stored on the server wrapped by wrapKey
//   item     = AES-GCM(vaultKey, json, aad = "webssh/v1/item/<id>")
//
// The recovery code derives a second auth token and wrap key the same way,
// so it can unwrap a second copy of the vault key.
import { argon2id } from 'hash-wasm'

export interface Kdf {
  alg: 'argon2id'
  m: number // KiB
  t: number
  p: number
}

export const DEFAULT_KDF: Kdf = { alg: 'argon2id', m: 65536, t: 3, p: 1 }

/** Plain ArrayBuffer-backed bytes, as WebCrypto wants them. */
export type Bytes = Uint8Array<ArrayBuffer>

const enc = new TextEncoder()
const dec = new TextDecoder()

export function toB64(b: Bytes): string {
  let s = ''
  for (const x of b) s += String.fromCharCode(x)
  return btoa(s)
}

export const fromB64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0))
export const randomBytes = (n: number) => crypto.getRandomValues(new Uint8Array(n))

async function hkdf(ikm: Bytes, info: string): Promise<Bytes> {
  const key = await crypto.subtle.importKey('raw', ikm, 'HKDF', false, ['deriveBits'])
  const bits = await crypto.subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(0), info: enc.encode(info) }, key, 256)
  return new Uint8Array(bits)
}

const aesKey = (raw: Bytes) => crypto.subtle.importKey('raw', raw, 'AES-GCM', false, ['encrypt', 'decrypt'])

async function seal(key: CryptoKey, plain: Bytes, aad: string): Promise<string> {
  const iv = randomBytes(12)
  const ct = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv, additionalData: enc.encode(aad) }, key, plain))
  const out = new Uint8Array(iv.length + ct.length)
  out.set(iv)
  out.set(ct, iv.length)
  return toB64(out)
}

async function open(key: CryptoKey, data: string, aad: string): Promise<Bytes> {
  const b = fromB64(data)
  return new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: b.slice(0, 12), additionalData: enc.encode(aad) }, key, b.slice(12)))
}

export interface PasswordKeys {
  authHash: string
  wrapKey: CryptoKey
}

/** The slow step (~0.5–1.5 s): run once per attempt and reuse the result. */
export async function derivePasswordKeys(password: string, salt: string, kdf: Kdf): Promise<PasswordKeys> {
  const master = await argon2id({
    password: password.normalize('NFKC'),
    salt: fromB64(salt),
    parallelism: kdf.p,
    iterations: kdf.t,
    memorySize: kdf.m,
    hashLength: 32,
    outputType: 'binary',
  })
  const m = new Uint8Array(master) as Bytes
  const [auth, wrap] = await Promise.all([hkdf(m, 'webssh/v1/auth'), hkdf(m, 'webssh/v1/wrap')])
  m.fill(0)
  master.fill(0)
  const wrapKey = await aesKey(wrap)
  wrap.fill(0)
  return { authHash: toB64(auth), wrapKey }
}

const VAULT_KEY_AAD = 'webssh/v1/vault-key'

export const wrapVaultKey = (wrapKey: CryptoKey, raw: Bytes) => seal(wrapKey, raw, VAULT_KEY_AAD)

/** Throws if the password (or recovery code) is wrong: AES-GCM authentication fails. */
export async function unwrapVaultKey(wrapKey: CryptoKey, wrapped: string): Promise<Bytes> {
  return open(wrapKey, wrapped, VAULT_KEY_AAD)
}

/** The in-memory vault key is non-extractable: page script can use it but never read it back. */
export const importVaultKey = (raw: Bytes) => aesKey(raw)

export interface Credentials {
  kdf: Kdf
  salt: string
  authHash: string
  wrappedKey: string
}

/** Fresh salt + password-derived material wrapping an existing raw vault key. */
export async function newCredentials(password: string, rawVaultKey: Bytes): Promise<Credentials> {
  const salt = toB64(randomBytes(16))
  const k = await derivePasswordKeys(password, salt, DEFAULT_KDF)
  return { kdf: DEFAULT_KDF, salt, authHash: k.authHash, wrappedKey: await wrapVaultKey(k.wrapKey, rawVaultKey) }
}

// ── recovery code: 160 random bits, Crockford base32, 8 groups of 4 ──

const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'

export function newRecoveryCode(): string {
  const bytes = randomBytes(20)
  let bits = 0
  let acc = 0
  let out = ''
  for (const b of bytes) {
    acc = (acc << 8) | b
    bits += 8
    while (bits >= 5) {
      out += CROCKFORD[(acc >> (bits - 5)) & 31]
      bits -= 5
    }
  }
  return out.match(/.{4}/g)!.join('-')
}

export function normalizeRecoveryCode(s: string): string {
  return s.toUpperCase().replace(/[^0-9A-Z]/g, '').replace(/O/g, '0').replace(/[IL]/g, '1')
}

export interface RecoveryMaterial {
  recoveryAuth: string
  recoveryWrappedKey: string
}

async function recoveryKeys(code: string) {
  const norm = normalizeRecoveryCode(code)
  if (norm.length !== 32) throw new Error('A recovery code has 32 characters.')
  const ikm = enc.encode(norm)
  const [auth, wrap] = await Promise.all([hkdf(ikm, 'webssh/v1/recovery-auth'), hkdf(ikm, 'webssh/v1/recovery-wrap')])
  return { auth: toB64(auth), wrapKey: await aesKey(wrap) }
}

export async function recoveryAuth(code: string): Promise<{ auth: string; wrapKey: CryptoKey }> {
  return recoveryKeys(code)
}

/** New recovery code plus the material the server stores for it. */
export async function newRecovery(rawVaultKey: Bytes): Promise<{ code: string; material: RecoveryMaterial }> {
  const code = newRecoveryCode()
  const k = await recoveryKeys(code)
  return { code, material: { recoveryAuth: k.auth, recoveryWrappedKey: await wrapVaultKey(k.wrapKey, rawVaultKey) } }
}

// ── vault items ──

export const encryptItem = (vaultKey: CryptoKey, id: string, body: unknown) =>
  seal(vaultKey, enc.encode(JSON.stringify(body)), 'webssh/v1/item/' + id)

export async function decryptItem<T>(vaultKey: CryptoKey, id: string, data: string): Promise<T> {
  return JSON.parse(dec.decode(await open(vaultKey, data, 'webssh/v1/item/' + id))) as T
}
