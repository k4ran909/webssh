// Phase 0 stand-in for the encrypted vault: only non-secret data lives here.
// Passwords and keys are never written to storage.

const KNOWN_HOSTS = 'webssh.knownHosts'
const LAST_TARGET = 'webssh.last'

function read<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as T) : fallback
  } catch {
    return fallback
  }
}

function write(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // storage blocked (private mode etc.): trust is just not remembered
  }
}

const hostId = (host: string, port: number) => `${host.toLowerCase()}:${port}`

export function getKnownHost(host: string, port: number): string | undefined {
  return read<Record<string, string>>(KNOWN_HOSTS, {})[hostId(host, port)]
}

export function saveKnownHost(host: string, port: number, fingerprint: string) {
  const all = read<Record<string, string>>(KNOWN_HOSTS, {})
  all[hostId(host, port)] = fingerprint
  write(KNOWN_HOSTS, all)
}

export function forgetKnownHost(host: string, port: number) {
  const all = read<Record<string, string>>(KNOWN_HOSTS, {})
  delete all[hostId(host, port)]
  write(KNOWN_HOSTS, all)
}

export interface LastTarget {
  host: string
  port: number
  user: string
  method: 'password' | 'key'
}

export const getLastTarget = () => read<Partial<LastTarget>>(LAST_TARGET, {})
export const saveLastTarget = (t: LastTarget) => write(LAST_TARGET, t)

export type ThemePref = 'system' | 'light' | 'dark'
export const getThemePref = () => read<ThemePref>('webssh.theme', 'system')
export const saveThemePref = (t: ThemePref) => write('webssh.theme', t)
