// Encrypted vault: decrypted items live only in memory while unlocked.
import { useSyncExternalStore } from 'react'
import { api, ApiError } from './api'
import { decryptItem, encryptItem } from './crypto'

export type AuthMethod = 'password' | 'key'

export interface HostItem {
  kind: 'host'
  label: string
  color: string
  host: string
  port: number
  user: string
  method: AuthMethod
  /** Optional saved credentials (encrypted with the vault, like everything else). */
  password?: string
  key?: string
  passphrase?: string
}
export interface SnippetItem {
  kind: 'snippet'
  name: string
  command: string
}
export interface KnownHostItem {
  kind: 'knownHost'
  hostPort: string
  fingerprint: string
}
export type ItemBody = HostItem | SnippetItem | KnownHostItem
export type Entry<T extends ItemBody = ItemBody> = T & { id: string }

export type SavedHost = Entry<HostItem>
export type Snippet = Entry<SnippetItem>

export const HOST_COLORS = ['#a3a3a3', '#f87171', '#fbbf24', '#4ade80', '#8ab4f8', '#d7aefb']

export const hasCredentials = (h: HostItem) => (h.method === 'password' ? !!h.password : !!h.key)

interface State {
  unlocked: boolean
  entries: Entry[]
  error?: string
}

const DEFAULT_SNIPPETS: SnippetItem[] = [
  { kind: 'snippet', name: 'Disk usage', command: 'df -h' },
  { kind: 'snippet', name: 'Memory', command: 'free -h' },
  { kind: 'snippet', name: 'Top processes', command: 'ps aux --sort=-%cpu | head -15' },
  { kind: 'snippet', name: 'Listening ports', command: 'ss -tulpn' },
]

const hostPort = (host: string, port: number) => `${host.toLowerCase()}:${port}`

class Vault {
  private key: CryptoKey | null = null
  private items = new Map<string, Entry>()
  private revision = 0
  private syncing = false
  private timer = 0
  private state: State = { unlocked: false, entries: [] }
  private listeners = new Set<() => void>()
  /** Called when the server says the session is gone. */
  onSignedOut: () => void = () => {}

  subscribe = (l: () => void) => {
    this.listeners.add(l)
    return () => { this.listeners.delete(l) }
  }
  getState = () => this.state

  private emit(error?: string) {
    this.state = { unlocked: !!this.key, entries: [...this.items.values()], error }
    this.listeners.forEach((l) => l())
  }

  private onFocus = () => this.sync()

  async open(key: CryptoKey) {
    this.lock()
    this.key = key
    this.emit()
    await this.sync()
    this.timer = window.setInterval(() => this.sync(), 30_000)
    window.addEventListener('focus', this.onFocus)
  }

  lock() {
    this.key = null
    this.items.clear()
    this.revision = 0
    clearInterval(this.timer)
    window.removeEventListener('focus', this.onFocus)
    this.emit()
  }

  async sync() {
    const key = this.key
    if (!key || this.syncing) return
    this.syncing = true
    try {
      const res = await api<{ items: { id: string; data?: string; revision: number; deleted?: boolean }[]; revision: number }>(
        'GET', `/api/vault?since=${this.revision}`,
      )
      if (key !== this.key) return // locked while the request was in flight
      for (const it of res.items) {
        if (it.deleted || !it.data) {
          this.items.delete(it.id)
          continue
        }
        try {
          const body = await decryptItem<ItemBody>(key, it.id, it.data)
          this.items.set(it.id, { ...body, id: it.id })
        } catch {
          console.warn('vault: skipping item that fails to decrypt', it.id)
        }
      }
      this.revision = Math.max(this.revision, res.revision)
      this.emit()
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) this.onSignedOut()
      else this.emit(e instanceof Error ? e.message : 'Sync failed')
    } finally {
      this.syncing = false
    }
  }

  async put(body: ItemBody, id: string = crypto.randomUUID()): Promise<string> {
    const key = this.key
    if (!key) throw new Error('Vault is locked.')
    // Optimistic: callers (e.g. host-key trust) read it back synchronously.
    this.items.set(id, { ...body, id })
    this.emit()
    const data = await encryptItem(key, id, body)
    const { revision } = await api<{ revision: number }>('PUT', `/api/vault/items/${id}`, { data })
    // Only advance the cursor when nothing from another device landed in between.
    if (revision === this.revision + 1) this.revision = revision
    return id
  }

  async remove(id: string) {
    if (!this.key) return
    this.items.delete(id)
    this.emit()
    const { revision } = await api<{ revision: number }>('DELETE', `/api/vault/items/${id}`)
    if (revision === this.revision + 1) this.revision = revision
  }

  knownHost(host: string, port: number): Entry<KnownHostItem> | undefined {
    const hp = hostPort(host, port)
    for (const e of this.items.values()) if (e.kind === 'knownHost' && e.hostPort === hp) return e
  }

  async seedDefaults() {
    await Promise.all(DEFAULT_SNIPPETS.map((s) => this.put(s)))
  }

  /** One-time move of pre-vault localStorage data (hosts, snippets, trusted keys). */
  async importLegacy(): Promise<number> {
    const take = <T>(k: string): T | null => {
      try {
        const raw = localStorage.getItem(k)
        return raw ? (JSON.parse(raw) as T) : null
      } catch {
        return null
      }
    }
    const hosts = take<{ label: string; color: string; host: string; port: number; user: string; method: AuthMethod }[]>('webssh.hosts')
    const snippets = take<{ name: string; command: string }[]>('webssh.snippets')
    const known = take<Record<string, string>>('webssh.knownHosts')
    const jobs: Promise<unknown>[] = []
    hosts?.forEach((h) => jobs.push(this.put({ kind: 'host', label: h.label, color: h.color, host: h.host, port: h.port, user: h.user, method: h.method })))
    snippets?.forEach((s) => jobs.push(this.put({ kind: 'snippet', name: s.name, command: s.command })))
    Object.entries(known ?? {}).forEach(([hp, fp]) => {
      const [h, p] = [hp.slice(0, hp.lastIndexOf(':')), Number(hp.slice(hp.lastIndexOf(':') + 1))]
      if (!this.knownHost(h, p)) jobs.push(this.put({ kind: 'knownHost', hostPort: hp, fingerprint: fp }))
    })
    await Promise.all(jobs)
    try {
      ;['webssh.hosts', 'webssh.snippets', 'webssh.knownHosts'].forEach((k) => localStorage.removeItem(k))
    } catch {
      // storage blocked: nothing to clean
    }
    return hosts?.length ?? 0
  }
}

export const vault = new Vault()

export function useVault() {
  const s = useSyncExternalStore(vault.subscribe, vault.getState)
  return {
    unlocked: s.unlocked,
    error: s.error,
    hosts: s.entries.filter((e): e is SavedHost => e.kind === 'host').sort((a, b) => (a.label || a.host).localeCompare(b.label || b.host)),
    snippets: s.entries.filter((e): e is Snippet => e.kind === 'snippet'),
  }
}

// Trusted host keys, read synchronously by the terminal.
export const getKnownHost = (host: string, port: number) => vault.knownHost(host, port)?.fingerprint

export function saveKnownHost(host: string, port: number, fingerprint: string) {
  vault.put({ kind: 'knownHost', hostPort: hostPort(host, port), fingerprint }, vault.knownHost(host, port)?.id).catch(() => {})
}

export function forgetKnownHost(host: string, port: number) {
  const e = vault.knownHost(host, port)
  if (e) vault.remove(e.id).catch(() => {})
}
