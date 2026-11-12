// Command history for autocomplete. Kept as one item in the personal vault,
// so it is end-to-end encrypted and follows you between devices. Only lines
// the server echoed back are recorded (a password typed at a prompt isn't
// echoed), and, as in shells, lines starting with a space are skipped.
import { getPrefs } from './storage'
import { vault, type HistoryItem } from './vault'

const MAX = 500
let pending: string[] | null = null
let timer = 0

function current(): string[] {
  return pending ?? vault.history()?.commands ?? []
}

export function recordCommand(cmd: string) {
  if (!getPrefs().suggest) return
  const c = cmd.trim()
  if (!c || cmd.startsWith(' ') || c.length > 1000) return
  pending = [c, ...current().filter((x) => x !== c)].slice(0, MAX)
  clearTimeout(timer)
  // Batch writes: one vault update per burst of commands.
  timer = window.setTimeout(() => {
    const body: HistoryItem = { kind: 'history', commands: pending ?? [] }
    pending = null
    vault.put(body, vault.history()?.id).catch(() => {})
  }, 3000)
}

export function clearHistory() {
  clearTimeout(timer)
  pending = null
  const h = vault.history()
  if (h) vault.remove(h.id).catch(() => {})
}

/** Up to n completions for what's typed so far: history first, then snippets. */
export function suggest(prefix: string, snippets: string[], n = 3): string[] {
  if (prefix.length < 2 || !getPrefs().suggest) return []
  const out: string[] = []
  for (const c of [...current(), ...snippets]) {
    if (c.length > prefix.length && c.startsWith(prefix) && !out.includes(c)) out.push(c)
    if (out.length >= n) break
  }
  return out
}
