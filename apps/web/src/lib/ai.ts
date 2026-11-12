// The optional AI helper ("explain this output"). The gateway admin turns it
// on; only text the user selects and confirms is ever sent.
import { useSyncExternalStore } from 'react'
import { api } from './api'

export interface AIStatus {
  enabled: boolean
  model?: string
}

let status: AIStatus = { enabled: false }
const listeners = new Set<() => void>()

export function loadAI() {
  api<AIStatus>('GET', '/api/ai').then(
    (s) => {
      status = s
      listeners.forEach((l) => l())
    },
    () => {},
  )
}

export function useAI(): AIStatus {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => status,
  )
}

export const MAX_EXPLAIN = 16 << 10

export const explain = (text: string, where: string) => api<{ answer: string }>('POST', '/api/ai/explain', { text, where })
