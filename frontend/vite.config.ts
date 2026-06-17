import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react-swc';

const backendTarget = process.env.VITE_BACKEND_URL || 'http://backend:8081';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      // @mui/material >=9.1 imports `react-transition-group/TransitionGroupContext`
      // as a bare directory specifier, but `react-transition-group` (still at 4.4.5)
      // ships no `exports` field, so Node ESM throws ERR_UNSUPPORTED_DIR_IMPORT
      // under vitest. Map the spec to its CJS file until either MUI changes the
      // import path or react-transition-group ships a modern exports map.
      'react-transition-group/TransitionGroupContext':
        'react-transition-group/cjs/TransitionGroupContext.js',
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    css: true,
    exclude: ['e2e/**', 'node_modules/**'],
    testTimeout: 15000,
    // Inline-bundle @mui/material so the bare directory import of
    // `react-transition-group/TransitionGroupContext` (in MUI ≥9.1's
    // Transition.mjs) gets resolved by Vite (which honors the alias above)
    // instead of by Node's stricter ESM resolver, which throws
    // ERR_UNSUPPORTED_DIR_IMPORT.
    server: {
      deps: {
        inline: [/@mui\/material/],
      },
    },
    coverage: {
      provider: 'v8',
      reporter: ['text', 'lcov'],
      reportsDirectory: './coverage',
    },
  },
  server: {
    port: 3000,
    host: true,
    proxy: {
      '/api': {
        target: backendTarget,
        changeOrigin: true
      },
      '/ws': {
        target: backendTarget,
        ws: true
      }
    }
  }
});
