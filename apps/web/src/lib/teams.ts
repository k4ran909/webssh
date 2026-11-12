// Teams on the client: the sharing keypair, team keys, and team vaults.
// Team keys are unwrapped in memory while the vault is unlocked and dropped on lock.
import { useSyncExternalStore } from 'react'
import { api, ApiError, type SignedIn } from './api'
import {
  decryptItem, encryptItem, importVaultKey, keyFingerprint, loadSharingKey, newSharingKeypair, randomBytes, unwrapTeamKey, wrapTeamKey, type Bytes,
} from './crypto'
import { closeTeamVaults, setOnKeyRotated, syncTeamVaults, type Role, type TeamVaultSpec } from './vault'

export interface Team {
  id: string
  name: string
  role: Role
  requireTotp: boolean
  confirmed: boolean
  wrappedKey?: string
  /** Bumped each time the team key is rotated. */
  keyEpoch: number
}

export interface Member {
  userId: string
  email: string
  role: Role
  confirmed: boolean
  publicKey?: string
  totp: boolean
}

export interface PendingInvite {
  email: string
  role: Role
  expiresAt: string
}

interface State {
  teams: Team[]
  myFingerprint: string
  ready: boolean
}

let state: State = { teams: [], myFingerprint: '', ready: false }
const listeners = new Set<() => void>()
const set = (p: Partial<State>) => {
  state = { ...state, ...p }
  listeners.forEach((l) => l())
}

let myPrivate: CryptoKey | null = null
let myPublic = ''
const rawTeamKeys = new Map<string, Bytes>()
let timer = 0

export const useTeams = () =>
  useSyncExternalStore((l) => { listeners.add(l); return () => { listeners.delete(l) } }, () => state)

/**
 * After unlock: load (or create, for accounts made before teams) the sharing
 * keypair, then open every team vault we hold a key for.
 */
let myUserId = ''
/** Which epoch each cached team key belongs to. */
const keyEpochs = new Map<string, number>()
setOnKeyRotated(() => { refreshTeams().catch(() => {}) })

export async function startTeams(session: SignedIn, vaultKey: CryptoKey): Promise<SignedIn> {
  let s = session
  myUserId = s.user.id
  if (s.keypair) {
    myPrivate = await loadSharingKey(vaultKey, s.keypair.encPrivateKey)
    myPublic = s.keypair.publicKey
  } else {
    const kp = await newSharingKeypair(vaultKey)
    await api('PUT', '/api/me/keypair', { publicKey: kp.publicKey, encPrivateKey: kp.encPrivateKey })
    myPrivate = kp.privateKey
    myPublic = kp.publicKey
    s = { ...s, keypair: { publicKey: kp.publicKey, encPrivateKey: kp.encPrivateKey } }
  }
  set({ myFingerprint: await keyFingerprint(myPublic), ready: true })
  await refreshTeams()
  clearInterval(timer)
  // Picks up confirmations and new teams from other people.
  timer = window.setInterval(() => refreshTeams().catch(() => {}), 60_000)
  return s
}

export function stopTeams() {
  clearInterval(timer)
  myPrivate = null
  myPublic = ''
  rawTeamKeys.forEach((k) => k.fill(0))
  rawTeamKeys.clear()
  closeTeamVaults()
  set({ teams: [], myFingerprint: '', ready: false })
}

export async function refreshTeams() {
  if (!myPrivate) return
  const { teams } = await api<{ teams: Team[] }>('GET', '/api/teams')
  const specs: TeamVaultSpec[] = []
  const errors: Record<string, string> = {}
  for (const t of teams) {
    if (!t.confirmed || !t.wrappedKey) continue
    try {
      let raw = rawTeamKeys.get(t.id)
      if (!raw || keyEpochs.get(t.id) !== t.keyEpoch) {
        raw = await unwrapTeamKey(myPrivate, t.wrappedKey)
        rawTeamKeys.set(t.id, raw)
        keyEpochs.set(t.id, t.keyEpoch)
      }
      specs.push({ id: t.id, name: t.name, role: t.role, key: await importVaultKey(raw), epoch: t.keyEpoch })
    } catch {
      errors[t.id] = `${t.name}\nCan't decrypt this team's key. Ask a team admin to confirm you again.`
    }
  }
  // Forget keys of teams we left or were removed from.
  for (const [id, k] of rawTeamKeys) {
    if (!teams.some((t) => t.id === id)) {
      k.fill(0)
      rawTeamKeys.delete(id)
    }
  }
  set({ teams })
  await syncTeamVaults(specs, errors)
}

