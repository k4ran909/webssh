// Registry of mounted terminals, so the workspace can route input (snippets,
// broadcast) and focus without threading refs through React.
import { useSyncExternalStore } from 'react'

export interface LiveSession {
  isReady(): boolean
  /** Raw bytes to this session's PTY. */
  write(data: Uint8Array): void
  focus(): void
  openSearch(): void
  startRecording(): void
  /** Returns the asciicast text, or null if nothing was being recorded. */
  stopRecording(): string | null
}

export const liveSessions = new Map<string, LiveSession>()

const enc = new TextEncoder()

export function readySessions(): LiveSession[] {
  return [...liveSessions.values()].filter((s) => s.isReady())
}

/** Types text into one session, or every connected session when broadcasting. */
export function sendText(text: string, focusedId: string | null, broadcast: boolean): number {
  const targets = broadcast ? readySessions() : [liveSessions.get(focusedId ?? '')].filter((s) => s?.isReady())
  const bytes = enc.encode(text)
  targets.forEach((s) => s!.write(bytes))
  return targets.length
}

/** Plain credential cache for this page load only, so reopening a host doesn't re-prompt. */
export const credentialCache = new Map<string, import('./protocol').Auth>()
export const credKey = (host: string, port: number, user: string) => `${user}@${host.toLowerCase()}:${port}`

// ── Sticky Ctrl / Alt for the on-screen key bar (phones have neither key) ──

export interface Modifiers { ctrl: boolean; alt: boolean }
let mods: Modifiers = { ctrl: false, alt: false }
const modListeners = new Set<() => void>()

export function setModifiers(next: Partial<Modifiers>) {
  mods = { ...mods, ...next }
  modListeners.forEach((l) => l())
}

export const useModifiers = () =>
  useSyncExternalStore((l) => { modListeners.add(l); return () => { modListeners.delete(l) } }, () => mods)

/** Applies (and clears) pending sticky modifiers to the next typed key. */
export function applyModifiers(data: string): string {
  if (!mods.ctrl && !mods.alt) return data
  let out = data
  if (mods.ctrl && data.length === 1) {
    const c = data.toUpperCase().charCodeAt(0)
    if (c >= 0x40 && c <= 0x5f) out = String.fromCharCode(c & 0x1f) // Ctrl+A..Z, @ [ \ ] ^ _
    else if (data === ' ') out = '\x00'
  }
  if (mods.alt) out = '\x1b' + out
  setModifiers({ ctrl: false, alt: false })
  return out
}
