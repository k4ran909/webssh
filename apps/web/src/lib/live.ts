// Registry of mounted terminals, so the workspace can route input (snippets,
// broadcast) and focus without threading refs through React.

export interface LiveSession {
  isReady(): boolean
  /** Raw bytes to this session's PTY. */
  write(data: Uint8Array): void
  focus(): void
  openSearch(): void
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
