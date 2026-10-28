// Phase 0/1 stand-in for the encrypted vault: only non-secret data lives here.
// Passwords and keys are never written to storage.

const KNOWN_HOSTS = 'webssh.knownHosts'
const LAST_TARGET = 'webssh.last'
const HOSTS = 'webssh.hosts'
const SNIPPETS = 'webssh.snippets'
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

export type AuthMethod = 'password' | 'key'

export interface LastTarget {
  host: string
  port: number
  user: string
  method: AuthMethod
}

export const getLastTarget = () => read<Partial<LastTarget>>(LAST_TARGET, {})
export const saveLastTarget = (t: LastTarget) => write(LAST_TARGET, t)

export interface SavedHost extends LastTarget {
  id: string
  label: string
  color: string
}

export const HOST_COLORS = ['#a3a3a3', '#f87171', '#fbbf24', '#4ade80', '#8ab4f8', '#d7aefb']

export const getHosts = () => read<SavedHost[]>(HOSTS, [])
export const saveHosts = (hosts: SavedHost[]) => write(HOSTS, hosts)

export interface Snippet {
  id: string
  name: string
  command: string
}

const DEFAULT_SNIPPETS: Snippet[] = [
  { id: 'disk', name: 'Disk usage', command: 'df -h' },
  { id: 'mem', name: 'Memory', command: 'free -h' },
  { id: 'top', name: 'Top processes', command: 'ps aux --sort=-%cpu | head -15' },
  { id: 'ports', name: 'Listening ports', command: 'ss -tulpn' },
]

export const getSnippets = () => read<Snippet[]>(SNIPPETS, DEFAULT_SNIPPETS)
export const saveSnippets = (s: Snippet[]) => write(SNIPPETS, s)

export type Layout = 'windows' | 'tabs'

export interface Prefs {
  layout: Layout
  fontSize: number
  sidebar: boolean
}

export const getPrefs = (): Prefs => ({ layout: 'windows', fontSize: 13, sidebar: true, ...read<Partial<Prefs>>(PREFS, {}) })
export const savePrefs = (p: Prefs) => write(PREFS, p)

export type ThemePref = 'system' | 'light' | 'dark'
export const getThemePref = () => read<ThemePref>('webssh.theme', 'system')
export const saveThemePref = (t: ThemePref) => write('webssh.theme', t)