export async function createTeam(name: string) {
  const raw = randomBytes(32) as Bytes
  const wrappedKey = await wrapTeamKey(raw, myPublic)
  const { id } = await api<{ id: string }>('POST', '/api/teams', { name, wrappedKey })
  rawTeamKeys.set(id, raw)
  keyEpochs.set(id, 0)
  await refreshTeams()
  return id
}

export const fingerprint = keyFingerprint

/** Hands the team key to a member, after the admin has checked their fingerprint. */
export async function confirmMember(teamId: string, m: Member) {
  const raw = rawTeamKeys.get(teamId)
  if (!raw) throw new Error("You don't hold this team's key yet.")
  if (!m.publicKey) throw new Error("This member hasn't unlocked webssh yet, so there's no key to encrypt to.")
  await api('POST', `/api/teams/${teamId}/members/${m.userId}/confirm`, { wrappedKey: await wrapTeamKey(raw, m.publicKey) })
}

export const members = (teamId: string) =>
  api<{ members: Member[]; invites?: PendingInvite[] }>('GET', `/api/teams/${teamId}/members`)

export const addMember = (teamId: string, email: string, role: Role) =>
  api<{ status: 'added' | 'invited'; link?: string }>('POST', `/api/teams/${teamId}/members`, { email, role })

export const setRole = (teamId: string, userId: string, role: Role) => api('PATCH', `/api/teams/${teamId}/members/${userId}`, { role })

/**
 * Removes someone. When an admin removes a confirmed member, the team key is
 * rotated straight away so the new key (and anything saved from now on) never
 * reaches them. Returns whether the key was rotated.
 */
export async function removeMember(teamId: string, userId: string, wasConfirmed = false): Promise<boolean> {
  await api('DELETE', `/api/teams/${teamId}/members/${userId}`)
  if (userId === myUserId || !wasConfirmed) {
    await refreshTeams()
    return false
  }
  await rotateTeamKey(teamId)
  return true
}

/**
 * Replaces the team key: every item is re-encrypted under a fresh key, which
 * is wrapped for each confirmed member. The server applies it only if nothing
 * changed meanwhile; on a clash this tries again with fresh data.
 */
export async function rotateTeamKey(teamId: string) {
  for (let attempt = 0; ; attempt++) {
    const oldRaw = rawTeamKeys.get(teamId)
    if (!oldRaw) throw new Error("You don't hold this team's key.")
    const { teams } = await api<{ teams: Team[] }>('GET', '/api/teams')
    const team = teams.find((t) => t.id === teamId)
    if (!team) throw new Error('That team is gone.')
    const [{ members: ms }, vaultData] = await Promise.all([
      members(teamId),
      api<{ items: { id: string; data: string; deleted?: boolean }[]; revision: number }>('GET', `/api/teams/${teamId}/vault?since=0`),
    ])
    const oldKey = await importVaultKey(oldRaw)
    const raw = randomBytes(32) as Bytes
    const newKey = await importVaultKey(raw)
    const items: { id: string; data: string }[] = []
    for (const it of vaultData.items) {
      if (it.deleted || !it.data) continue
      items.push({ id: it.id, data: await encryptItem(newKey, it.id, await decryptItem(oldKey, it.id, it.data)) })
    }
    const keys: Record<string, string> = {}
    for (const m of ms) {
      if (!m.confirmed) continue
      if (!m.publicKey) throw new Error(`${m.email} has no key to encrypt to.`)
      keys[m.userId] = await wrapTeamKey(raw, m.publicKey)
    }
    try {
      await api('POST', `/api/teams/${teamId}/rotate`, { epoch: team.keyEpoch, revision: vaultData.revision, items, keys })
    } catch (e) {
      if (isApiError(e, 'ROTATE_CONFLICT') && attempt < 3) continue
      throw e
    }
    oldRaw.fill(0)
    rawTeamKeys.set(teamId, raw)
    keyEpochs.set(teamId, team.keyEpoch + 1)
    await refreshTeams()
    return
  }
}

export const revokeInvite = (teamId: string, email: string) => api('DELETE', `/api/teams/${teamId}/invites`, { email })

export async function setTeamRequireTotp(teamId: string, on: boolean) {
  await api('PATCH', `/api/teams/${teamId}`, { requireTotp: on })
  await refreshTeams()
}

export async function deleteTeam(teamId: string) {
  await api('DELETE', `/api/teams/${teamId}`)
  rawTeamKeys.get(teamId)?.fill(0)
  rawTeamKeys.delete(teamId)
  keyEpochs.delete(teamId)
  await refreshTeams()
}

export const isApiError = (e: unknown, code: string) => e instanceof ApiError && e.code === code
