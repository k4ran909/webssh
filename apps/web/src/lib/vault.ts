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
  /** Credentials from the keychain instead of inline ones. */
  keyId?: string
  identityId?: string
  group?: string
  /** Connect through this saved host first (ProxyJump). */
  jumpHostId?: string
  /** Snippet typed into the shell after connecting. */
  startupSnippetId?: string
  /** Forward an SSH agent holding this connection's keys (opt-in). */
  agent?: boolean
  /** Run SSH in the browser; the gateway only relays encrypted bytes. */
  e2e?: boolean
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
/** A private key in the keychain, reusable across hosts. */
export interface KeyItem {
  kind: 'key'
  label: string
  type: string
  privateKey: string
  passphrase?: string
  publicKey?: string
  fingerprint?: string
}
/** A reusable login: username plus a password or a keychain key. */
export interface IdentityItem {
  kind: 'identity'
  label: string
  username: string
  password?: string
  keyId?: string
}
export type ItemBody = HostItem | SnippetItem | KnownHostItem | KeyItem | IdentityItem
/** `vault` says where the item lives (personal or a team id); it is never encrypted into the item. */
export type Entry<T extends ItemBody = ItemBody> = T & { id: string; vault?: string }

export type SavedHost = Entry<HostItem>
export type Snippet = Entry<SnippetItem>
export type SavedKey = Entry<KeyItem>
export type Identity = Entry<IdentityItem>

export const HOST_COLORS = ['#a3a3a3', '#f87171', '#fbbf24', '#4ade80', '#8ab4f8', '#d7aefb']

export const hasCredentials = (h: HostItem) =>
  !!h.identityId || !!h.keyId || (h.method === 'password' ? !!h.password : !!h.key)


const DEFAULT_SNIPPETS: SnippetItem[] = [
  { kind: 'snippet', name: 'Disk usage', command: 'df -h' },
  { kind: 'snippet', name: 'Memory', command: 'free -h' },
  { kind: 'snippet', name: 'Top processes', command: 'ps aux --sort=-%cpu | head -15' },
  { kind: 'snippet', name: 'Listening ports', command: 'ss -tulpn' },
]

const hostPort = (host: string, port: number) => `${host.toLowerCase()}:${port}`

export const PERSONAL = 'personal'

export type Role = 'owner' | 'admin' | 'member' | 'viewer'

export interface VaultInfo {
  id: string
  name: string
  role: Role
  writable: boolean
  error?: string
}

// All open vaults (personal + teams) share one subscription and one merged snapshot.
const listeners = new Set<() => void>()
let snapshot: { unlocked: boolean; entries: Entry[]; vaults: VaultInfo[]; error?: string } = { unlocked: false, entries: [], vaults: [] }

function publish() {
  const all = [vault, ...teamVaults.values()]
  snapshot = {
    unlocked: vault.isOpen(),
    entries: all.flatMap((v) => v.entries()),
    vaults: all.filter((v) => v.isOpen() || v.error).map((v) => v.info()),
    error: vault.error,
  }
  listeners.forEach((l) => l())
}

class Vault {
  private key: CryptoKey | null = null
  private items = new Map<string, Entry>()
  private revision = 0
  private syncing = false
  private timer = 0
  error?: string
  /** Called when the server says the session is gone. */
  onSignedOut: () => void = () => {}
  /** Called when the gateway's 2FA policy blocks this account. */

  constructor(readonly id: string, private base: string, public name: string, public role: Role) {}

  isOpen = () => !!this.key
  entries = () => [...this.items.values()]
  info = (): VaultInfo => ({ id: this.id, name: this.name, role: this.role, writable: this.role !== 'viewer', error: this.error })

  // Kept for callers that only care about the personal vault.
  subscribe = (l: () => void) => {
    listeners.add(l)
    return () => { listeners.delete(l) }
  }
  getState = () => ({ unlocked: this.isOpen(), entries: this.entries(), error: this.error })

