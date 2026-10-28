// Per-browser conveniences only. Hosts, snippets and trusted host keys live in
// the encrypted vault (lib/vault.ts); nothing secret is ever written here.

const LAST_TARGET = 'webssh.last'
const PREFS = 'webssh.prefs'

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
    // storage blocked (private mode etc.): settings just aren't remembered
  }
}

export interface LastTarget {
  host: string
  port: number
  user: string
  method: 'password' | 'key'
}

export const getLastTarget = () => read<Partial<LastTarget>>(LAST_TARGET, {})
export const saveLastTarget = (t: LastTarget) => write(LAST_TARGET, t)

export type Layout = 'windows' | 'tabs'

export interface Prefs {
  layout: Layout
  fontSize: number
  sidebar: boolean
  /** Minutes of inactivity before the vault locks; 0 = never. */
  autoLock: number
}

export const getPrefs = (): Prefs => ({ layout: 'windows', fontSize: 13, sidebar: true, autoLock: 15, ...read<Partial<Prefs>>(PREFS, {}) })
export const savePrefs = (p: Prefs) => write(PREFS, p)

export type ThemePref = 'system' | 'light' | 'dark'
export const getThemePref = () => read<ThemePref>('webssh.theme', 'system')
export const saveThemePref = (t: ThemePref) => write('webssh.theme', t)
