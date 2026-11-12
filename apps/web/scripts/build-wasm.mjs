// Builds the in-browser SSH client (server/cmd/webssh-wasm) for end-to-end
// sessions into public/wasm/, with the matching wasm_exec.js and a gzipped
// copy the gateway serves to browsers that accept it.
// WEBSSH_PREBUILT_WASM=1 skips the Go build (the Docker image builds it in a
// Go stage and copies it in).
import { execFileSync } from 'node:child_process'
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { gzipSync, constants } from 'node:zlib'

const web = fileURLToPath(new URL('..', import.meta.url))
const out = join(web, 'public', 'wasm')
const server = join(web, '..', '..', 'server')
mkdirSync(out, { recursive: true })

if (!process.env.WEBSSH_PREBUILT_WASM) {
  const env = { ...process.env, GOOS: 'js', GOARCH: 'wasm', CGO_ENABLED: '0' }
  execFileSync('go', ['build', '-trimpath', '-ldflags=-s -w', '-o', join(out, 'ssh.wasm'), './cmd/webssh-wasm'], { cwd: server, env, stdio: 'inherit' })
  const goroot = execFileSync('go', ['env', 'GOROOT'], { cwd: server, encoding: 'utf8' }).trim()
  copyFileSync(join(goroot, 'lib', 'wasm', 'wasm_exec.js'), join(out, 'wasm_exec.js'))
}
const wasm = readFileSync(join(out, 'ssh.wasm'))
writeFileSync(join(out, 'ssh.wasm.gz'), gzipSync(wasm, { level: constants.Z_BEST_COMPRESSION }))
console.log(`wasm: ssh.wasm ${(wasm.length / 1e6).toFixed(1)} MB`)
