// asciicast v2 (https://docs.asciinema.org/manual/asciicast/v2/): recording
// happens in the browser, so recordings never touch the gateway or the vault.

export class CastRecorder {
  private dec = new TextDecoder('utf-8')
  private t0 = performance.now()
  private lines: string[]

  constructor(cols: number, rows: number, title: string) {
    this.lines = [JSON.stringify({ version: 2, width: cols, height: rows, timestamp: Math.floor(Date.now() / 1000), title, env: { TERM: 'xterm-256color' } })]
  }

  private at() {
    return Math.round(performance.now() - this.t0) / 1000
  }

  output(bytes: Uint8Array) {
    const s = this.dec.decode(bytes, { stream: true }) // keeps split UTF-8 sequences intact
    if (s) this.lines.push(JSON.stringify([this.at(), 'o', s]))
  }

  resize(cols: number, rows: number) {
    this.lines.push(JSON.stringify([this.at(), 'r', `${cols}x${rows}`]))
  }

  finish(): string {
    return this.lines.join('\n') + '\n'
  }
}

export interface Cast {
  width: number
  height: number
  title?: string
  events: { t: number; type: 'o' | 'r'; data: string }[]
  duration: number
}

export function parseCast(text: string): Cast {
  const lines = text.split('\n').filter((l) => l.trim())
  if (!lines.length) throw new Error('Empty file.')
  let header: { version?: number; width?: number; height?: number; title?: string }
  try {
    header = JSON.parse(lines[0])
  } catch {
    throw new Error("That isn't an asciicast recording.")
  }
  if (header.version !== 2 || !header.width || !header.height) throw new Error('Only asciicast v2 recordings are supported.')
  const events: Cast['events'] = []
  for (const l of lines.slice(1)) {
    try {
      const [t, type, data] = JSON.parse(l)
      if ((type === 'o' || type === 'r') && typeof t === 'number' && typeof data === 'string') events.push({ t, type, data })
    } catch {
      // skip malformed lines
    }
  }
  return { width: header.width, height: header.height, title: header.title, events, duration: events.at(-1)?.t ?? 0 }
}

export function downloadText(name: string, text: string, type = 'application/x-asciicast') {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}
