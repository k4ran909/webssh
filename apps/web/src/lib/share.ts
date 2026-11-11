// Moving a saved host between vaults (personal ↔ team). Teammates can only
// use what's in the team vault, so the host's dependencies come along as
// copies: its identity/key, its jump-host chain, its startup snippet and its
// trusted host key. Personal copies of shared keys stay where they are.
import type { Entry, ItemBody, SavedHost } from './vault'
import { PERSONAL, vaultFor } from './vault'
import type { VaultView } from './resolve'

export interface MovePlan {
  host: SavedHost
  /** Items that will be copied alongside (labels, for the confirmation). */
  copies: string[]
}

type Copy = { from: Entry; body: ItemBody }

function collect(host: SavedHost, v: VaultView, target: string, knownHosts: Entry[]): Copy[] {
  const out: Copy[] = []
  const seen = new Set<string>()
  const all: Entry[] = [...v.hosts, ...v.keys, ...v.identities, ...v.snippets, ...knownHosts]
  const visit = (id: string | undefined) => {
    const e = id ? all.find((x) => x.id === id) : undefined
    if (e) visitEntry(e)
  }
  const visitEntry = (e: Entry) => {
    if (seen.has(e.id)) return
    seen.add(e.id)
    if (e.vault === target) return
    out.push({ from: e, body: e })
    if (e.kind === 'identity') visit(e.keyId)
    if (e.kind === 'host') {
      visit(e.identityId)
      visit(e.keyId)
      visit(e.jumpHostId)
      visit(e.startupSnippetId)
      const kh = knownHosts.find((k) => k.kind === 'knownHost' && k.hostPort === `${e.host.toLowerCase()}:${e.port}`)
      if (kh) visit(kh.id)
    }
  }
  visitEntry(host) // the host as given (it may be newer than the view)
  return out
}

const describe = (e: Entry) =>
  e.kind === 'host' ? `host ${e.label || e.host}`
  : e.kind === 'identity' ? `identity ${e.label}`
  : e.kind === 'key' ? `key ${e.label}`
  : e.kind === 'snippet' ? `snippet ${e.name}`
  : e.kind === 'knownHost' ? `trusted host key for ${e.hostPort}`
  : 'item'

export function planMove(host: SavedHost, v: VaultView, target: string, knownHosts: Entry[]): MovePlan {
  return { host, copies: collect(host, v, target, knownHosts).slice(1).map((c) => describe(c.from)) }
}

/**
 * Copies host + dependencies into the target vault under new ids (references
 * rewritten), then removes the host from its old vault. Returns the new host id.
 */
export async function moveHost(host: SavedHost, v: VaultView, target: string, knownHosts: Entry[]): Promise<string> {
  const copies = collect(host, v, target, knownHosts)
  const ids = new Map(copies.map((c) => [c.from.id, crypto.randomUUID()]))
  const map = (id?: string) => (id ? ids.get(id) ?? id : undefined)
  const dest = vaultFor(target === PERSONAL ? undefined : target)
  // Dependencies first, so the host never points at something missing.
  for (const c of [...copies].reverse()) {
    const b = { ...c.body } as ItemBody & Record<string, unknown>
    delete b.id
    delete b.vault
    if (b.kind === 'host') Object.assign(b, { identityId: map(b.identityId as string), keyId: map(b.keyId as string), jumpHostId: map(b.jumpHostId as string), startupSnippetId: map(b.startupSnippetId as string) })
    if (b.kind === 'identity') b.keyId = map(b.keyId as string)
    await dest.put(b as ItemBody, ids.get(c.from.id))
  }
  await vaultFor(host.vault === PERSONAL ? undefined : host.vault).remove(host.id)
  return ids.get(host.id)!
}
