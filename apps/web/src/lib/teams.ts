// Teams on the client: the sharing keypair, team keys, and team vaults.
// Team keys are unwrapped in memory while the vault is unlocked and dropped on lock.
import { useSyncExternalStore } from 'react'
import { api, ApiError, type SignedIn } from './api'
import {
  importVaultKey, keyFingerprint, loadSharingKey, newSharingKeypair, randomBytes, unwrapTeamKey, wrapTeamKey, type Bytes,
} from './crypto'
import { closeTeamVaults, syncTeamVaults, type Role, type TeamVaultSpec } from './vault'

export interface Team {
  id: string
  name: string
  role: Role
  requireTotp: boolean
  confirmed: boolean
  wrappedKey?: string
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
export async function startTeams(session: SignedIn, vaultKey: CryptoKey): Promise<SignedIn> {
  let s = session
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
      if (!raw) {
        raw = await unwrapTeamKey(myPrivate, t.wrappedKey)
        rawTeamKeys.set(t.id, raw)
      }
      specs.push({ id: t.id, name: t.name, role: t.role, key: await importVaultKey(raw) })
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

export async function removeMember(teamId: string, userId: string) {
  await api('DELETE', `/api/teams/${teamId}/members/${userId}`)
  await refreshTeams()
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
  await refreshTeams()
}

export const isApiError = (e: unknown, code: string) => e instanceof ApiError && e.code === code
