import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const backendTarget = 'http://localhost:6061';

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'build',
  },
  server: {
    proxy: {
      '/api': backendTarget,
      '/local': backendTarget,
      '/uploads': backendTarget,
      '/v0': {
        target: backendTarget,
        ws: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test-setup.js',
  },
});
