// Minimal ~/.ssh/config reader for importing hosts. Handles Host blocks with
// HostName, User, Port, ProxyJump and IdentityFile; patterns (*, ?, !) and
// Match blocks are skipped, and `Host *` values become defaults.

export interface ConfigHost {
  alias: string
  host: string
  port: number
  user?: string
  /** Alias (or first hop) of a ProxyJump chain. */
  proxyJump?: string
  identityFile?: string
}

export interface ParseResult {
  hosts: ConfigHost[]
  warnings: string[]
}

const isPattern = (s: string) => /[*?!]/.test(s)

export function parseSshConfig(text: string): ParseResult {
  const warnings: string[] = []
  const defaults: Record<string, string> = {}
  const blocks: { aliases: string[]; opts: Record<string, string> }[] = []
  let cur: Record<string, string> | null = defaults
  let skipping = false

  for (const [i, raw] of text.split(/\r?\n/).entries()) {
    const line = raw.replace(/\s+#.*$/, '').trim()
    if (!line || line.startsWith('#')) continue
    const m = line.match(/^(\S+?)\s*(?:=|\s)\s*(.+)$/)
    if (!m) continue
    const key = m[1].toLowerCase()
    const value = m[2].replace(/^"(.*)"$/, '$1')

    if (key === 'host') {
      const names = value.split(/\s+/)
      skipping = false
      if (names.every((n) => n === '*')) {
        cur = defaults
      } else {
        const aliases = names.filter((n) => !isPattern(n))
        if (!aliases.length) {
          skipping = true
          cur = null
          continue
        }
        const block = { aliases, opts: {} as Record<string, string> }
        blocks.push(block)
        cur = block.opts
      }
      continue
    }
    if (key === 'match') {
      warnings.push(`Line ${i + 1}: Match blocks aren't supported; skipped.`)
      skipping = true
      cur = null
      continue
    }
    if (skipping || !cur) continue
    // First value wins, like ssh itself.
    if (!(key in cur)) cur[key] = value
  }

  const hosts: ConfigHost[] = []
  for (const b of blocks) {
    const o = { ...defaults, ...b.opts }
    for (const alias of b.aliases) {
      const port = Number(o.port ?? 22)
      let proxyJump: string | undefined
      if (o.proxyjump && o.proxyjump.toLowerCase() !== 'none') {
        const hops = o.proxyjump.split(',')
        if (hops.length > 1) warnings.push(`${alias}: multi-hop ProxyJump; using ${hops[hops.length - 1]} (set its own jump host to chain further).`)
        proxyJump = hops[hops.length - 1].trim()
      }
      hosts.push({
        alias,
        host: (o.hostname ?? alias).replace(/%h/g, alias),
        port: Number.isInteger(port) && port > 0 && port < 65536 ? port : 22,
        user: o.user,
        proxyJump,
        identityFile: o.identityfile,
      })
    }
  }
  return { hosts, warnings }
}
