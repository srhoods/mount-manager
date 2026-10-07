import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { readdirSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'

const outDir = resolve(__dirname, '../server/internal/ui/dist')
// The version this bundle was built as; compared with the server's at run time to spot a stale browser tab after an upgrade.
const appVersion = (() => { try { return readFileSync(resolve(__dirname, '../VERSION'), 'utf8').trim() } catch { return 'dev' } })()

// Clear the previous bundle but keep the tracked dist/.gitkeep placeholder (Vite's emptyOutDir would delete it).
const cleanDist = () => ({
  name: 'clean-dist',
  buildStart() {
    try { for (const f of readdirSync(outDir)) if (f !== '.gitkeep') rmSync(join(outDir, f), { recursive: true, force: true }) } catch { /* not created yet */ }
  },
})

// Build output is embedded into the Go server binary (server/internal/ui).
export default defineConfig({
  plugins: [react(), cleanDist()],
  define: { __APP_VERSION__: JSON.stringify(appVersion) },
  build: { outDir, emptyOutDir: false },
  server: { proxy: { '/api': { target: 'https://10.10.10.76:8444', secure: false } } },
})
