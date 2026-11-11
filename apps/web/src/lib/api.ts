import type { Kdf } from './crypto'

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message)
  }
}

let policyBlocked: () => void = () => {}
/** Called whenever the gateway refuses a request because this account must set up 2FA first. */
export function onPolicyBlock(fn: () => void) {
  policyBlocked = fn
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new ApiError(0, 'NETWORK', "Can't reach the gateway.")
  }
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    if (data.code === 'TOTP_SETUP_REQUIRED') policyBlocked()
    throw new ApiError(res.status, data.code ?? `HTTP_${res.status}`, data.msg ?? res.statusText)
  }
  return data as T
}

export interface User {
  id: string
  email: string
  totp: boolean
  isAdmin?: boolean
}

export interface Keys {
  kdf: Kdf
  salt: string
  wrappedKey: string
}

export interface Keypair {
  publicKey: string
  encPrivateKey: string
}

export interface Policy {
  require2fa: boolean
}

export interface SignedIn {
  user: User
  keys: Keys
  keypair?: Keypair
  policy?: Policy
}

export interface SessionInfo {
  user: User | null
  keys?: Keys
  keypair?: Keypair
  policy?: Policy
  signupOpen: boolean
}

export interface AuditEvent {
  id: number
  email?: string
  action: string
  ip: string
  detail: string
  createdAt: string
}
