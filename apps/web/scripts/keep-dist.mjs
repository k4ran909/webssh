// vite empties the embed dir on build; restore the tracked placeholder so
// `go build` works on a fresh clone and git stays clean.
import { existsSync, rmSync, writeFileSync } from 'node:fs'

writeFileSync(new URL('../../../server/internal/web/dist/.gitkeep', import.meta.url), '')

// The gateway serves the WebAssembly client from its gzipped copy only; don't
// embed the 10 MB original in the binary too.
const wasm = new URL('../../../server/internal/web/dist/wasm/ssh.wasm', import.meta.url)
if (existsSync(new URL('ssh.wasm.gz', wasm))) rmSync(wasm)
