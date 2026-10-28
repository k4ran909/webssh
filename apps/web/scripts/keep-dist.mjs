// vite empties the embed dir on build; restore the tracked placeholder so
// `go build` works on a fresh clone and git stays clean.
import { writeFileSync } from 'node:fs'

writeFileSync(new URL('../../../server/internal/web/dist/.gitkeep', import.meta.url), '')
