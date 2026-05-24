import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'lcov'],
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/test/**', 'src/**/*.d.ts', 'src/main.tsx'],
    },
  },
  resolve: {
    alias: [
      {
        find: /^.*\/bindings\/github\.com\/FPGSchiba\/vcs-srs-server\//,
        replacement: `${path.resolve(__dirname, 'src/test/mocks')}/`,
      },
      {
        // @wailsio/runtime/types/events is not in the package exports map.
        // Redirect to the dist JS file so Vite resolves it; vi.mock in setup.ts overrides it for tests.
        find: '@wailsio/runtime/types/events',
        replacement: path.resolve(__dirname, 'node_modules/@wailsio/runtime/dist/events.js'),
      },
    ],
  },
})