  private emit(error?: string) {
    this.error = error
    publish()
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
      const res = await api<{ items: { id: string; data?: string; revision: number; deleted?: boolean }[]; revision: number; role?: Role }>(
        'GET', `${this.base}?since=${this.revision}`,
      )
      if (key !== this.key) return // locked while the request was in flight
      if (res.role) this.role = res.role
      for (const it of res.items) {
        if (it.deleted || !it.data) {
          this.items.delete(it.id)
          continue
        }
        try {
          const body = await decryptItem<ItemBody>(key, it.id, it.data)
          this.items.set(it.id, { ...body, id: it.id, vault: this.id })
        } catch {
          console.warn('vault: skipping item that fails to decrypt', it.id)
        }
      }
      this.revision = Math.max(this.revision, res.revision)
      this.emit()
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) this.onSignedOut()
      else if (e instanceof ApiError && e.code === 'TOTP_SETUP_REQUIRED') return // the 2FA setup gate takes over
      else this.emit(e instanceof Error ? e.message : 'Sync failed')
    } finally {
      this.syncing = false
    }
  }

  async put(body: ItemBody, id: string = crypto.randomUUID()): Promise<string> {
    const key = this.key
    if (!key) throw new Error('Vault is locked.')
    if (this.role === 'viewer') throw new Error(`You can only view ${this.name}.`)
    // Never store which vault an item came from inside the item itself.
    const { id: _id, vault: _v, ...clean } = body as ItemBody & { id?: string; vault?: string }
    // Optimistic: callers (e.g. host-key trust) read it back synchronously.
    this.items.set(id, { ...(clean as ItemBody), id, vault: this.id })
    this.emit(this.error)
    const data = await encryptItem(key, id, clean)
    const { revision } = await api<{ revision: number }>('PUT', `${this.base}/items/${id}`, { data })
    // Only advance the cursor when nothing from another device landed in between.
    if (revision === this.revision + 1) this.revision = revision
    return id
  }

  async remove(id: string) {
    if (!this.key) return
    this.items.delete(id)
    this.emit(this.error)
    const { revision } = await api<{ revision: number }>('DELETE', `${this.base}/items/${id}`)
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

export const vault = new Vault(PERSONAL, '/api/vault', 'Personal', 'owner')

// ── team vaults ──

const teamVaults = new Map<string, Vault>()

export interface TeamVaultSpec {
  id: string
  name: string
  role: Role
  key: CryptoKey
}

/** Opens new team vaults and closes ones the user no longer has. */
export async function syncTeamVaults(specs: TeamVaultSpec[], errors: Record<string, string> = {}) {
  const keep = new Set(specs.map((s) => s.id))
  for (const [id, v] of teamVaults) {
    if (!keep.has(id) && !errors[id]) {
      v.lock()
      teamVaults.delete(id)
    }
  }
  for (const s of specs) {
    const v = teamVaults.get(s.id)
    if (v) {
      v.name = s.name
      v.role = s.role
      continue
    }
    const nv = new Vault(s.id, `/api/teams/${s.id}/vault`, s.name, s.role)
    teamVaults.set(s.id, nv)
    await nv.open(s.key)
  }
  // Teams we can't open (e.g. their 2FA rule) still show, with the reason.
  for (const [id, msg] of Object.entries(errors)) {
    if (!teamVaults.has(id)) {
      const ev = new Vault(id, `/api/teams/${id}/vault`, msg.split('\n')[0], 'viewer')
      ev.error = msg.split('\n')[1] ?? msg
      teamVaults.set(id, ev)
    }
  }
  publish()
}

export function closeTeamVaults() {
  teamVaults.forEach((v) => v.lock())
  teamVaults.clear()
  publish()
}

export function vaultFor(id: string | undefined): Vault {
  return (id && teamVaults.get(id)) || vault
}

export function useVault() {
  const s = useSyncExternalStore((l) => { listeners.add(l); return () => { listeners.delete(l) } }, () => snapshot)
  const byLabel = (a: { label: string }, b: { label: string }) => a.label.localeCompare(b.label)
  return {
    unlocked: s.unlocked,
    error: s.error,
    vaults: s.vaults,
    hosts: s.entries.filter((e): e is SavedHost => e.kind === 'host').sort((a, b) => (a.label || a.host).localeCompare(b.label || b.host)),
    snippets: s.entries.filter((e): e is Snippet => e.kind === 'snippet'),
    keys: s.entries.filter((e): e is SavedKey => e.kind === 'key').sort(byLabel),
    identities: s.entries.filter((e): e is Identity => e.kind === 'identity').sort(byLabel),
    knownHosts: s.entries.filter((e) => e.kind === 'knownHost'),
  }
}

// Trusted host keys, read synchronously by the terminal. Personal trust wins;
// keys a teammate trusted (shared with the host) count too.
export const getKnownHost = (host: string, port: number) =>
  (vault.knownHost(host, port) ?? [...teamVaults.values()].map((v) => v.knownHost(host, port)).find(Boolean))?.fingerprint

export function saveKnownHost(host: string, port: number, fingerprint: string) {
  vault.put({ kind: 'knownHost', hostPort: hostPort(host, port), fingerprint }, vault.knownHost(host, port)?.id).catch(() => {})
}

export function forgetKnownHost(host: string, port: number) {
  const e = vault.knownHost(host, port)
  if (e) vault.remove(e.id).catch(() => {})
  for (const v of teamVaults.values()) {
    const t = v.knownHost(host, port)
    if (t && v.role !== 'viewer') v.remove(t.id).catch(() => {})
  }
}
