import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Dev API proxy target; override with ND_API_TARGET=http://localhost:9999 npm run dev.
// Do not default to :8080 because it may be occupied by an SSH tunnel to an old backend.
const apiTarget = process.env.ND_API_TARGET || 'http://localhost:8091';

// Build assets are embedded by go:embed (web/embed.go → all:dist) and served same-origin; use relative base paths.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    proxy: {
      '/api': apiTarget,
      '/boot': apiTarget,
      '/healthz': apiTarget,
    },
  },
  // Run tests under jsdom to validate clicks, polling and counters as browser behavior.
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.jsx'],
  },
});
