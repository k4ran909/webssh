// End-to-end sessions: the SSH client runs here, in WebAssembly
// (server/cmd/webssh-wasm), and the gateway only relays the encrypted TCP
// stream. It speaks the gateway's terminal protocol, so a terminal can't tell
// an E2ESocket from a WebSocket to /ws/terminal.

interface WasmTerm {
  send(data: string | Uint8Array): void
  close(): void
}
interface WasmResponse {
  status: number
  headers: Record<string, string>
  body: Uint8Array
}
interface WasmApi {
  open(relayURL: string, onText: (s: string) => void, onBinary: (b: Uint8Array) => void, onClose: () => void): WasmTerm
  sftp(handle: string, method: string, url: string, body?: Uint8Array): Promise<WasmResponse>
}

declare global {
  // Defined by wasm_exec.js and by the WebAssembly module.
  var Go: new () => { importObject: WebAssembly.Imports; run(instance: WebAssembly.Instance): Promise<void> }
  var websshE2E: WasmApi | undefined
}

let loading: Promise<WasmApi> | null = null

function loadScript(src: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const s = document.createElement('script')
    s.src = src
    s.onload = () => resolve()
    s.onerror = () => reject(new Error('Could not load the end-to-end SSH client.'))
    document.head.append(s)
  })
}

/** Loads the WebAssembly SSH client once (about 2.7 MB, gzipped). */
export function loadE2E(): Promise<WasmApi> {
  loading ??= (async () => {
    if (!globalThis.Go) await loadScript('/wasm/wasm_exec.js')
    const go = new globalThis.Go()
    const res = await fetch('/wasm/ssh.wasm')
    if (!res.ok) throw new Error('The end-to-end SSH client is missing from this gateway build.')
    const { instance } = await WebAssembly.instantiate(await res.arrayBuffer(), go.importObject)
    void go.run(instance) // runs main(), which registers websshE2E and then waits forever
    if (!globalThis.websshE2E) throw new Error('The end-to-end SSH client failed to start.')
    return globalThis.websshE2E
  })()
  loading.catch(() => { loading = null })
  return loading
}

function relayURL(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}/ws/relay`
}

/** The subset of WebSocket that TerminalView uses. */
export interface TermSocket {
  readonly readyState: number
  binaryType: BinaryType
  onopen: ((e: Event) => void) | null
  onmessage: ((e: MessageEvent) => void) | null
  onclose: ((e: CloseEvent) => void) | null
  send(data: string | Uint8Array): void
  close(): void
}

export class E2ESocket implements TermSocket {
  readyState: number = WebSocket.CONNECTING
  binaryType: BinaryType = 'arraybuffer'
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onclose: ((e: CloseEvent) => void) | null = null
  private term: WasmTerm | null = null

  constructor() {
    loadE2E().then(
      (api) => {
        if (this.readyState === WebSocket.CLOSED) return
        this.term = api.open(
          relayURL(),
          (s) => this.emit(s),
          (b) => this.emit(b.buffer as ArrayBuffer),
          () => this.closed(),
        )
        this.readyState = WebSocket.OPEN
        this.onopen?.(new Event('open'))
      },
      (e: Error) => {
        this.emit(JSON.stringify({ t: 'error', code: 'E2E_UNAVAILABLE', msg: e.message }))
        this.closed()
      },
    )
  }

  send(data: string | Uint8Array) {
    if (this.readyState === WebSocket.OPEN) this.term?.send(data)
  }

  close() {
    if (this.readyState === WebSocket.CLOSED) return
    this.readyState = WebSocket.CLOSED
    this.term?.close()
  }

  private emit(data: string | ArrayBuffer) {
    this.onmessage?.(new MessageEvent('message', { data }))
  }

  private closed() {
    const was = this.readyState
    this.readyState = WebSocket.CLOSED
    if (was !== WebSocket.CLOSED) this.onclose?.(new CloseEvent('close'))
  }
}

/** True for SFTP handles of end-to-end sessions, which live in this page. */
export const isE2EHandle = (handle?: string) => !!handle?.startsWith('e2e-')

/** Biggest upload for an end-to-end session (it passes through page memory). */
export const E2E_MAX_UPLOAD = 256 << 20

/**
 * fetch() for the file API (/api/sftp/{handle}/…): the gateway for its own
 * sessions, the WebAssembly client for end-to-end ones.
 */
export async function sftpFetch(handle: string, url: string, init: { method?: string; body?: BodyInit | null } = {}): Promise<Response> {
  const method = init.method ?? 'GET'
  if (!isE2EHandle(handle)) return fetch(url, { method, body: init.body, credentials: 'same-origin' })
  const api = await loadE2E()
  let body: Uint8Array | undefined
  if (init.body != null) body = new Uint8Array(await new Response(init.body).arrayBuffer())
  const r = await api.sftp(handle, method, url, body)
  return new Response(r.status === 204 || r.status === 304 ? null : (r.body as BodyInit), { status: r.status, headers: r.headers })
}
