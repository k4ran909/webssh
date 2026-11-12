// Mirrors server/internal/gateway/protocol.go

export type Auth =
  | { type: 'password'; password: string }
  | { type: 'key'; key: string; passphrase?: string }

export interface Endpoint {
  host: string
  port: number
  user: string
  auth: Auth
}

export interface Target extends Endpoint {
  /** Bastions to hop through, outermost first (ProxyJump). */
  jump?: (Endpoint & { label?: string })[]
  /** Command typed into the shell once it's ready. */
  startup?: string
  /** Forward an SSH agent holding this connection's keys. */
  agent?: boolean
  /** End-to-end: SSH runs in this page and the gateway only relays bytes. */
  e2e?: boolean
  /** Telnet instead of SSH; auth is ignored. */
  proto?: 'telnet'
}

type WireHop = Endpoint & { knownHostKey: string }

export type ClientMsg =
  | (WireHop & { t: 'connect'; proto?: 'telnet'; jump?: WireHop[]; agent?: boolean; cols: number; rows: number; term: string })
  | { t: 'resume'; token: string; offset: number; cols: number; rows: number }
  | { t: 'resize'; cols: number; rows: number }
  | { t: 'hostkey-accept' }
  | { t: 'hostkey-reject' }

export type ServerMsg =
  | { t: 'hostkey'; status: 'new' | 'changed'; host: string; port: number; fingerprint: string; algo: string }
  | { t: 'ready'; fingerprint: string; algo: string; sftp?: string; resume?: string }
  | { t: 'resumed'; offset?: number; gap?: boolean; sftp?: string }
  | { t: 'exit'; exitCode: number }
  | { t: 'error'; code: string; msg: string }

export function gatewayURL(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}/ws/terminal`
}

const ALGO_LABELS: Record<string, string> = {
  'ssh-ed25519': 'ED25519',
  'ssh-rsa': 'RSA',
  'rsa-sha2-256': 'RSA',
  'rsa-sha2-512': 'RSA',
  'ecdsa-sha2-nistp256': 'ECDSA',
  'ecdsa-sha2-nistp384': 'ECDSA',
  'ecdsa-sha2-nistp521': 'ECDSA',
}

export const algoLabel = (algo: string) => ALGO_LABELS[algo] ?? algo
