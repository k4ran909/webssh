// Turns a saved host (plus keychain references and jump hosts) into a Target.
import type { Auth, Endpoint, Target } from './protocol'
import type { Identity, SavedHost, SavedKey, Snippet } from './vault'
import { MAX_JUMPS } from './limits'

export interface VaultView {
  hosts: SavedHost[]
  keys: SavedKey[]
  identities: Identity[]
  snippets: Snippet[]
}

export type Resolved =
  | { ok: true; target: Target; title: string }
  /** `host` is the one (target or a jump host) that needs attention. */
  | { ok: false; reason: 'credentials' | 'missing-ref' | 'loop'; host: SavedHost; message: string }

const label = (h: SavedHost) => h.label || h.host

function keyAuth(k: SavedKey | undefined): Auth | undefined {
  return k ? { type: 'key', key: k.privateKey, passphrase: k.passphrase } : undefined
}

/** Username and credential for one host, or undefined when none are saved. */
export function credentialsFor(h: SavedHost, v: VaultView): { user: string; auth: Auth } | undefined | 'missing-ref' {
  if (h.identityId) {
    const id = v.identities.find((i) => i.id === h.identityId)
    if (!id) return 'missing-ref'
    if (id.keyId) {
      const auth = keyAuth(v.keys.find((k) => k.id === id.keyId))
      return auth ? { user: id.username, auth } : 'missing-ref'
    }
    return id.password ? { user: id.username, auth: { type: 'password', password: id.password } } : undefined
  }
  if (h.keyId) {
    const auth = keyAuth(v.keys.find((k) => k.id === h.keyId))
    return auth ? { user: h.user, auth } : 'missing-ref'
  }
  if (h.method === 'password' && h.password) return { user: h.user, auth: { type: 'password', password: h.password } }
  if (h.method === 'key' && h.key) return { user: h.user, auth: { type: 'key', key: h.key, passphrase: h.passphrase } }
  return undefined
}

/**
 * Resolves a host's jump chain (outermost first) and credentials. `override`
 * supplies credentials typed in for the target itself.
 */
export function resolveHost(target: SavedHost, v: VaultView, override?: { user: string; auth: Auth }): Resolved {
  const chain: SavedHost[] = []
  const seen = new Set([target.id])
  for (let h = target; h.jumpHostId; ) {
    const j = v.hosts.find((x) => x.id === h.jumpHostId)
    if (!j) return { ok: false, reason: 'missing-ref', host: h, message: `The jump host for ${label(h)} was deleted.` }
    if (seen.has(j.id) || chain.length >= MAX_JUMPS) return { ok: false, reason: 'loop', host: h, message: `Jump hosts for ${label(target)} loop or go deeper than ${MAX_JUMPS}.` }
    seen.add(j.id)
    chain.unshift(j)
    h = j
  }

  const jump: (Endpoint & { label: string })[] = []
  for (const j of chain) {
    const c = credentialsFor(j, v)
    if (c === 'missing-ref') return { ok: false, reason: 'missing-ref', host: j, message: `The keychain entry used by ${label(j)} was deleted.` }
    if (!c) return { ok: false, reason: 'credentials', host: j, message: `Save credentials for the jump host ${label(j)} first.` }
    jump.push({ host: j.host, port: j.port, user: c.user, auth: c.auth, label: label(j) })
  }

  const c = override ?? credentialsFor(target, v)
  if (c === 'missing-ref') return { ok: false, reason: 'missing-ref', host: target, message: `The keychain entry used by ${label(target)} was deleted.` }
  if (!c) return { ok: false, reason: 'credentials', host: target, message: `Enter credentials for ${label(target)}.` }

  const startup = v.snippets.find((s) => s.id === target.startupSnippetId)?.command
  return {
    ok: true,
    title: label(target),
    target: { host: target.host, port: target.port, user: c.user, auth: c.auth, jump: jump.length ? jump : undefined, startup },
  }
}
