// Account flows: each turns a password into keys locally, then talks to /api.
import { api, ApiError, type Keys, type SignedIn } from './api'
import {
  derivePasswordKeys, importVaultKey, newCredentials, newRecovery, randomBytes, recoveryAuth, unwrapVaultKey,
  type Bytes, type Kdf, type PasswordKeys,
} from './crypto'

export interface Opened {
  session: SignedIn
  vaultKey: CryptoKey
  /** Shown once: after sign-up, recovery, or regeneration. */
  recoveryCode?: string
}

const wrongPassword = () => new ApiError(401, 'INVALID_CREDENTIALS', 'Wrong master password.')

async function unlockWith(keys: PasswordKeys, wrapped: string): Promise<CryptoKey> {
  let raw: Bytes
  try {
    raw = await unwrapVaultKey(keys.wrapKey, wrapped)
  } catch {
    throw wrongPassword()
  }
  const key = await importVaultKey(raw)
  raw.fill(0)
  return key
}

export async function signup(email: string, password: string): Promise<Opened> {
  const raw = randomBytes(32)
  const creds = await newCredentials(password, raw)
  const rec = await newRecovery(raw)
  const session = await api<SignedIn>('POST', '/api/auth/signup', { email, ...creds, ...rec.material })
  const vaultKey = await importVaultKey(raw)
  raw.fill(0)
  return { session, vaultKey, recoveryCode: rec.code }
}

/** Slow key derivation for a login, kept so a 2FA retry doesn't redo it. */
export async function prepareLogin(email: string, password: string): Promise<PasswordKeys> {
  const pre = await api<{ kdf: Kdf; salt: string }>('POST', '/api/auth/prelogin', { email })
  return derivePasswordKeys(password, pre.salt, pre.kdf)
}

export async function login(email: string, keys: PasswordKeys, totp?: string): Promise<Opened> {
  const session = await api<SignedIn>('POST', '/api/auth/login', { email, authHash: keys.authHash, totp: totp || undefined })
  return { session, vaultKey: await unlockWith(keys, session.keys.wrappedKey) }
}

/** Unlock after a reload or auto-lock: no server round trip, just unwrap. */
export async function unlock(password: string, keys: Keys): Promise<CryptoKey> {
  return unlockWith(await derivePasswordKeys(password, keys.salt, keys.kdf), keys.wrappedKey)
}

async function rawVaultKey(password: string, keys: Keys): Promise<{ pk: PasswordKeys; raw: Bytes }> {
  const pk = await derivePasswordKeys(password, keys.salt, keys.kdf)
  try {
    return { pk, raw: await unwrapVaultKey(pk.wrapKey, keys.wrappedKey) }
  } catch {
    throw wrongPassword()
  }
}

export async function changePassword(current: string, next: string, keys: Keys): Promise<SignedIn> {
  const { pk, raw } = await rawVaultKey(current, keys)
  const creds = await newCredentials(next, raw)
  raw.fill(0)
  return api<SignedIn>('POST', '/api/auth/password', { currentAuthHash: pk.authHash, ...creds })
}

export async function regenerateRecovery(password: string, keys: Keys): Promise<string> {
  const { pk, raw } = await rawVaultKey(password, keys)
  const rec = await newRecovery(raw)
  raw.fill(0)
  await api('POST', '/api/auth/recovery', { authHash: pk.authHash, ...rec.material })
  return rec.code
}

export async function recover(email: string, code: string, newPassword: string): Promise<Opened> {
  const rk = await recoveryAuth(code)
  const begin = await api<{ recoveryWrappedKey: string; ticket: string }>('POST', '/api/auth/recover/begin', { email, recoveryAuth: rk.auth })
  let raw: Bytes
  try {
    raw = await unwrapVaultKey(rk.wrapKey, begin.recoveryWrappedKey)
  } catch {
    throw new ApiError(401, 'INVALID_RECOVERY', 'Wrong recovery code.')
  }
  const creds = await newCredentials(newPassword, raw)
  const rec = await newRecovery(raw)
  const session = await api<SignedIn>('POST', '/api/auth/recover/finish', { ticket: begin.ticket, ...creds, ...rec.material })
  const vaultKey = await importVaultKey(raw)
  raw.fill(0)
  return { session, vaultKey, recoveryCode: rec.code }
}

export async function authHashFor(password: string, keys: Keys): Promise<string> {
  return (await rawVaultKey(password, keys)).pk.authHash
}

export const logout = () => api('POST', '/api/auth/logout', {})
