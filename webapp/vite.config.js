import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';

const backendTarget = process.env.VITE_BACKEND_TARGET || 'http://localhost:6061';
const localXiaobaTarget = 'http://127.0.0.1:3800';

const proxy = {
  '/local-xiaoba': {
    target: localXiaobaTarget,
    rewrite: (path) => path.replace(/^\/local-xiaoba/, ''),
  },
  '/api/stt/realtime': {
    target: backendTarget,
    changeOrigin: true,
    ws: true,
  },
  '/api': {
    target: backendTarget,
    changeOrigin: true,
  },
  '/local': {
    target: backendTarget,
    changeOrigin: true,
  },
  '/uploads': {
    target: backendTarget,
    changeOrigin: true,
  },
  '/v0': {
    target: backendTarget,
    changeOrigin: true,
    ws: true,
  },
};

export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      strategies: 'injectManifest',
      srcDir: 'src',
      filename: 'sw.js',
      registerType: 'prompt',
      injectRegister: null,
      includeAssets: [
        'pwa-192x192.png',
        'pwa-512x512.png',
        'pwa-maskable-512x512.png',
        'pwa-notification-badge-96x96.png',
      ],
      manifest: {
        id: '/',
        name: 'CatsCo',
        short_name: 'CatsCo',
        description: '与 AI 员工协作、分派任务并接收结果。',
        lang: 'zh-CN',
        start_url: '/',
        scope: '/',
        display: 'standalone',
        theme_color: '#f8f8f8',
        background_color: '#f8f8f8',
        categories: ['productivity', 'business'],
        icons: [
          {
            src: '/pwa-192x192.png',
            sizes: '192x192',
            type: 'image/png',
            purpose: 'any',
          },
          {
            src: '/pwa-512x512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'any',
          },
          {
            src: '/pwa-maskable-512x512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable',
          },
        ],
      },
      injectManifest: {
        // Keep only hashed entry assets and the explicit offline page in the
        // precache. Navigation HTML must stay network-only so an old shell
        // cannot hide a live service failure or reference stale bundles.
        globPatterns: [
          'assets/index-*.{js,css}',
          'assets/workbox-window.*.js',
          'offline.html',
        ],
        maximumFileSizeToCacheInBytes: 4 * 1024 * 1024,
      },
      devOptions: {
        enabled: false,
      },
    }),
  ],
  build: {
    outDir: 'build',
  },
  server: {
    proxy,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test-setup.js',
  },
});
