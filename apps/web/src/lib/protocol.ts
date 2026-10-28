// Mirrors server/internal/gateway/protocol.go

export type Auth =
  | { type: 'password'; password: string }
  | { type: 'key'; key: string; passphrase?: string }

export interface Target {
  host: string
  port: number
  user: string
  auth: Auth
}

export type ClientMsg =
  | {
      t: 'connect'
      host: string
      port: number
      user: string
      auth: Auth
      knownHostKey: string
      cols: number
      rows: number
      term: string
    }
  | { t: 'resize'; cols: number; rows: number }
  | { t: 'hostkey-accept' }
  | { t: 'hostkey-reject' }

export type ServerMsg =
  | { t: 'hostkey'; status: 'new' | 'changed'; fingerprint: string; algo: string }
  | { t: 'ready'; fingerprint: string; algo: string }
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
