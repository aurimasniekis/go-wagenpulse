import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import { fileURLToPath } from 'node:url'

// A running `wagenpulse serve` (make dev) answers the API on 127.0.0.1:8080.
const api = process.env.WAGENPULSE_API ?? 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [tanstackRouter({ target: 'react', autoCodeSplitting: true }), react()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    // Embedded into the Go binary (internal/webui). public/README.txt keeps `go:embed all:dist` compiling.
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
    // The CSP forbids inline data: scripts; keep every asset a file.
    assetsInlineLimit: 0,
  },
  server: {
    proxy: { '/api': api, '/auth': api },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
