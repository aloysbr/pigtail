import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'
import { inlineBoot } from './src/theme/boot-plugin'

/** Served by pigtaild at / and /assets/* (hashed names); `make web` copies dist/ into internal/webui/dist. */
export default defineConfig({
  plugins: [inlineBoot(), react(), tailwindcss()],
  base: '/',
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: { host: '127.0.0.1' },
  preview: { host: '127.0.0.1' },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    restoreMocks: true,
    // Most tests render the whole app in jsdom: ~1 s alone, past the 5 s
    // default when 95 files run in parallel on a loaded machine (pre-push).
    testTimeout: 15_000,
  },
})
