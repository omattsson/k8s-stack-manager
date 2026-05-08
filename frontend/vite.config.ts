import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react-swc';

const backendTarget = process.env.VITE_BACKEND_URL || 'http://backend:8081';

export default defineConfig({
  plugins: [react()],
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    css: true,
    exclude: ['e2e/**', 'node_modules/**'],
    testTimeout: 15000,
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
